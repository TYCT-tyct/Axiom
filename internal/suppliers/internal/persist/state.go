package persist

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/reconcile"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// format is the number of the shape the state is stored in. A change to State that an old stored state would read
// differently raises it, and Load then reads the old number too.
const format = 1

// never is the next_due of a supplier no round is due for: one the engine does not poll.
var never = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// listings keeps resumable reads outside the strictly decoded state for rolling releases.
// Version invalidates positions when an older node commits without updating this column.
type listings struct {
	Version int64           `json:"version"`
	Gen     int64           `json:"gen"`
	Offer   adapter.Listing `json:"offer"`
	Keys    adapter.Listing `json:"keys"`
}

func encodeListings(gen int64, st syncer.State) ([]byte, error) {
	if len(st.OfferAt) == 0 && len(st.KeysAt) == 0 {
		return nil, nil
	}
	return jsonx.MarshalCanonical(listings{Version: st.Version, Gen: gen, Offer: st.OfferAt, Keys: st.KeysAt})
}

func decodeListings(row []byte, gen, version int64) (offer, keys adapter.Listing) {
	if len(row) == 0 {
		return nil, nil
	}
	var l listings
	if jsonx.Unmarshal(row, &l) != nil || l.Gen != gen || l.Version != version {
		return nil, nil
	}
	if len(l.Offer) == 0 {
		l.Offer = nil
	}
	if len(l.Keys) == 0 {
		l.Keys = nil
	}
	return l.Offer, l.Keys
}

type stateStore struct{ s *Session }

// Load implements syncer.Store. A supplier synced for the first time has no state yet, which is the zero State.
func (st stateStore) Load(ctx context.Context) (syncer.State, error) {
	var out syncer.State
	err := st.s.node.cfg.DB.ReadTx(ctx, st.s.node.cfg.Scope, func(tx pg.Tx) error {
		row, err := store.New().GetSyncState(ctx, tx, st.s.Supplier.ID)
		if err != nil {
			return err
		}
		out, err = decode(row.Version, row.Format, row.State)
		if err != nil {
			return err
		}
		out.OfferAt, out.KeysAt = decodeListings(row.Listing, st.s.Supplier.ConfigGen, row.Version)
		return nil
	})
	if err != nil {
		return syncer.State{}, fmt.Errorf("persist: load the state of supplier %s: %w", st.s.Supplier.ID, err)
	}
	return out, nil
}

// decode reads a stored state as this program keeps it. Its version is the row's, which the state's own member is not
// read for: the row is what a Commit names.
func decode(version int64, storedFormat int16, raw []byte) (syncer.State, error) {
	if storedFormat != format {
		return syncer.State{}, fmt.Errorf("the state is stored in format %d, which this program does not read", storedFormat)
	}
	var out syncer.State
	if err := jsonx.UnmarshalKnown(raw, &out); err != nil {
		return syncer.State{}, fmt.Errorf("decode the state: %w", err)
	}
	out.Version = version
	return out, nil
}

// Wake resumes polling in the transaction that replaces the login, fencing out the old lease holder. It keeps the
// charge position to avoid rescanning history, and LastOK so commits still project the stored listing (readsListing).
//
// It ensures and locks the state row before the caller locks the supplier, matching hold's lock order. Without the
// insert, an initial sync could create and stop the state between this read and the caller's supplier lock.
func Wake(ctx context.Context, tx pg.Tx, id uuid.UUID, now time.Time) error {
	q := store.New()
	if err := q.EnsureSyncState(ctx, tx, store.EnsureSyncStateParams{SupplierID: id, Now: now}); err != nil {
		return fmt.Errorf("persist: make the state of supplier %s: %w", id, err)
	}
	row, err := q.LockSyncState(ctx, tx, id)
	if errors.Is(err, pg.ErrNoRows) {
		// No supplier to wake: the caller's own read of it reports that, as it did before there was a state to wake.
		return nil
	}
	if err != nil {
		return fmt.Errorf("persist: lock the state of supplier %s: %w", id, err)
	}
	woken, err := decode(row.Version, row.Format, row.State)
	if err != nil {
		return fmt.Errorf("persist: read the state of supplier %s: %w", id, err)
	}
	woken.Version++
	woken.Status, woken.Reason = syncer.Normal, ""
	for i, t := range woken.Tasks {
		woken.Tasks[i] = syncer.TaskState{LastOK: t.LastOK}
	}
	encoded, err := jsonx.MarshalCanonical(woken)
	if err != nil {
		return fmt.Errorf("persist: encode the state of supplier %s: %w", id, err)
	}
	err = q.ResetSyncState(ctx, tx, store.ResetSyncStateParams{
		SupplierID: id, Version: woken.Version, State: encoded, NextDue: now,
	})
	if err != nil {
		return fmt.Errorf("persist: store the state of supplier %s: %w", id, err)
	}
	return nil
}

