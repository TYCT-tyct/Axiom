package persist

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// TaskView is how one task of a supplier's sync stands, as the admin shows it. Due is when it may run next: its own
// time or the end of its backoff, whichever is later; zero when it is due now.
type TaskView struct {
	Task     string
	Due      time.Time
	LastOK   time.Time
	Failures int
	Halted   bool
}

// Tasks reads how each task of a supplier stands; a supplier that has never synced has no state, and no tasks.
func Tasks(ctx context.Context, tx pg.Tx, id uuid.UUID, now time.Time) ([]TaskView, error) {
	row, err := store.New().GetSyncState(ctx, tx, id)
	if errors.Is(err, pg.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("persist: read the state of supplier %s: %w", id, err)
	}
	st, err := decode(row.Version, row.Format, row.State)
	if err != nil {
		return nil, fmt.Errorf("persist: the state of supplier %s: %w", id, err)
	}
	return taskViews(st, now), nil
}

func taskViews(st syncer.State, now time.Time) []TaskView {
	out := make([]TaskView, len(st.Tasks))
	for i, t := range st.Tasks {
		v := TaskView{Task: syncer.Task(i).String(), LastOK: t.LastOK, Failures: t.Failures, Halted: t.Halted}
		if due := latest(t.Due, t.BackoffUntil); due.After(now) {
			v.Due = due.UTC()
		}
		out[i] = v
	}
	return out
}

// RequestSync brings every task of a supplier forward to now, as RequestBalance does the balance's: a halted task stays
// halted and a backoff runs out first, and another node's lease is left alone. The sweep queues the round; the raised
// version fences out a round already in flight, which then starts again from this state. It answers the tasks as they
// now stand.
func RequestSync(ctx context.Context, tx pg.Tx, id uuid.UUID, now time.Time) ([]TaskView, error) {
	q := store.New()
	if err := q.EnsureSyncState(ctx, tx, store.EnsureSyncStateParams{SupplierID: id, Now: now}); err != nil {
		return nil, err
	}
	row, err := q.LockSyncState(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	st, err := decode(row.Version, row.Format, row.State)
	if err != nil {
		return nil, err
	}
	changed := false
	for i := range st.Tasks {
		if t := &st.Tasks[i]; st.Status == syncer.Normal && !t.Halted && t.Due.After(now) {
			t.Due, changed = now, true
		}
	}
	if !changed {
		return taskViews(st, now), nil
	}
	st.Version++
	raw, err := jsonx.MarshalCanonical(st)
	if err != nil {
		return nil, err
	}
	if err := q.RequestBalanceSync(ctx, tx, store.RequestBalanceSyncParams{
		SupplierID: id, Version: st.Version, State: raw, NextDue: nextDue(st),
	}); err != nil {
		return nil, err
	}
	return taskViews(st, now), nil
}

// KeyName is the name the sync gives a key it makes for a group, which a key made at a person's asking takes too: a
// key whose answer was lost is then adopted by the next listing instead of made twice.
func KeyName(supplier uuid.UUID, groupRef string) string { return keyName(supplier, groupRef) }
