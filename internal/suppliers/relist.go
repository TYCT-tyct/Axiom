package suppliers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"slices"
	"sync"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/client"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/store"
)

// The platform's pasted lines kept up to date (docs/domains/suppliers-direct.md「模型重读与余额」): every hour each key of
// a direct line whose type lists models is listed again, and the balance of one whose type reads it is read, with
// requests the provider does not charge for; every line is projected again then, and whenever the catalog's revision
// has changed. Only the platform's active direct and cloud suppliers are gone over: an account's own key never is.

const (
	// relistEvery is how often each key is listed again.
	relistEvery = time.Hour
	// reprojectEvery is how often the catalog's revision is compared with the one the lines were last projected at.
	reprojectEvery = time.Minute
)

// relistArgs is the hourly job; reprojectArgs the one that projects the lines again when the catalog changed.
type (
	relistArgs    struct{}
	reprojectArgs struct{}
)

// Kind implements river.JobArgs.
func (relistArgs) Kind() string { return "suppliers.relist" }

// Kind implements river.JobArgs.
func (reprojectArgs) Kind() string { return "suppliers.reproject" }

// InsertOpts implements river.JobArgsWithInsertOpts: one at a time, on the syncs queue, not retried (the next period
// is the retry).
func (relistArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueSyncs, MaxAttempts: 1, UniqueOpts: lineJobUnique}
}

// InsertOpts implements river.JobArgsWithInsertOpts, as relistArgs'.
func (reprojectArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueSyncs, MaxAttempts: 1, UniqueOpts: lineJobUnique}
}

// lineJobUnique keeps one job of a kind with the same arguments in the queue until it ends.
var lineJobUnique = river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable,
	rivertype.JobStateScheduled,
}}

// lines is the re-list and re-projection of one node.
type lines struct {
	db     *pg.DB
	scope  pg.Scope
	admin  *Admin
	cat    *catalog.Catalog
	rates  func(context.Context, pg.Tx) ([]platform.ExchangeRate, error)
	logger *slog.Logger

	mu       sync.Mutex
	revision int64 // the catalog revision this node last projected every line at; 0 for none yet
}

// registerLines adds the two jobs to the queue, before it starts.
func registerLines(cfg SyncConfig) error {
	admin, err := NewAdmin(AdminConfig{Keys: cfg.Keys, Egress: cfg.Egress, Registry: cfg.Registry, Clock: cfg.Clock})
	if err != nil {
		return err
	}
	cat, err := catalog.Open(cfg.DB, cfg.Scope, catalog.Config{Logger: cfg.Logger})
	if err != nil {
		return err
	}
	l := &lines{db: cfg.DB, scope: cfg.Scope, admin: admin, cat: cat, rates: cfg.Rates, logger: cfg.Logger}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(func(ctx context.Context, _ *river.Job[relistArgs]) error {
		return l.relist(ctx)
	})); err != nil {
		return fmt.Errorf("suppliers: register the re-list: %w", err)
	}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(func(ctx context.Context, _ *river.Job[reprojectArgs]) error {
		return l.reproject(ctx)
	})); err != nil {
		return fmt.Errorf("suppliers: register the re-projection: %w", err)
	}
	cfg.Queue.Every(relistEvery, func() river.JobArgs { return relistArgs{} })
	cfg.Queue.Every(reprojectEvery, func() river.JobArgs { return reprojectArgs{} })
	return l.registerCeilingChecks(cfg.Queue)
}

// relist lists every key of the lines again and projects every line. A line that fails does not keep the others
// from their turn; the errors are returned together.
func (l *lines) relist(ctx context.Context) error {
	snap, rows, err := l.read(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range rows {
		if r.Mode == "direct" {
			if err := l.relistKeys(ctx, snap, r); err != nil {
				errs = append(errs, fmt.Errorf("suppliers: re-list %s: %w", r.ID, err))
			}
		}
	}
	return errors.Join(append(errs, l.projectAll(ctx, snap, rows))...)
}

// reproject projects every line again when the catalog's revision is not the one this node last projected them at.
func (l *lines) reproject(ctx context.Context) error {
	snap, rows, err := l.read(ctx)
	if err != nil {
		return err
	}
	l.mu.Lock()
	seen := l.revision == snap.Revision()
	l.mu.Unlock()
	if seen {
		return nil
	}
	return l.projectAll(ctx, snap, rows)
}

// read is the catalog as it is now and the lines to go over.
func (l *lines) read(ctx context.Context) (*catalog.Snapshot, []store.ListPastedLinesRow, error) {
	if err := l.cat.Refresh(ctx); err != nil {
		return nil, nil, err
	}
	snap, err := l.cat.Current()
	if err != nil {
		return nil, nil, err
	}
	var rows []store.ListPastedLinesRow
	err = l.db.ReadTx(ctx, l.scope, func(tx pg.Tx) (err error) {
		rows, err = store.New().ListPastedLines(ctx, tx)
		return err
	})
	return snap, rows, err
}

// projectAll projects each line at snap, and records the revision once every one has been.
func (l *lines) projectAll(ctx context.Context, snap *catalog.Snapshot, rows []store.ListPastedLinesRow) error {
	var errs []error
	for _, r := range rows {
		if err := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error { return l.project(ctx, tx, snap, r) }); err != nil {
			errs = append(errs, fmt.Errorf("suppliers: project %s: %w", r.ID, err))
		}
	}
	if len(errs) == 0 {
		l.mu.Lock()
		l.revision = snap.Revision()
		l.mu.Unlock()
	}
	return errors.Join(errs...)
}