// Commit implements syncer.Store: the state, the events and plans the step produced, the charge records it read, and the
// projection of the state onto the supplier and its listing, in one transaction that holds only if the lease is still the
// session's, the supplier's credentials are still the generation it was taken in, and the stored version is expect.
// Otherwise nothing of the step is written, and the error is syncer.ErrFenced.
func (st stateStore) Commit(ctx context.Context, expect int64, c syncer.Commit) error {
	var snap *catalog.Snapshot
	if readsListing(c.State) {
		// Read before the transaction opens, so that a step never waits for a second connection while it holds a lease.
		var err error
		if snap, err = st.snapshot(ctx); err != nil {
			return err
		}
	}
	encoded, err := jsonx.MarshalCanonical(c.State)
	if err != nil {
		return fmt.Errorf("persist: encode the state: %w", err)
	}
	cfg, id := st.s.node.cfg, st.s.Supplier.ID
	listing, err := encodeListings(st.s.Supplier.ConfigGen, c.State)
	if err != nil {
		return fmt.Errorf("persist: encode the listings of supplier %s: %w", id, err)
	}
	now := cfg.Now()
	until := now.Add(cfg.LeaseTTL)
	err = cfg.DB.Tx(ctx, cfg.Scope, func(tx pg.Tx) error {
		cur, err := st.s.hold(ctx, tx)
		if err != nil {
			return err
		}
		changed, err := store.New().CommitSyncState(ctx, tx, store.CommitSyncStateParams{
			SupplierID: id, Expect: expect, Version: c.State.Version, Format: format,
			State: encoded, Listing: listing, NextDue: nextDue(c.State), LeaseUntil: &until,
		})
		if err != nil {
			return err
		}
		if changed == 0 {
			return syncer.ErrFenced
		}
		return writeStep(ctx, tx, id, c, cur, snap, now)
	})
	if err != nil {
		return fmt.Errorf("persist: commit version %d of supplier %s: %w", c.State.Version, id, err)
	}
	return nil
}

// readsListing reports whether the state has a listing to project. Until a round has read one — the catalog task has
// succeeded at least once — there is nothing to write: the tables hold what a person, or an earlier round, put there,
// and the groups a state does not name are not gone, they were never looked at.
func readsListing(st syncer.State) bool { return !st.Tasks[syncer.TaskCatalog].LastOK.IsZero() }

// snapshot is the catalog as the projection maps upstream names by. A snapshot the catalog has not loaded yet, or one
// older than its staleness limit, is loaded here, and the rest of a round uses the copy in memory.
func (st stateStore) snapshot(ctx context.Context) (*catalog.Snapshot, error) {
	cat := st.s.node.cfg.Catalog
	if snap, err := cat.Current(); err == nil {
		return snap, nil
	}
	if err := cat.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("persist: read the catalog: %w", err)
	}
	snap, err := cat.Current()
	if err != nil {
		return nil, fmt.Errorf("persist: read the catalog: %w", err)
	}
	return snap, nil
}

// writeStep writes what a step produced next to its state.
func writeStep(ctx context.Context, tx pg.Tx, id uuid.UUID, c syncer.Commit, cur store.LockSupplierRow, snap *catalog.Snapshot, now time.Time) error {
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
	if err := projectSupplier(ctx, tx, id, c.State, cur); err != nil {
		return err
	}
	if snap == nil {
		// No listing was read, so there is none to project: the supplier's tables are left as a person or an earlier round
		// left them.
		return nil
	}
	return projectListing(ctx, tx, id, c.State, cur, snap, now)
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

// projectSupplier writes onto the supplier what routing and the back office read of the engine's state: the balance last
// seen and the status. A supplier a person paused stays paused, a removal is not undone, and an engine that is polling
// again brings back only a supplier that was still connecting or running. cur is the supplier as the step locked it.
func projectSupplier(ctx context.Context, tx pg.Tx, id uuid.UUID, st syncer.State, cur store.LockSupplierRow) error {
	q := store.New()
	if !st.BalanceAt.IsZero() {
		at := st.BalanceAt
		if err := q.SetSupplierBalance(ctx, tx, store.SetSupplierBalanceParams{ID: id, Balance: string(st.Balance), ObservedAt: &at}); err != nil {
			return fmt.Errorf("write the balance: %w", err)
		}
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
