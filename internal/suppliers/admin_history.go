package suppliers

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/reconcile"
	"axiom/internal/suppliers/internal/store"
)

// What a supplier's sync and reconciliation found, read back for a person (docs/domains/suppliers.md「同步记录」「对账」),
// and a person's asking for a sync now.

// SyncTask is how one task of the sync stands. Due is zero when the task is due now.
type SyncTask = persist.TaskView

// SyncTasks reads how each task of the supplier's sync stands; a supplier never synced has none.
func (a *Admin) SyncTasks(ctx context.Context, tx pg.Tx, supplier uuid.UUID) ([]SyncTask, error) {
	return persist.Tasks(ctx, tx, supplier, a.now())
}

// RequestSync makes every task of a supplier the sync runs due now, without clearing a halted task or a backoff: the
// next sweep queues it. A supplier the sync does not run (paused, needing a person, removed) is ErrStatusLocked.
func (a *Admin) RequestSync(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier uuid.UUID, reason string) ([]SyncTask, error) {
	// The state's row before the supplier's, the order the sync's step takes them in (persist.Wake).
	tasks, err := persist.RequestSync(ctx, tx, supplier, a.now())
	if err != nil {
		return nil, fmt.Errorf("request the sync: %w", err)
	}
	row, err := a.locked(ctx, tx, supplier, Connecting, Active, LowBalance)
	if err != nil {
		return nil, err
	}
	if err := a.record(ctx, tx, actor, "supplier.sync_request", supplier, reason, map[string]any{"status": row.Status}); err != nil {
		return nil, err
	}
	return tasks, nil
}

// SyncEvent is one difference a sync recorded, in Axiom's terms. Group is the group it is about while Axiom has it.
type SyncEvent struct {
	Kind, Subject, Old, New, Why string
	Group                        *uuid.UUID
}

// SyncRun is a commit of the sync that recorded differences: the version of the state it committed and when.
type SyncRun struct {
	Version int64
	At      time.Time
	Events  []SyncEvent
}

// SyncRuns lists a page of a supplier's runs that changed something, newest first; before is the version the page
// before ended at, 0 for the first page. more tells whether runs remain beyond it.
func (a *Admin) SyncRuns(ctx context.Context, tx pg.Tx, supplier uuid.UUID, before int64, pageSize int) (runs []SyncRun, more bool, err error) {
	if _, err := a.view(ctx, tx, supplier, PlatformOwned); err != nil {
		return nil, false, err
	}
	q := store.New()
	params := store.SyncRunsPageParams{SupplierID: supplier, PageSize: int32(pageSize + 1)} // #nosec G115 -- a page is at most 200
	if before > 0 {
		params.Before = &before
	}
	rows, err := q.SyncRunsPage(ctx, tx, params)
	if err != nil {
		return nil, false, fmt.Errorf("list the sync's runs: %w", err)
	}
	if more = len(rows) > pageSize; more {
		rows = rows[:pageSize]
	}
	versions := make([]int64, len(rows))
	runs = make([]SyncRun, len(rows))
	for i, r := range rows {
		versions[i] = r.Version
		runs[i] = SyncRun{Version: r.Version, At: r.At, Events: []SyncEvent{}}
	}
	events, err := q.SyncEventsOf(ctx, tx, store.SyncEventsOfParams{SupplierID: supplier, Versions: versions})
	if err != nil {
		return nil, false, fmt.Errorf("read the sync's events: %w", err)
	}
	for _, e := range events {
		i, _ := slices.BinarySearchFunc(runs, e.Version, func(r SyncRun, v int64) int { return compareDesc(r.Version, v) })
		runs[i].Events = append(runs[i].Events, SyncEvent{Kind: e.Kind, Subject: e.Subject, Old: e.OldValue, New: e.NewValue, Why: e.Why, Group: e.GroupID})
	}
	return runs, more, nil
}

// compareDesc orders versions newest first.
func compareDesc(a, b int64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}

// MaxReconciliationDays is the most days one reading of a reconciliation covers.
const MaxReconciliationDays = 31