// pastedLine is a line of the listing with what its type and row say: its adapter, the family it speaks, its
// settlement currency, and its ratio once read under the supplier's lock (locked).
type pastedLine struct {
	store.ListPastedLinesRow
	t        adapter.Type
	family   adapter.Family
	currency platform.Currency
	ratio    *big.Rat
}

func (l *lines) pasted(r store.ListPastedLinesRow) (pastedLine, error) {
	t, ok := l.admin.TypeOf(r.Kind)
	if !ok {
		return pastedLine{}, ErrNoType
	}
	family, _ := adapter.FamilyOf(t, adapter.Family(deref(r.ApiFormat))) // the family it was connected with
	currency, err := platform.ParseCurrency(r.SettlementCurrency)
	if err != nil {
		return pastedLine{}, fmt.Errorf("the settlement currency: %w", err)
	}
	return pastedLine{ListPastedLinesRow: r, t: t, family: family, currency: currency}, nil
}

// project projects one line again, under its supplier's lock: a name waiting for a mapping that the catalog maps now
// gets an endpoint, a platform whose models the catalog offers gets one for each it offers now, and every priced
// endpoint is priced again at the type's current price and the line's ratio. A supplier no longer active is left
// alone. A type whose models a person names (deployments) is not mapped again: the name is not what the catalog knows.
func (l *lines) project(ctx context.Context, tx pg.Tx, snap *catalog.Snapshot, r store.ListPastedLinesRow) error {
	q := store.New()
	line, err := l.pasted(r)
	if err != nil {
		return err
	}
	if ok, err := line.locked(ctx, tx, q); !ok {
		return err
	}
	o, err := l.offer(ctx, tx, q, snap, line)
	if err != nil {
		return err
	}
	has, err := lineModels(ctx, tx, q, r.GroupID)
	if err != nil {
		return err
	}
	if _, _, err := insertLineModels(ctx, tx, q, r.ID, r.GroupID, snap, line.currency, o, has, line.ratio); err != nil {
		return err
	}
	_, err = l.admin.priceLine(ctx, tx, snap, Supplier{ID: r.ID, Kind: r.Kind, Origin: r.Origin, SettlementCurrency: r.SettlementCurrency}, r.GroupID, line.ratio)
	return err
}

// offer is what a projection maps: the line's names waiting for a mapping and, for a platform whose models the catalog
// offers, every model it offers now.
func (l *lines) offer(ctx context.Context, tx pg.Tx, q *store.Queries, snap *catalog.Snapshot, line pastedLine) (lineOffer, error) {
	var names []string
	if !line.t.Has(adapter.FieldDeployments) {
		listed, err := q.LineNames(ctx, tx, line.GroupID)
		if err != nil {
			return lineOffer{}, fmt.Errorf("read the line's names: %w", err)
		}
		for _, n := range listed {
			if n.ModelID == nil {
				names = append(names, n.UpstreamName)
			}
		}
	}
	ad, _ := l.admin.registry.Get(adapter.Kind(line.Kind)) // pasted found the type
	if c, ok := ad.(adapter.Cloud); ok {
		known := map[string]string{}
		names = append(names, offeredBy(c, snap, known)...)
		slices.Sort(names)
		return lineOffer{
			names: slices.Compact(names), formats: func(m catalog.Model, _ string) []protocol.Format { return c.Formats(m) },
			unmapped: c.Unmapped(), serves: line.t.Serves,
			known: func(name string) string {
				if k, ok := known[name]; ok {
					return k
				}
				return name
			},
		}, nil
	}
	return directOffer(ad.(adapter.Direct), line, names), nil
}

// directOffer is the names as a direct line of the family lists them.
func directOffer(d adapter.Direct, line pastedLine, names []string) lineOffer {
	return lineOffer{
		names: names, formats: func(m catalog.Model, name string) []protocol.Format { return d.Formats(line.family, m, name) },
		unmapped: familyFormats(line.family), serves: line.t.ServesAt(line.Origin, line.currency.String()),
	}
}

