package suppliers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strconv"
	"sync"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/platform"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/client"
	"axiom/internal/suppliers/internal/store"
)

// The attempts at a ceiling plan checked after they settled (docs/domains/suppliers-direct.md「OpenRouter 的成本」),
// every minute: one the answer said no cost for has its generation's record read and its cost booked from it, once; a
// figure is judged against the plan's bound, and an endpoint that a figure went past, or that a BYOK call ran on, leaves
// routing and is alerted on. The record is read outside any transaction, the bookings after it in one.

const (
	// checkEvery is how often the checks run.
	checkEvery = time.Minute
	// lookupAfter is how long after an attempt ends its record is first read: the aggregator writes it a little after
	// the answer.
	lookupAfter = time.Minute
	// lookupFor is how long a record that is not there is waited for; the attempt then stays booked at the bound.
	lookupFor = 24 * time.Hour
	// checkBatch is the most attempts of each kind a run takes; a backlog is worked off over the next runs.
	checkBatch = 100
)

// ceilingChecksArgs is the job.
type ceilingChecksArgs struct{}

// Kind implements river.JobArgs.
func (ceilingChecksArgs) Kind() string { return "suppliers.ceiling_checks" }

// InsertOpts implements river.JobArgsWithInsertOpts: one at a time, on the syncs queue, which waits on the aggregator,
// and not retried: the next minute is the retry.
func (ceilingChecksArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueSyncs, MaxAttempts: 1, UniqueOpts: lineJobUnique}
}

// registerCeilingChecks adds the job to the queue, before it starts.
func (l *lines) registerCeilingChecks(q *pg.Queue) error {
	if err := river.AddWorkerSafely(q.Workers(), river.WorkFunc(func(ctx context.Context, _ *river.Job[ceilingChecksArgs]) error {
		return l.checkCeilings(ctx)
	})); err != nil {
		return fmt.Errorf("suppliers: register the ceiling checks: %w", err)
	}
	q.Every(checkEvery, func() river.JobArgs { return ceilingChecksArgs{} })
	return nil
}

// checkCeilings reads the records still to read, then judges the figures still to judge.
func (l *lines) checkCeilings(ctx context.Context) error {
	return errors.Join(l.lookUp(ctx), l.judge(ctx))
}

// lookup is one attempt whose generation's record is to be read, with the key it ran on.
type lookup struct {
	store.CeilingLookupsRow
	key    secret.Secret
	client adapter.Client
	ag     adapter.Aggregator
}

// lookUp reads the record of each attempt that settled without the aggregator's figure and books the ones found. Only
// lookupFor running out ends a lookup with no record, and a key that no longer opens: a read that failed, or found no
// record yet, is read again next run.
func (l *lines) lookUp(ctx context.Context) error {
	now := l.admin.now()
	var due []lookup
	var shut []uuid.UUID
	if err := l.db.ReadTx(ctx, l.scope, func(tx pg.Tx) (err error) {
		due, shut, err = l.lookups(ctx, tx, now)
		return err
	}); err != nil {
		return err
	}
	read := make([]adapter.Generation, len(due))
	var wg sync.WaitGroup
	slots := make(chan struct{}, ceilingParallel)
	for i, d := range due {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			g, err := d.ag.ReadGeneration(ctx, d.client, d.key, settingsOf(d.Settings), d.GenerationID)
			if err != nil {
				l.logger.WarnContext(ctx, "suppliers: a generation's record did not read", "generation_id", d.GenerationID, "err", err)
				return // its zero value is no record: read again next run
			}
			read[i] = g // each its own element
		})
	}
	wg.Wait()
	return l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		q := store.New()
		for i, d := range due {
			if err := bookLookup(ctx, tx, q, d.CeilingLookupsRow, read[i]); err != nil {
				return err
			}
		}
		for _, credential := range shut {
			if _, err := q.EndKeyLookups(ctx, tx, credential); err != nil {
				return err
			}
		}
		_, err := q.EndStaleLookups(ctx, tx, now.Add(-lookupFor))
		return err
	})
}

// lookupsPerKey is the most attempts of one key a run reads, so that a key whose records cannot be read now leaves the
// rest of the batch to the others.
const lookupsPerKey = 20

