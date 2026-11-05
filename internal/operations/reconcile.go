package operations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// The reconciliation of the cap counters (docs/domains/operations.md「失败与对账」): the counters are a running sum of
// the money actions, kept in the same transactions, so they can always be added up again from them. A scope and
// category whose counters of the current UTC day and ISO week differ is paused, and every proposal there waits for the
// owner, until the owner has the counters recomputed (RepairCaps); nothing writes a number by hand.

// capDriftSignal is the alert of a paused scope and category, keyed by both: raised when it is paused, resolved when
// the owner repairs it.
const capDriftSignal = "operations.cap_drift"

// reconcileActor is who pauses a scope whose counters drifted.
var reconcileActor = audit.System("operations.cap_reconcile")

// ReconcileCaps adds up the counters of the current UTC day and ISO week again from the money actions and pauses every
// scope and category where they differ, recorded and alerted once; it returns how many it paused now. Each is
// checked again under the lock of its policy before it is paused, so a repair that ran meanwhile is not undone, and in
// its own transaction, so one that fails leaves the others paused.
func ReconcileCaps(ctx context.Context, db *pg.DB, scope pg.Scope) (int, error) {
	var drift []CapDriftRow
	if err := db.ReadTx(ctx, scope, func(tx pg.Tx) (err error) {
		drift, err = driftOf(ctx, tx, CapDriftParams{Every: true})
		return err
	}); err != nil {
		return 0, err
	}
	paused := 0
	var errs []error // one scope that cannot be paused does not keep the others running
	for i, d := range drift {
		if i > 0 && d.Category == drift[i-1].Category && sameSite(d.SiteID, drift[i-1].SiteID) {
			continue // the rows of a scope are adjacent, and the scope is checked whole
		}
		var now bool
		errs = append(errs, db.Tx(ctx, scope, func(tx pg.Tx) (err error) {
			now, err = pauseDrifted(ctx, tx, Category(d.Category), d.SiteID)
			return err
		}))
		if now {
			paused++
		}
	}
	return paused, errors.Join(errs...)
}

// driftOf is CapDrift in the current UTC day and ISO week by the database's clock as it is now.
func driftOf(ctx context.Context, tx pg.Tx, p CapDriftParams) ([]CapDriftRow, error) {
	q := New()
	w, err := q.CapWindows(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("read the cap windows: %w", err)
	}
	p.DayStart, p.WeekStart = w.DayStart, w.WeekStart
	drift, err := q.CapDrift(ctx, tx, p)
	if err != nil {
		return nil, fmt.Errorf("add up the cap counters: %w", err)
	}
	return drift, nil
}

// pauseDrifted pauses the policy of site and category if its counters still differ, and reports whether it paused it
// now.
func pauseDrifted(ctx context.Context, tx pg.Tx, category Category, site *uuid.UUID) (bool, error) {
	q := New()
	policies, err := q.LockCapPolicyForUpdate(ctx, tx, LockCapPolicyForUpdateParams{Category: string(category), SiteID: site})
	if err != nil {
		return false, fmt.Errorf("lock the cap policy: %w", err)
	}
	if len(policies) == 0 { // money moves only under a policy, and none is ever deleted
		return false, fmt.Errorf("the cap counters of %s in %s drift, and it has no policy to pause", category, scopeName(site))
	}
	drift, err := driftOf(ctx, tx, CapDriftParams{Category: string(category), SiteID: site})
	if err != nil || len(drift) == 0 {
		return false, err
	}
	if n, err := q.SetCapPaused(ctx, tx, SetCapPausedParams{Paused: true, Category: string(category), SiteID: site}); err != nil || n == 0 {
		return false, err
	}
	if err := recordDrift(ctx, tx, reconcileActor, "cap_policy.pause", category, site, drift, ""); err != nil {
		return false, err
	}
	return true, alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
		Kind: capDriftSignal, Key: scopeName(site) + ":" + string(category), Severity: "critical",
		Fields: map[string]string{"scope": scopeName(site), "category": string(category), "counters": fmt.Sprint(len(drift))},
	})
}

// RepairCaps is the owner's repair of site and category: under the lock of its policy, which every proposal there
// waits for, and of its counters of the current UTC day and ISO week, each counter is set to what its money actions
// add up to, the policy is resumed and its alert resolved. It is recorded with every counter it changed, and a scope
// that did not drift is resumed alike. A scope without a policy is ErrNoCapPolicy.
func RepairCaps(ctx context.Context, tx pg.Tx, actor audit.Actor, category Category, site *uuid.UUID, reason string) (CapPolicy, error) {
	q := New()
	policies, err := q.LockCapPolicyForUpdate(ctx, tx, LockCapPolicyForUpdateParams{Category: string(category), SiteID: site})
	if err != nil {
		return CapPolicy{}, fmt.Errorf("lock the cap policy: %w", err)
	}
	if len(policies) == 0 {
		return CapPolicy{}, ErrNoCapPolicy
	}
	w, err := q.CapWindows(ctx, tx)
	if err != nil {
		return CapPolicy{}, fmt.Errorf("read the cap windows: %w", err)
	}
	if err := q.LockScopeCounters(ctx, tx, LockScopeCountersParams{
		Category: string(category), SiteID: site, DayStart: w.DayStart, WeekStart: w.WeekStart,
	}); err != nil {
		return CapPolicy{}, fmt.Errorf("lock the cap counters: %w", err)
	}
	drift, err := q.CapDrift(ctx, tx, CapDriftParams{Category: string(category), SiteID: site, DayStart: w.DayStart, WeekStart: w.WeekStart})
	if err != nil {
		return CapPolicy{}, fmt.Errorf("add up the cap counters: %w", err)
	}
	for _, d := range drift {
		if err := q.SetCapCounter(ctx, tx, SetCapCounterParams{
			Category: d.Category, WindowKind: d.WindowKind, WindowStart: d.WindowStart, ObjectKind: d.ObjectKind,
			ObjectID: d.ObjectID, SiteID: d.SiteID, Reserved: d.WantReserved, Committed: d.WantCommitted, Count: d.WantCount,
		}); err != nil {
			return CapPolicy{}, fmt.Errorf("repair the cap counters: %w", err)
		}
	}
	resumed, err := q.SetCapPaused(ctx, tx, SetCapPausedParams{Paused: false, Category: string(category), SiteID: site})
	if err != nil {
		return CapPolicy{}, fmt.Errorf("resume the cap policy: %w", err)
	}
	p, err := policyOf(policies[0])
	if err != nil {
		return CapPolicy{}, err
	}
	p.Paused = nil
	if err := recordDrift(ctx, tx, actor, "cap_counters.repair", category, site, drift, reason); err != nil || resumed == 0 {
		return p, err
	}
	return p, alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
		Kind: capDriftSignal, Key: scopeName(site) + ":" + string(category), Severity: "info", Resolved: true,
	})
}