// lineModels is the models the line has an endpoint for.
func lineModels(ctx context.Context, tx pg.Tx, q *store.Queries, group uuid.UUID) (map[catalog.ModelID]bool, error) {
	served, err := q.LineEndpoints(ctx, tx, group)
	if err != nil {
		return nil, fmt.Errorf("read the line's endpoints: %w", err)
	}
	has := make(map[catalog.ModelID]bool, len(served))
	for _, e := range served {
		has[catalog.ModelID(e.ModelID)] = true
	}
	return has, nil
}

// relisted is what the provider said of one key: its listing, and its balance when its type reads one and it read.
type relisted struct {
	id      uuid.UUID
	check   adapter.KeyCheck
	balance *adapter.Money
	at      time.Time
}

// relistKeys asks the provider about each key of a direct line, a few at once, and writes what it said: a key it
// refused is rejected as the call path rejects one, a key that passed serves what it lists now (a name it newly lists
// is projected as a new key's is), and a balance read is stored. A check that says nothing about the key changes
// nothing. A type that cannot list its models for free is never asked for its list.
func (l *lines) relistKeys(ctx context.Context, snap *catalog.Snapshot, r store.ListPastedLinesRow) error {
	line, err := l.pasted(r)
	if err != nil || !line.t.Lists && !line.t.Balance {
		return err
	}
	var keys []lineKey
	if err := l.db.ReadTx(ctx, l.scope, func(tx pg.Tx) (err error) {
		keys, err = l.keysOf(ctx, tx, line)
		return err
	}); err != nil {
		return err
	}
	said := l.ask(ctx, line, keys)
	written := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error { return l.writeKeys(ctx, tx, snap, line, said) })
	return errors.Join(written, l.relistCeilings(ctx, line, keys, said))
}

// keysOf is a line's keys, opened, each asked through its calls' exit; one whose exit is disabled waits for it, as calls do.
func (l *lines) keysOf(ctx context.Context, tx pg.Tx, line pastedLine) ([]lineKey, error) {
	rows, err := store.New().ListLineKeys(ctx, tx, line.ID)
	if err != nil {
		return nil, fmt.Errorf("read the keys: %w", err)
	}
	exits, err := l.admin.readExits(ctx, tx)
	if err != nil {
		return nil, err
	}
	defer l.admin.keepExits(exits)
	var keys []lineKey
	for _, k := range rows {
		exit, _, err := exits.client(k.EgressID)
		switch {
		case errors.Is(err, errNoExit):
			continue
		case err != nil:
			return nil, fmt.Errorf("credential %s: %w", k.ID, err)
		}
		c, err := client.New(line.Origin, exit, client.NewLimiter(nil, l.admin.now), l.admin.now)
		if err != nil {
			return nil, err
		}
		key, err := l.admin.openKey(ctx, tx, k.ID)
		if err != nil {
			return nil, fmt.Errorf("credential %s: %w", k.ID, err)
		}
		keys = append(keys, lineKey{id: k.ID, key: key, settings: settingsOf(k.Settings), client: c})
	}
	return keys, nil
}

// lineKey is one key of a line, opened for the requests that ask about it and kept nowhere, and the client they go by.
type lineKey struct {
	id       uuid.UUID
	key      secret.Secret
	settings adapter.Settings
	client   adapter.Client
}

// ask is what the provider says of each key, a few keys at once.
func (l *lines) ask(ctx context.Context, line pastedLine, keys []lineKey) []relisted {
	said := make([]relisted, len(keys))
	var wg sync.WaitGroup
	slots := make(chan struct{}, checkParallel)
	for i, k := range keys {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			said[i] = l.askKey(ctx, line, k) // each goroutine writes only its own element
		})
	}
	wg.Wait()
	return said
}

// askKey lists one key's models, when its type lists them, and reads its balance, when its type reads one. A balance
// that does not read stays as last read, and the next hour reads it again.
func (l *lines) askKey(ctx context.Context, line pastedLine, key lineKey) relisted {
	ad, _ := l.admin.registry.Get(adapter.Kind(line.Kind))
	d := ad.(adapter.Direct) // the line's mode is direct
	k := relisted{id: key.id}
	if line.t.Lists {
		k.check = d.CheckKey(ctx, key.client, line.family, key.key, key.settings)
	}
	if b, ok := d.(adapter.Balancer); ok && line.t.Balance {
		m, err := b.ReadBalance(ctx, key.client, key.key, key.settings, line.currency)
		if err != nil {
			l.logger.WarnContext(ctx, "suppliers: a key's balance did not read", "supplier", line.ID.String(), "credential", key.id.String(), "err", err)
		} else {
			k.balance = &m
		}
	}
	k.at = l.admin.now()
	return k
}