// lookups is the attempts due, each with its key opened and the client of its exit, and the keys that no longer open,
// whose attempts no run can read. One whose exit is disabled waits for it, as calls do.
func (l *lines) lookups(ctx context.Context, tx pg.Tx, now time.Time) ([]lookup, []uuid.UUID, error) {
	rows, err := store.New().CeilingLookups(ctx, tx, store.CeilingLookupsParams{Before: now.Add(-lookupAfter), PerKey: lookupsPerKey, MaxRows: checkBatch})
	if err != nil || len(rows) == 0 {
		return nil, nil, err
	}
	exits, err := l.admin.readExits(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	defer l.admin.keepExits(exits)
	var due []lookup
	var shut []uuid.UUID
	opened := map[uuid.UUID]*secret.Secret{} // a key is opened once a run, however many of its attempts are due
	for _, r := range rows {
		ag, ok := l.admin.aggregator(r.Kind)
		exit, _, err := exits.client(r.EgressID)
		if !ok || err != nil {
			continue
		}
		c, err := client.New(r.Origin, exit, client.NewLimiter(nil, l.admin.now), l.admin.now)
		if err != nil {
			return nil, nil, err
		}
		key, first := l.lookupKey(ctx, tx, opened, r.CredentialID)
		if key == nil && first {
			shut = append(shut, r.CredentialID)
		}
		if key != nil {
			due = append(due, lookup{CeilingLookupsRow: r, key: *key, client: c, ag: ag})
		}
	}
	return due, shut, nil
}

// lookupKey is the credential's key, opened once a run and kept in opened, nil when it does not open, which is logged;
// first says this call opened it.
func (l *lines) lookupKey(ctx context.Context, tx pg.Tx, opened map[uuid.UUID]*secret.Secret, credential uuid.UUID) (*secret.Secret, bool) {
	if key, seen := opened[credential]; seen {
		return key, false
	}
	key, err := l.admin.openKey(ctx, tx, credential)
	if err != nil {
		l.logger.WarnContext(ctx, "suppliers: a key did not open for a generation's record; its attempts stay at the bound", "credential", credential.String(), "err", err)
		opened[credential] = nil
		return nil, true
	}
	opened[credential] = &key
	return &key, true
}

// bookLookup books one record found against its attempt (SettleLookup, once a generation): the cost is the record's
// figure times the line's ratio the attempt ran at, and for a BYOK call that fee on top of the bound the attempt was
// booked at, since the provider bills the model apart. No record found books nothing.
func bookLookup(ctx context.Context, tx pg.Tx, q *store.Queries, r store.CeilingLookupsRow, g adapter.Generation) error {
	if !g.Found {
		return nil
	}
	ratio, ok := new(big.Rat).SetString(r.LineRatio)
	bound, err := platform.ParseExact(r.EstimatedNative)
	if !ok || err != nil {
		return fmt.Errorf("generation %s: its ratio %q or its cost %q is not a number", r.GenerationID, r.LineRatio, r.EstimatedNative)
	}
	cost, err := platform.CeilExact(new(big.Rat).Mul(g.Cost.Rat(), ratio))
	report := reportCharged
	if err == nil && g.BYOK {
		cost, err = cost.Add(bound)
		report = reportBYOKFee
	}
	if err != nil {
		return fmt.Errorf("generation %s: %w", r.GenerationID, err)
	}
	native, err := decimal(cost)
	if err != nil {
		return err
	}
	figure, err := decimal(g.Cost)
	if err != nil {
		return err
	}
	_, err = q.SettleLookup(ctx, tx, store.SettleLookupParams{ActualNative: native, ReportedCost: figure, CostReport: &report, GenerationID: &r.GenerationID})
	return err
}

// judge judges each figure against its bound, in credits before the ratio: a BYOK call's always takes its endpoint out,
// and a figure past the bound by more than the tolerance (1e-10 and 1% of the bound) does when the usage it was bounded
// by was all reported. Either takes the endpoint out only while it is on the plan the attempt was costed at.
func (l *lines) judge(ctx context.Context) error {
	var found map[string][]uuid.UUID
	err := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		found = map[string][]uuid.UUID{}
		q := store.New()
		rows, err := q.CeilingJudgments(ctx, tx, checkBatch)
		if err != nil {
			return err
		}
		for _, r := range rows {
			finding, err := judgeFigure(ctx, tx, q, r)
			if err != nil {
				return err
			}
			if finding != "" {
				found[finding] = append(found[finding], r.EndpointID)
			}
		}
		return persistCeilingAlerts(ctx, tx, found)
	})
	if err != nil {
		return fmt.Errorf("suppliers: judge the ceiling figures: %w", err)
	}
	for finding, endpoints := range found {
		for _, id := range endpoints {
			l.logger.ErrorContext(ctx, "suppliers.ceiling", "finding", finding, "endpoint_id", id.String())
		}
	}
	return nil
}

// The findings of a judgment, by their alert's key.
const (
	findingBYOK     = "ceiling_byok"
	findingExceeded = "ceiling_exceeded"
)

// judgeFigure judges one figure and marks it judged. It returns the finding that took its endpoint out of routing, ""
// when it took none.
func judgeFigure(ctx context.Context, tx pg.Tx, q *store.Queries, r store.CeilingJudgmentsRow) (string, error) {
	over, err := overBound(r)
	if err != nil {
		return "", err
	}
	finding := map[bool]string{true: findingExceeded}[over]
	if r.CostReport == reportBYOKFee {
		finding = findingBYOK
	}
	if finding != "" {
		n, err := q.DelistCeilingEndpoint(ctx, tx, store.DelistCeilingEndpointParams{ID: r.EndpointID, CostScheduleID: &r.CostScheduleID})
		if err != nil {
			return "", err
		}
		if n == 0 { // on another plan by now, or out of routing already
			finding = ""
		}
	}
	return finding, q.EndJudgment(ctx, tx, store.EndJudgmentParams{RequestID: r.RequestID, AttemptSeq: r.AttemptSeq})
}

