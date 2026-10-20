package suppliers

import (
	"context"
	"errors"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// noCatalog is the catalog a step here never asks: a round reads a listing only when the catalog task succeeded before,
// and no state of this test has read one.
type noCatalog struct{}

func (noCatalog) Current() (*catalog.Snapshot, error) {
	return nil, errors.New("no catalog in this test")
}
func (noCatalog) Refresh(context.Context) error { return errors.New("no catalog in this test") }

// node is a sync node over the admin side's database, on its clock, under the holder's name.
func (a admins) node(holder string) *persist.Node {
	a.t.Helper()
	n, err := persist.New(persist.Config{
		DB: a.db, Scope: platformScope, Keys: a.keys, Holder: holder, LeaseTTL: time.Minute, Now: a.clock.now,
		Catalog: noCatalog{},
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return n
}

// TestReplaceLoginWakesAStoppedSync: a login the supplier rejects stops its sync for good — the state is "needs
// attention", which no round polls, and next_due is never — until a person replaces the login. The state then goes back
// into service in the same transaction, the failures of the old login gone and the position in the charge records kept,
// and the lease goes with it, so the node that held the old login writes neither a step nor a refreshed token. What the
// sweep looks for is true again: the supplier is connecting, due now, and no lease is in the way.
func TestReplaceLoginWakesAStoppedSync(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	at := a.clock.now()

	// The state the rejected login left: not polling, a task failed and one halted, a balance and a charge scan to keep,
	// a low balance already told, and what each task last read.
	stopped := syncer.State{
		Version: 1, Status: syncer.NeedsAttention, Reason: "the supplier rejected the account credentials",
		Tasks: [4]syncer.TaskState{
			syncer.TaskBalance: {Due: at, LastOK: at.Add(-time.Minute), Failures: 3, BackoffUntil: at.Add(time.Minute)},
			syncer.TaskCatalog: {Due: at},
			syncer.TaskKeys:    {Due: at, LastOK: at.Add(-5 * time.Minute), Halted: true},
			syncer.TaskCharges: {Due: at, LastOK: at, Failures: 2, BackoffUntil: at.Add(time.Minute)},
		},
		Balance: "12.5", BalanceAt: at, Funding: adapter.FundingWallet, LowNotified: true,
		Scan: adapter.ScanState(`{"watermark":1750000000}`), ScanOpen: true, Through: at,
	}
	held, err := a.node("the node with the old login").Acquire(t.Context(), id)
	if err != nil {
		t.Fatalf("taking the lease: %v", err)
	}
	if err := held.Store().Commit(t.Context(), 0, syncer.Commit{
		State:  stopped,
		Events: []syncer.Event{{Kind: syncer.NeedsAttentionEvent, Why: stopped.Reason}},
	}); err != nil {
		t.Fatal(err)
	}
	same(t, "the status the stop is shown as", "needs_attention",
		a.text(`SELECT status FROM suppliers.suppliers WHERE id = $1`, id))
	same(t, "the reason the stop is shown with", stopped.Reason,
		a.text(`SELECT status_reason FROM suppliers.suppliers WHERE id = $1`, id))
	same(t, "next due while the supplier is stopped", "true",
		a.text(`SELECT (next_due = '9999-01-01T00:00:00Z'::timestamptz)::text FROM suppliers.sync_states WHERE supplier_id = $1`, id))

	// The login is replaced: the state is put back into service in the same transaction, and the lease goes with it.
	s, err := inTx(a, func(ctx context.Context, tx pg.Tx) (Supplier, error) {
		return a.admin.ReplaceLogin(ctx, tx, a.actor, id, Credentials{AccessToken: "new-token"}, "the old login was revoked")
	})
	refuses(t, "replacing the login", err, nil)
	same(t, "the supplier after the replacement", []string{string(Connecting), "", "2"}, []string{
		string(s.Status), s.StatusReason, a.text(`SELECT version::text FROM suppliers.supplier_logins WHERE supplier_id = $1`, id),
	})
	same(t, "next due after the replacement", "true",
		a.text(`SELECT (next_due = $1)::text FROM suppliers.sync_states WHERE supplier_id = $2`, at, id))
	same(t, "the lease after the replacement", []string{"true", "2"}, []string{
		a.text(`SELECT (lease_holder IS NULL AND lease_until IS NULL)::text FROM suppliers.sync_states WHERE supplier_id = $1`, id),
		a.text(`SELECT lease_fence::text FROM suppliers.sync_states WHERE supplier_id = $1`, id),
	})

	var woken syncer.State
	raw := a.text(`SELECT state::text FROM suppliers.sync_states WHERE supplier_id = $1`, id)
	if err := jsonx.UnmarshalKnown([]byte(raw), &woken); err != nil {
		t.Fatalf("the state after the replacement: %v", err)
	}
	same(t, "the version the state is stored at", int64(2), woken.Version)
	same(t, "the status the state is polled in", syncer.Normal, woken.Status)
	same(t, "the reason the state holds", "", woken.Reason)
	// The tasks are due again with nothing counted against them, and what each last read stays: a listing that was read
	// is still projected by the commits that follow (the catalog's own last success is the persist tests' business).
	same(t, "the tasks the state holds", [4]syncer.TaskState{
		syncer.TaskBalance: {LastOK: at.Add(-time.Minute)},
		syncer.TaskKeys:    {LastOK: at.Add(-5 * time.Minute)},
		syncer.TaskCharges: {LastOK: at},
	}, woken.Tasks)
	same(t, "the balance the state keeps", stopped.Balance, woken.Balance)
	same(t, "the balance's time the state keeps", at, woken.BalanceAt)
	same(t, "the funding the state keeps", adapter.FundingWallet, woken.Funding)
	same(t, "a low balance already told", true, woken.LowNotified)
	same(t, "the position in the charge records", stopped.Scan, woken.Scan)
	same(t, "whether a pass was under way", true, woken.ScanOpen)
	same(t, "how far the records are read", at, woken.Through)

	// The node that held the old login writes nothing more: not a step, not a refreshed token.
	next := stopped
	next.Version = 2
	refuses(t, "a step of the node that held the old login",
		held.Store().Commit(t.Context(), 1, syncer.Commit{State: next}), syncer.ErrFenced)
	refuses(t, "a login the node that held the old login refreshed",
		held.Logins().Swap(t.Context(), 1, []byte(`{"token":"old"}`)), adapter.ErrStaleLogin)

	// The next round takes the lease and reads a state it polls from, with nothing counted against it.
	handover, err := a.node("the node with the new login").Acquire(t.Context(), id)
	if err != nil {
		t.Fatalf("the next round's take of the lease: %v", err)
	}
	st, err := handover.Store().Load(t.Context())
	if err != nil {
		t.Fatalf("reading the state the next round polls: %v", err)
	}
	same(t, "the status of the state the next round polls", syncer.Normal, st.Status)
	same(t, "the balance of the state the next round polls", stopped.Balance, st.Balance)
	same(t, "the failures the next round counts", 0, st.Tasks[syncer.TaskBalance].Failures)
}

// The first sync's uncommitted state is invisible to a plain SELECT FOR UPDATE. Replacing the login must wait for its
// insertion before resetting it, or the sync can leave a stopped state behind the newly connecting supplier.
func TestReplaceLoginWakesAStateTheFirstRoundMakes(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	at := a.clock.now()
	same(t, "the state rows before the first take of the lease", "0", a.text(`SELECT count(*)::text FROM suppliers.sync_states`))

	// The first round, with the login the supplier rejects: it has taken the lease — which made the state row, not yet
	// committed — and the supplier's row, and is about to write the state that stops the supplier.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	firstRound, err := pg.Open(t.Context(), a.data.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(firstRound.Close)
	pids, replaced := make(chan int32, 1), make(chan error, 1)
	err = firstRound.Tx(ctx, platformScope, func(tx pg.Tx) error {
		q := store.New()
		holder, lease := "the first round", at.Add(time.Minute)
		if _, err := q.AcquireSyncLease(ctx, tx, store.AcquireSyncLeaseParams{
			SupplierID: id, Now: at, Holder: &holder, LeaseUntil: &lease,
		}); err != nil {
			return err
		}
		if _, err := q.LockSupplier(ctx, tx, id); err != nil {
			return err
		}
		go func() {
			replaced <- a.db.Tx(ctx, platformScope, func(tx pg.Tx) error {
				var pid int32
				if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
					return err
				}
				pids <- pid
				_, err := a.admin.ReplaceLogin(ctx, tx, a.actor, id, Credentials{AccessToken: "new-token"}, "the old login was revoked")
				return err
			})
		}()
		var pid int32
		select {
		case pid = <-pids:
		case err := <-replaced:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
		// Release the first sync only once this replacement is waiting, independently of the server's speed.
		if err := a.waitForLockWait(ctx, pid); err != nil {
			return err
		}
		stopped := syncer.State{Version: 1, Status: syncer.NeedsAttention, Reason: "the supplier rejected the account credentials"}
		encoded, err := jsonx.MarshalCanonical(stopped)
		if err != nil {
			return err
		}
		if _, err := q.CommitSyncState(ctx, tx, store.CommitSyncStateParams{
			SupplierID: id, Expect: 0, Version: 1, Format: 1, State: encoded,
			NextDue: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), LeaseUntil: &lease,
		}); err != nil {
			return err
		}
		return q.SetSupplierStatus(ctx, tx, store.SetSupplierStatusParams{
			ID: id, Status: "needs_attention", StatusReason: &stopped.Reason,
		})
	})
	if err != nil {
		t.Fatalf("the first round: %v", err)
	}
	if err := <-replaced; err != nil {
		t.Fatalf("replacing the login: %v", err)
	}

	same(t, "the supplier after the replacement", []string{"connecting", "2"}, []string{
		a.text(`SELECT status FROM suppliers.suppliers WHERE id = $1`, id),
		a.text(`SELECT version::text FROM suppliers.supplier_logins WHERE supplier_id = $1`, id),
	})
	same(t, "next due after the replacement", "true",
		a.text(`SELECT (next_due = $1)::text FROM suppliers.sync_states WHERE supplier_id = $2`, at, id))
	same(t, "the lease after the replacement", []string{"true", "2"}, []string{
		a.text(`SELECT (lease_holder IS NULL AND lease_until IS NULL)::text FROM suppliers.sync_states WHERE supplier_id = $1`, id),
		a.text(`SELECT lease_fence::text FROM suppliers.sync_states WHERE supplier_id = $1`, id),
	})
	var woken syncer.State
	if err := jsonx.UnmarshalKnown([]byte(a.text(`SELECT state::text FROM suppliers.sync_states WHERE supplier_id = $1`, id)), &woken); err != nil {
		t.Fatalf("the state after the replacement: %v", err)
	}
	same(t, "the version the state is stored at", int64(2), woken.Version)
	same(t, "the status the state is polled in", syncer.Normal, woken.Status)
	same(t, "the reason the state holds", "", woken.Reason)
}

// waitForLockWait observes only this replacement's connection, so other transactions cannot release the first sync.
func (a admins) waitForLockWait(ctx context.Context, pid int32) error {
	a.t.Helper()
	super, err := pg.Open(ctx, a.data.Superuser)
	if err != nil {
		return err
	}
	defer super.Close()
	for {
		var waiting bool
		if err := super.ReadTx(ctx, pg.Scope{}, func(tx pg.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database() AND pid = $1 AND wait_event_type = 'Lock')`, pid).Scan(&waiting)
		}); err != nil {
			return err
		}
		if waiting {
			return nil
		}
	}
}