// writeKeys writes what the provider said of the keys, under the supplier's lock; a supplier no longer active is left
// alone.
func (l *lines) writeKeys(ctx context.Context, tx pg.Tx, snap *catalog.Snapshot, line pastedLine, said []relisted) error {
	q := store.New()
	if ok, err := line.locked(ctx, tx, q); !ok {
		return err
	}
	var listed []string
	for _, k := range said {
		switch k.check.Outcome {
		case adapter.KeyFailed:
			if err := persist.RejectCredential(ctx, tx, line.ID, k.id, k.at); err != nil {
				return err
			}
		case adapter.KeyPassed:
			listed = append(listed, k.check.Models...)
		case adapter.KeyInconclusive:
		}
		if err := l.storeBalance(ctx, tx, q, line, k); err != nil {
			return err
		}
	}
	slices.Sort(listed)
	listed = slices.Compact(listed)
	if err := l.addListed(ctx, tx, q, snap, line, listed); err != nil {
		return err
	}
	return serveListed(ctx, tx, q, snap, line, said, listed)
}

// addListed projects the names the keys list that the line does not have yet, as a new key's are.
func (l *lines) addListed(ctx context.Context, tx pg.Tx, q *store.Queries, snap *catalog.Snapshot, line pastedLine, listed []string) error {
	rows, err := q.LineNames(ctx, tx, line.GroupID)
	if err != nil {
		return fmt.Errorf("read the line's names: %w", err)
	}
	known := make(map[string]bool, len(rows))
	for _, row := range rows {
		known[row.UpstreamName] = true
	}
	fresh := slices.DeleteFunc(slices.Clone(listed), func(n string) bool { return known[n] })
	has, err := lineModels(ctx, tx, q, line.GroupID)
	if err != nil {
		return err
	}
	ad, _ := l.admin.registry.Get(adapter.Kind(line.Kind))
	_, _, err = insertLineModels(ctx, tx, q, line.ID, line.GroupID, snap, line.currency, directOffer(ad.(adapter.Direct), line, fresh), has, line.ratio)
	return err
}

// serveListed makes each key that passed serve what it lists now, and the line's endpoints it lists by another name.
func serveListed(ctx context.Context, tx pg.Tx, q *store.Queries, snap *catalog.Snapshot, line pastedLine, said []relisted, listed []string) error {
	names, err := lineNames(ctx, tx, q, line.GroupID, snap, lineOffer{names: listed})
	if err != nil {
		return err
	}
	for _, k := range said {
		if k.check.Outcome != adapter.KeyPassed {
			continue
		}
		allowed, calls, err := lineServes(k.check.Models, nil, names)
		if err != nil {
			return err
		}
		if err := q.SetKeyModels(ctx, tx, store.SetKeyModelsParams{ID: k.id, SupplierID: line.ID, AllowedModels: allowed, CallNames: calls}); err != nil {
			return fmt.Errorf("credential %s: its models: %w", k.id, err)
		}
	}
	return nil
}

// storeBalance stores a key's balance as read, in the price entry's currency: one the provider tells in another is
// converted at the platform's exchange rate, rounded down, and not stored while there is none.
func (l *lines) storeBalance(ctx context.Context, tx pg.Tx, q *store.Queries, line pastedLine, k relisted) error {
	if k.balance == nil {
		return nil
	}
	amount := k.balance.Amount
	if k.balance.Currency != line.currency {
		rate, ok, err := l.rate(ctx, tx, k.balance.Currency, line.currency)
		if err != nil || !ok {
			return err
		}
		if amount, err = floorExact(rate.Mul(rate, amount.Rat())); err != nil {
			return fmt.Errorf("credential %s: its balance converted: %w", k.id, err)
		}
	}
	if err := q.SetKeyBalance(ctx, tx, store.SetKeyBalanceParams{
		ID: k.id, SupplierID: line.ID, BalanceNative: exactText(amount), ObservedAt: &k.at,
	}); err != nil {
		return fmt.Errorf("credential %s: its balance: %w", k.id, err)
	}
	return nil
}

// rate is what one unit of from is worth in to by the platform's exchange rates; false when they have no such pair.
func (l *lines) rate(ctx context.Context, tx pg.Tx, from, to platform.Currency) (*big.Rat, bool, error) {
	if l.rates == nil {
		return nil, false, nil
	}
	rates, err := l.rates(ctx, tx)
	if err != nil {
		return nil, false, fmt.Errorf("read the exchange rates: %w", err)
	}
	for _, r := range rates {
		if rate, ok := r.Between(from, to); ok {
			return rate, true, nil
		}
	}
	return nil, false, nil
}
