package suppliers

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

// originPattern is the origin a supplier is connected at: https, a host in lower case, a port if it is not the default.
var unitValue = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})(\.[0-9]{1,15})?$`)

// ConfirmUnitValue records what one unit of the supplier's balance is worth, in its settlement currency. A later value
// applies from now: the earlier ones stay, and what was costed keeps its own.
func (a *Admin) ConfirmUnitValue(ctx context.Context, tx pg.Tx, actor audit.Actor, id uuid.UUID, value, reason string) (Supplier, error) {
	if !unitValue.MatchString(value) {
		return Supplier{}, ErrUnitValue
	}
	if v, _ := new(big.Rat).SetString(value); v.Sign() <= 0 || v.Cmp(big.NewRat(1000, 1)) > 0 {
		return Supplier{}, ErrUnitValue
	}
	row, err := a.locked(ctx, tx, id)
	if err != nil {
		return Supplier{}, err
	}
	if err := store.New().InsertUnitValue(ctx, tx, store.InsertUnitValueParams{SupplierID: id, Value: value, Actor: actor.String(), Reason: reason}); err != nil {
		return Supplier{}, fmt.Errorf("keep the unit value: %w", err)
	}
	if err := a.record(ctx, tx, actor, "supplier.unit_value", id, reason, map[string]any{"value": value, "status": row.Status}); err != nil {
		return Supplier{}, err
	}
	return a.view(ctx, tx, id)
}

// SelectGroup chooses whether a supplier's group is used. The first time it is selected it gets its code name, which
// never changes; unselecting takes it out of routing and keeps the name. The sync makes the endpoints of the models.
func (a *Admin) SelectGroup(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, group uuid.UUID, selected bool, reason string) (Group, error) {
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return Group{}, err
	}
	q := store.New()
	rows, err := q.LockGroup(ctx, tx, store.LockGroupParams{ID: group, SupplierID: supplier})
	switch {
	case err != nil:
		return Group{}, fmt.Errorf("lock the group: %w", err)
	case len(rows) == 0:
		return Group{}, ErrNoGroup
	case selected && !rows[0].Present:
		return Group{}, ErrGroupGone
	}
	if rows[0].Selected != selected {
		if err := a.chooseGroup(ctx, tx, group, selected, rows[0].Codename == nil); err != nil {
			return Group{}, err
		}
		if err := q.BumpConfigGen(ctx, tx, supplier); err != nil {
			return Group{}, fmt.Errorf("raise the generation: %w", err)
		}
		action := "supplier_group.unselect"
		if selected {
			action = "supplier_group.select"
		}
		if err := a.record(ctx, tx, actor, action, supplier, reason, map[string]any{"group": group.String()}); err != nil {
			return Group{}, err
		}
	}
	return a.group(ctx, tx, supplier, group)
}

// chooseGroup writes the selection; a group selected for the first time gets a code name. A name is 40 random bits, so a
// name another group has is not to be expected, and if it happens the table refuses it, the change fails whole, and
// asking again makes another.
func (*Admin) chooseGroup(ctx context.Context, tx pg.Tx, group uuid.UUID, selected, nameless bool) error {
	params := store.SelectGroupParams{ID: group, Selected: selected}
	if selected && nameless {
		name := store.Codename()
		params.Codename = &name
	}
	if err := store.New().SelectGroup(ctx, tx, params); err != nil {
		return fmt.Errorf("select the group: %w", err)
	}
	return nil
}

// group reads one group of a supplier.
func (*Admin) group(ctx context.Context, tx pg.Tx, supplier, id uuid.UUID) (Group, error) {
	rows, err := store.New().GetGroupOf(ctx, tx, store.GetGroupOfParams{SupplierID: supplier, ID: id})
	if err != nil {
		return Group{}, fmt.Errorf("read the group: %w", err)
	}
	if len(rows) == 0 {
		return Group{}, ErrNoGroup
	}
	g := rows[0]
	return groupOf(groupFacts{
		ID: g.ID, Ref: g.UpstreamRef, Name: g.Name, Ratio: g.Ratio, Facts: g.Facts, Selected: g.Selected,
		Present: g.Present, KeyEligible: g.KeyEligible, Codename: g.Codename, KeyError: g.KeyError,
		Pool: poolOf(g.PoolAccounts, g.PoolFailed, g.PoolUsable, g.PoolHeadroom, g.PoolCapacity, g.PoolLow, g.PoolObservedAt),
	})
}

// defaultMaxConcurrency is how many calls a key serves at once until a person sets another number.
const defaultMaxConcurrency = 4

// BindKey binds one of the supplier's keys to a selected group, by the supplier's own ID of it. It is the exception: the
// sync makes the keys a group needs by itself. The credential is `binding` until the sync has read the key's text from
// the supplier, sealed it and activated it. A key is bound once.
func (a *Admin) BindKey(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, group uuid.UUID, keyRef, reason string) (Credential, error) {
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return Credential{}, err
	}
	g, err := a.group(ctx, tx, supplier, group)
	switch {
	case err != nil:
		return Credential{}, err
	case !g.Selected:
		return Credential{}, ErrGroupNotSelected
	}
	id := uuid.NewV7()
	err = store.New().InsertBindingCredential(ctx, tx, store.InsertBindingCredentialParams{
		ID: id, SupplierID: supplier, SupplierGroupID: group, UpstreamKeyRef: keyRef, MaxConcurrency: defaultMaxConcurrency,
	})
	if _, duplicate := pg.Duplicate(err); duplicate {
		return Credential{}, ErrKeyBound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("bind the key: %w", err)
	}
	if err := store.New().BumpConfigGen(ctx, tx, supplier); err != nil {
		return Credential{}, fmt.Errorf("raise the generation: %w", err)
	}
	if err := a.record(ctx, tx, actor, "credential.bind", supplier, reason, map[string]any{"group": group.String(), "credential": id.String(), "upstream_key_ref": keyRef}); err != nil {
		return Credential{}, err
	}
	return Credential{ID: id, Group: group, UpstreamKeyRef: keyRef, Status: "binding", MaxConcurrency: defaultMaxConcurrency}, nil
}

// Limits are a credential's concurrency and its ceiling on the upstream account's shared rolling minute in this node.
type Limits struct {
	// MaxConcurrency is the calls in flight at once; at least one, or the key would serve nothing.
	MaxConcurrency int
	// RequestsPerMinute is the account RPM ceiling for selecting this credential; nil adds no ceiling.
	RequestsPerMinute *int
}

// maxLimit is the largest concurrency the int4 column can hold; a number above it would fail there.
const maxLimit = math.MaxInt32

// MaxRequestsPerMinute is the largest per-minute ceiling a credential may have: finite so the rolling window's counts
// and the memory of unlimited traffic stay bounded (issue #382).
const MaxRequestsPerMinute = 100_000

// valid is whether the columns can hold these limits: concurrency 1 to 2³¹-1, and requests a minute 1 to
// MaxRequestsPerMinute or none.
func (l Limits) valid() bool {
	if l.MaxConcurrency < 1 || l.MaxConcurrency > maxLimit {
		return false
	}
	return l.RequestsPerMinute == nil || *l.RequestsPerMinute >= 1 && *l.RequestsPerMinute <= MaxRequestsPerMinute
}

// SetLimits sets how much of a credential this node gives out, and records it. A credential already as asked changes
// nothing and records nothing. The counts themselves are each node's own, in memory (README.md「并发与速率」), so the
// change reaches this node at its next Reload and the others at theirs.
func (a *Admin) SetLimits(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, credentialID uuid.UUID, limits Limits, reason string) (Credential, error) {
	if !limits.valid() {
		return Credential{}, ErrLimits
	}
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return Credential{}, err
	}
	concurrency := int32(limits.MaxConcurrency) //nolint:gosec // valid checked it is 1 to 2³¹-1
	var perMinute *int32
	if limits.RequestsPerMinute != nil {
		n := int32(*limits.RequestsPerMinute) //nolint:gosec // valid checked it is 1 to MaxRequestsPerMinute
		perMinute = &n
	}
	q := store.New()
	rows, err := q.GetCredentialOf(ctx, tx, store.GetCredentialOfParams{SupplierID: supplier, ID: credentialID})
	if err != nil {
		return Credential{}, fmt.Errorf("read the credential: %w", err)
	}
	if len(rows) == 0 {
		return Credential{}, ErrNoCredential
	}
	row := rows[0]
	if row.Status == "erased" {
		return Credential{}, ErrStatusLocked
	}
	if row.MaxConcurrency == concurrency && sameLimit(row.RequestsPerMinute, perMinute) {
		return credentialOf(row.ID, row.SupplierGroupID, row.UpstreamKeyRef, row.Status, row.CooldownUntil, row.ExpiresAt, row.MaxConcurrency, row.RequestsPerMinute), nil
	}
	changed, err := q.SetCredentialLimits(ctx, tx, store.SetCredentialLimitsParams{
		SupplierID: supplier, ID: credentialID, MaxConcurrency: concurrency, RequestsPerMinute: perMinute,
	})
	if err != nil {
		return Credential{}, fmt.Errorf("set the limits: %w", err)
	}
	if changed == 0 { // the row was there a moment ago, and the supplier's lock is held
		return Credential{}, ErrNoCredential
	}
	if err := a.record(ctx, tx, actor, "credential.limits", supplier, reason, map[string]any{
		"credential":               row.ID.String(),
		"max_concurrency_from":     int(row.MaxConcurrency),
		"max_concurrency_to":       int(concurrency),
		"requests_per_minute_from": limited(row.RequestsPerMinute),
		"requests_per_minute_to":   limited(perMinute),
	}); err != nil {
		return Credential{}, err
	}
	return credentialOf(row.ID, row.SupplierGroupID, row.UpstreamKeyRef, row.Status, row.CooldownUntil, row.ExpiresAt, concurrency, perMinute), nil
}

// sameLimit is whether two per-minute limits are the same, absent counting as a value.
func sameLimit(a, b *int32) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// Mapping is an alias: the supplier's name for a model, in a group or in the whole supplier.
type Mapping struct {
	ID, Supplier uuid.UUID
	Group        *uuid.UUID
	UpstreamName string
	Model        uuid.UUID
	Revision     int64
	Formats      []string // the call formats the alias declares beyond the adapter's, canonical names
}

// afterMapping raises the generation and records an alias change. The formats-only path joins here so it shares the
// audit and idempotency with creates and revisions.
func (a *Admin) afterMapping(ctx context.Context, tx pg.Tx, q *store.Queries, actor audit.Actor, supplier uuid.UUID, action, reason string, detail map[string]any, m Mapping) error {
	if err := q.BumpConfigGen(ctx, tx, supplier); err != nil {
		return err
	}
	detail["alias"], detail["revision"] = m.ID.String(), m.Revision
	if err := a.record(ctx, tx, actor, action, supplier, reason, detail); err != nil {
		return err
	}
	return nil
}

// checkMappingRevision runs under the supplier lock, so two corrections of one revision cannot both commit.
func checkMappingRevision(rows []store.CurrentAliasRow, group *uuid.UUID, rev *int64) error {
	if rev != nil && (len(rows) == 0 || rows[0].Revision != *rev) {
		return ErrAliasRevision
	}
	if len(rows) != 0 && (group == nil || rev == nil) {
		return ErrAliasExists
	}
	return nil
}

// PendingMapping is an upstream name waiting to be mapped.
type PendingMapping struct {
	Supplier, Group uuid.UUID
	GroupCodename   string
	UpstreamName    string
	Formats         []string
	FirstSeen       time.Time
}

// PendingQuery chooses the names to list, oldest first. A zero Supplier is every supplier; After is the last name of
// the page before, nil for the first page.
type PendingQuery struct {
	Supplier uuid.UUID
	After    *PendingMapping
	PageSize int
}

// PendingMappings lists a page of names that map to no model; more tells whether names remain beyond it.
func (*Admin) PendingMappings(ctx context.Context, tx pg.Tx, q PendingQuery) (names []PendingMapping, more bool, err error) {
	params := store.PendingMappingsPageParams{PageSize: int32(q.PageSize + 1)} // #nosec G115 -- a page is at most 200
	if q.Supplier != (uuid.UUID{}) {
		params.SupplierID = &q.Supplier
	}
	if after := q.After; after != nil {
		params.AfterSeen, params.AfterGroup, params.AfterName = &after.FirstSeen, &after.Group, &after.UpstreamName
	}
	rows, err := store.New().PendingMappingsPage(ctx, tx, params)
	if err != nil {
		return nil, false, fmt.Errorf("list the names to map: %w", err)
	}
	if more = len(rows) > q.PageSize; more {
		rows = rows[:q.PageSize]
	}
	names = make([]PendingMapping, len(rows))
	for i, r := range rows {
		names[i] = PendingMapping{
			Supplier: r.SupplierID, Group: r.SupplierGroupID, GroupCodename: deref(r.Codename), UpstreamName: r.UpstreamName,
			Formats: r.Formats, FirstSeen: r.FirstSeenAt,
		}
	}
	return names, more, nil
}