// ceilingTolerance is the aggregator's own rounding of a charge, in credits, on top of 1% of the bound: a charge
// within both is the bound's.
var ceilingTolerance = big.NewRat(1, 10_000_000_000)

// overBound reports a figure past its bound by more than the tolerance, for usage that was all reported.
func overBound(r store.CeilingJudgmentsRow) (bool, error) {
	if r.CostFlag != CostComplete.String() || r.ReportedCost == "" || r.BoundCost == "" {
		return false, nil
	}
	figure, ok1 := new(big.Rat).SetString(r.ReportedCost)
	bound, ok2 := new(big.Rat).SetString(r.BoundCost)
	if !ok1 || !ok2 {
		return false, fmt.Errorf("attempt %s/%d: its figure %q or bound %q is not a number", r.RequestID, r.AttemptSeq, r.ReportedCost, r.BoundCost)
	}
	limit := new(big.Rat).Add(bound, ceilingTolerance)
	limit.Add(limit, new(big.Rat).Mul(bound, big.NewRat(1, 100)))
	return figure.Cmp(limit) > 0, nil
}

// persistCeilingAlerts raises one alert for the endpoints each kind of finding took out of routing.
func persistCeilingAlerts(ctx context.Context, tx pg.Tx, found map[string][]uuid.UUID) error {
	for _, key := range slices.Sorted(maps.Keys(found)) {
		s := alert.Signal{Kind: "suppliers.reconciliation_difference", Key: key, Severity: "critical", Fields: map[string]string{"value": strconv.Itoa(len(found[key]))}}
		if err := alert.Persist(ctx, tx, uuid.NewV7(), s); err != nil {
			return err
		}
	}
	return nil
}

// relistCeilings reads the highest price of every name of an aggregator's line the catalog maps, a few at once, with a
// key the aggregator just let in, and stores what it said: a ceiling read, or none for a name nobody serves or one
// priced in a way the plan cannot hold. A read that says nothing leaves the ceiling as it was; ceilingFresh bounds how
// long that one prices the endpoint. The projection after the re-list prices the line from them.
func (l *lines) relistCeilings(ctx context.Context, line pastedLine, keys []lineKey, said []relisted) error {
	ag, ok := l.admin.aggregator(line.Kind)
	i := slices.IndexFunc(said, func(k relisted) bool { return k.check.Outcome == adapter.KeyPassed })
	if !ok || i < 0 {
		return nil
	}
	key := keys[i] // ask answers each key in its place
	var rows []store.NameCeilingsRow
	if err := l.db.ReadTx(ctx, l.scope, func(tx pg.Tx) (err error) {
		rows, err = store.New().NameCeilings(ctx, tx, line.GroupID)
		return err
	}); err != nil {
		return err
	}
	// Each name is stored as soon as it is read, in a transaction of its own: a round the job's deadline cuts short keeps
	// what it read, and the next round reads the rest.
	errs := make([]error, len(rows))
	var wg sync.WaitGroup
	slots := make(chan struct{}, ceilingParallel)
	for i, r := range rows {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			c := ag.ReadCeiling(ctx, key.client, key.key, key.settings, r.UpstreamName)
			errs[i] = l.db.Tx(ctx, l.scope, func(tx pg.Tx) error { // each its own element
				q := store.New()
				if ok, err := line.locked(ctx, tx, q); !ok {
					return err
				}
				return storeCeiling(ctx, tx, q, line.GroupID, r.UpstreamName, c, l.admin.now())
			})
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// ceilingParallel is how many of a line's names are read at once.
const ceilingParallel = 4

// storeCeiling stores what a ceiling's read said of one name.
func storeCeiling(ctx context.Context, tx pg.Tx, q *store.Queries, group uuid.UUID, name string, c adapter.Ceiling, at time.Time) error {
	switch c.Outcome {
	case adapter.CeilingInconclusive:
		return nil
	case adapter.CeilingRead:
		b, err := c.Schedule.MarshalJSON()
		if err != nil {
			return err
		}
		return q.SetNameCeiling(ctx, tx, store.SetNameCeilingParams{SupplierGroupID: group, UpstreamName: name, Ceiling: b, ReadAt: &at})
	case adapter.CeilingNone, adapter.CeilingUnexpressible:
	}
	return q.ClearNameCeiling(ctx, tx, store.ClearNameCeilingParams{SupplierGroupID: group, UpstreamName: name})
}