// recordDrift records action on the policy of site and category with every counter that drifted, as it was and as its
// money actions add up, in as many entries as the audit log's 8 KiB detail needs: each is numbered part of parts, and
// a scope with nothing drifted is one entry with no counters.
func recordDrift(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, category Category, site *uuid.UUID, drift []CapDriftRow, reason string) error {
	parts, err := driftParts(drift)
	if err != nil {
		return err
	}
	for i, counters := range parts {
		if err := audit.Record(ctx, tx, audit.Event{
			Actor: actor, Action: action, Reason: reason,
			Target: audit.Target{Module: "operations", Kind: "cap_policy", ID: scopeName(site) + ":" + string(category)},
			Detail: map[string]any{"counters": counters, "part": i + 1, "parts": len(parts)},
		}); err != nil {
			return err
		}
	}
	return nil
}

// driftBudget is how much of the audit log's 8192-byte detail the counters of one entry may take, leaving the rest to
// the envelope ({"counters": [...], "part": n, "parts": n}).
const driftBudget = 8192 - 64

// driftParts packs the counters into parts that fit driftBudget each. A row's size as the database prints the detail
// is at most its compact JSON plus a space after each colon and comma (jsonb's text form; Go escapes at least as much
// as it does), so the sum of those bounds is a bound. An object ID is cut to the 128 characters a money action allows,
// so a row written by hand cannot outgrow a part on its own.
func driftParts(drift []CapDriftRow) ([][]map[string]any, error) {
	parts := [][]map[string]any{{}}
	used := 0
	for _, d := range drift {
		id := []rune(d.ObjectID)
		row := map[string]any{
			"window_kind": d.WindowKind, "window_start": d.WindowStart, "object_kind": d.ObjectKind,
			"object_id": string(id[:min(len(id), 128)]),
			"before":    map[string]any{"reserved": d.Reserved, "committed": d.Committed, "count": d.Count},
			"after":     map[string]any{"reserved": d.WantReserved, "committed": d.WantCommitted, "count": d.WantCount},
		}
		encoded, err := jsonx.MarshalCanonical(row)
		if err != nil {
			return nil, fmt.Errorf("encode a drifted counter: %w", err)
		}
		size := len(encoded) + bytes.Count(encoded, []byte(":")) + bytes.Count(encoded, []byte(",")) + 2 // ", " between rows
		if last := len(parts) - 1; used+size > driftBudget && len(parts[last]) > 0 {
			parts, used = append(parts, nil), 0
		}
		parts[len(parts)-1] = append(parts[len(parts)-1], row)
		used += size
	}
	return parts, nil
}

// scopeName is how a scope is named in the audit and alerts: the site's ID, or platform.
func scopeName(site *uuid.UUID) string {
	if site == nil {
		return "platform"
	}
	return site.String()
}

func sameSite(a, b *uuid.UUID) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// reconcileArgs is one run of the reconciliation.
type reconcileArgs struct{}

// Kind implements river.JobArgs.
func (reconcileArgs) Kind() string { return "operations.cap_reconcile" }

// InsertOpts implements river.JobArgsWithInsertOpts: one run at a time, on the money queue, as a late one leaves a
// drift unpaused for longer.
func (reconcileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueMoney, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}}}
}

// RegisterReconcile adds the reconciliation to queue, before it starts: every minute, on whichever node leads the
// queue, ReconcileCaps. scope is the platform's, which only cmd/ builds.
func RegisterReconcile(db *pg.DB, scope pg.Scope, queue *pg.Queue, logger *slog.Logger) error {
	// A failed run is logged and not retried: the next one comes within the minute.
	work := func(ctx context.Context, _ *river.Job[reconcileArgs]) error {
		if _, err := ReconcileCaps(ctx, db, scope); err != nil {
			logger.Error("operations: the reconciliation of the cap counters stopped", "err", err)
		}
		return nil
	}
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(work)); err != nil {
		return fmt.Errorf("operations: %w", err)
	}
	queue.Every(time.Minute, func() river.JobArgs { return reconcileArgs{} })
	return nil
}