// Reconciliation is a supplier's reconciliation by UTC day, newest first, in its native unit, with the unit value in
// force to show it as money (empty until confirmed). Days is empty for a supplier with no charge records at all: its
// adapter does not read any.
type Reconciliation struct {
	Supplier           uuid.UUID
	NativeUnit         string
	UnitValue          string
	SettlementCurrency string
	Days               []ReconciliationDay
}

// ReconciliationDay is one UTC day. DifferenceBP is (charged − estimated) / estimated in basis points, rounded half away
// from zero; nil when nothing was estimated.
type ReconciliationDay struct {
	Date                           string
	EstimatedNative, ChargedNative string
	DifferenceBP                   *int64
	Charges, Matched, Held         int64
	ChargedAbove, ChargedBelow     int64
	UnmatchedAttempts              int64
}

// Reconciliation reads the last days days of a platform supplier's reconciliation, today included.
func (a *Admin) Reconciliation(ctx context.Context, tx pg.Tx, supplier uuid.UUID, days int) (Reconciliation, error) {
	sup, err := a.view(ctx, tx, supplier, PlatformOwned)
	if err != nil {
		return Reconciliation{}, err
	}
	out := Reconciliation{
		Supplier: supplier, NativeUnit: sup.NativeUnit, UnitValue: sup.UnitValue, SettlementCurrency: sup.SettlementCurrency,
		Days: []ReconciliationDay{},
	}
	q := store.New()
	has, err := q.HasChargeRecords(ctx, tx, supplier)
	if err != nil {
		return Reconciliation{}, fmt.Errorf("read the charge records: %w", err)
	}
	if !has {
		return out, nil
	}
	now := a.now().UTC()
	tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	rows, err := q.ReconciliationDays(ctx, tx, store.ReconciliationDaysParams{
		SupplierID: supplier, FromAt: tomorrow.AddDate(0, 0, -days), LastDay: tomorrow.AddDate(0, 0, -1), ToAt: tomorrow,
		GraceBefore: now.Add(-reconcile.DefaultConfig().Grace),
	})
	if err != nil {
		return Reconciliation{}, fmt.Errorf("read the reconciliation: %w", err)
	}
	for _, r := range rows {
		day := ReconciliationDay{
			Date: r.Day, EstimatedNative: r.EstimatedNative, ChargedNative: r.ChargedNative, Charges: r.Charges,
			Matched: r.Matched, Held: r.Held, ChargedAbove: r.ChargedAbove, ChargedBelow: r.ChargedBelow,
			UnmatchedAttempts: r.UnmatchedAttempts,
		}
		bp, estimated, err := differenceBP(r.EstimatedNative, r.ChargedNative)
		if err != nil {
			return Reconciliation{}, fmt.Errorf("the reconciliation of %s: %w", r.Day, err)
		}
		if estimated {
			day.DifferenceBP = &bp
		}
		out.Days = append(out.Days, day)
	}
	return out, nil
}

// differenceBP is (charged − estimated) / estimated × 10000, rounded half away from zero, exactly; ok is false when
// nothing was estimated, which has no difference. The amounts are the columns' numeric text.
func differenceBP(estimated, charged string) (bp int64, ok bool, err error) {
	e, isNumber := new(big.Rat).SetString(estimated)
	if !isNumber {
		return 0, false, fmt.Errorf("the estimate %q is not a number", estimated)
	}
	c, isNumber := new(big.Rat).SetString(charged)
	if !isNumber {
		return 0, false, fmt.Errorf("the charge %q is not a number", charged)
	}
	if e.Sign() == 0 {
		return 0, false, nil
	}
	ratio := new(big.Rat).Quo(new(big.Rat).Mul(new(big.Rat).Sub(c, e), big.NewRat(10000, 1)), e)
	// Half away from zero: add a half of the ratio's sign, then cut toward zero.
	half := big.NewRat(int64(ratio.Sign()), 2)
	q := new(big.Int).Quo(new(big.Rat).Add(ratio, half).Num(), new(big.Rat).Add(ratio, half).Denom())
	if !q.IsInt64() {
		return 0, false, fmt.Errorf("the difference %s basis points is out of range", q)
	}
	return q.Int64(), true, nil
}
