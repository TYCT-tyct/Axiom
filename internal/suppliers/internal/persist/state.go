package persist

import (
	"context"
	"fmt"
	"slices"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/reconcile"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// format is the number of the shape the state is stored in. A change to State that an old stored state would read
// differently raises it, and Load then reads the old number too.
const format = 1

// never is the next_due of a supplier no round is due for: one the engine does not poll.
var never = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

type stateStore struct{ s *Session }

// Load implements syncer.Store. A supplier synced for the first time has no state yet, which is the zero State.
func (st stateStore) Load(ctx context.Context) (syncer.State, error) {
	var out syncer.State
	err := st.s.node.cfg.DB.ReadTx(ctx, st.s.node.cfg.Scope, func(tx pg.Tx) error {
		row, err := store.New().GetSyncState(ctx, tx, st.s.Supplier.ID)
		if err != nil {
			return err
		}
		if row.Format != format {
			return fmt.Errorf("the state is stored in format %d, which this program does not read", row.Format)
		}
		if err := jsonx.UnmarshalKnown(row.State, &out); err != nil {
			return fmt.Errorf("decode the state: %w", err)
		}
		out.Version = row.Version
		return nil
	})
	if err != nil {
		return syncer.State{}, fmt.Errorf("persist: load the state of supplier %s: %w", st.s.Supplier.ID, err)
	}
	return out, nil
}

// Commit implements syncer.Store: the state, the events and plans the step produced, the charge records it read, and the
// projection of the state onto the supplier, in one transaction that holds only if the lease is still the session's and
// the stored version is expect. Otherwise nothing of the step is written, and the error is syncer.ErrFenced.
func (st stateStore) Commit(ctx context.Context, expect int64, c syncer.Commit) error {
	encoded, err := jsonx.MarshalCanonical(c.State)
	if err != nil {
		return fmt.Errorf("persist: encode the state: %w", err)
	}
	cfg, id := st.s.node.cfg, st.s.Supplier.ID
	now := cfg.Now()
	until := now.Add(cfg.LeaseTTL)
	err = cfg.DB.Tx(ctx, cfg.Scope, func(tx pg.Tx) error {
		changed, err := store.New().CommitSyncState(ctx, tx, store.CommitSyncStateParams{
			SupplierID: id, Expect: expect, LeaseFence: st.s.fence, Version: c.State.Version, Format: format,
			State: encoded, NextDue: nextDue(c.State), LeaseUntil: &until,
		})
		if err != nil {
			return err
		}
		if changed == 0 {
			return syncer.ErrFenced
		}
		return writeStep(ctx, tx, id, c)
	})
	if err != nil {
		return fmt.Errorf("persist: commit version %d of supplier %s: %w", c.State.Version, id, err)
	}
	return nil
}

// writeStep writes what a step produced next to its state.
func writeStep(ctx context.Context, tx pg.Tx, id uuid.UUID, c syncer.Commit) error {
	if len(c.Events) > 0 {
		rows := make([]store.SyncEventRow, len(c.Events))
		for i, e := range c.Events {
			rows[i] = store.SyncEventRow{Kind: e.Kind.String(), Subject: e.Subject, Old: e.Old, New: e.New, Why: e.Why}
		}
		if err := store.InsertSyncEvents(ctx, tx, id, c.State.Version, rows); err != nil {
			return fmt.Errorf("write the events: %w", err)
		}
	}
	if len(c.Plans) > 0 {
		rows := make([]store.SyncPlanRow, len(c.Plans))
		for i, p := range c.Plans {
			rows[i] = store.SyncPlanRow{Endpoint: p.Endpoint, Fingerprint: p.Fingerprint, Schedule: p.Schedule, Basis: p.Basis}
		}
		if err := store.InsertSyncPlans(ctx, tx, id, c.State.Version, rows); err != nil {
			return fmt.Errorf("write the cost plans: %w", err)
		}
	}
	if _, err := reconcile.Ingest(ctx, tx, id, c.Charges); err != nil {
		return fmt.Errorf("write the charge records: %w", err)
	}
	return project(ctx, tx, id, c.State)
}

// nextDue is when the supplier next needs a round: the earliest time one of its tasks may run, or never while the
// engine does not poll it (it needs a person, or is being removed) or every task has stopped.
func nextDue(st syncer.State) time.Time {
	if st.Status != syncer.Normal {
		return never
	}
	due := never
	for _, t := range st.Tasks {
		if at := latest(t.Due, t.BackoffUntil); !t.Halted && at.Before(due) {
			due = at
		}
	}
	return due.UTC()
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// project writes onto the supplier what routing and the back office read of the engine's state: the balance last seen
// and the status. A supplier a person paused stays paused, a removal is not undone, and an engine that is polling again
// brings back only a supplier that was still connecting or running.
func project(ctx context.Context, tx pg.Tx, id uuid.UUID, st syncer.State) error {
	q := store.New()
	if !st.BalanceAt.IsZero() {
		at := st.BalanceAt
		if err := q.SetSupplierBalance(ctx, tx, store.SetSupplierBalanceParams{ID: id, Balance: string(st.Balance), ObservedAt: &at}); err != nil {
			return fmt.Errorf("write the balance: %w", err)
		}
	}
	cur, err := q.LockSupplierStatus(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("read the status: %w", err)
	}
	status, reason := projectStatus(cur.Status, st)
	if status == cur.Status {
		return nil
	}
	if err := q.SetSupplierStatus(ctx, tx, store.SetSupplierStatusParams{ID: id, Status: status, StatusReason: reason}); err != nil {
		return fmt.Errorf("write the status: %w", err)
	}
	return nil
}

// projectStatus is the status the supplier has once the engine's state is written, given the one it has.
func projectStatus(current string, st syncer.State) (status string, reason *string) {
	switch st.Status {
	case syncer.Normal:
		if !slices.Contains([]string{"connecting", "active", "low_balance"}, current) {
			return current, nil
		}
		if st.LowNotified {
			return "low_balance", nil
		}
		return "active", nil
	case syncer.NeedsAttention:
		if current == "removing" || current == "removed" {
			return current, nil
		}
		if st.Reason == "" {
			return "needs_attention", nil
		}
		return "needs_attention", &st.Reason
	case syncer.Removing:
		if current == "removed" {
			return current, nil
		}
		return "removing", nil
	case syncer.Removed:
		return "removed", nil
	}
	panic("persist: a status the engine does not have")
}
