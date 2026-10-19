package persist_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/syncer"
)

var errLetGo = errors.New("let the held-up write go on")

// heldUp starts write while stall holds it up on a lock, tries intrude meanwhile for as long as it takes to be sure a
// statement that has to wait is waiting, and then lets write go on. It reports whether intrude got through, and
// write's error.
func (c *cluster) heldUp(stall func(ctx context.Context, tx pg.Tx) error, write func() error, intrude func(ctx context.Context) error) (intruded bool, err error) {
	c.t.Helper()
	watch, done := c.open(), make(chan error, 1)
	held := c.open().Tx(c.t.Context(), platform, func(tx pg.Tx) error {
		if err := stall(c.t.Context(), tx); err != nil {
			return err
		}
		go func() { done <- write() }()
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			var waiting int
			err := watch.ReadTx(c.t.Context(), platform, func(tx pg.Tx) error {
				return tx.QueryRow(c.t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'").Scan(&waiting)
			})
			if err != nil {
				return err
			}
			if waiting > 0 {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("the write never waited for the lock")
			}
		}
		ctx, cancel := context.WithTimeout(c.t.Context(), 500*time.Millisecond)
		defer cancel()
		intruded = intrude(ctx) == nil
		return errLetGo
	})
	if !errors.Is(held, errLetGo) {
		c.t.Fatal(held)
	}
	return intruded, <-done
}

// TestAHeldUpWriteIsNotLetInAfterTheLeaseOrTheCredentialsMovedOn: a write that is already waiting on a row when another
// node takes the lease over, or when the supplier's credentials are replaced, either is ordered before that change and
// lands, or is void; it never lands after the change, which is what a write that looked at the lease and the generation
// without holding them would do.
func TestAHeldUpWriteIsNotLetInAfterTheLeaseOrTheCredentialsMovedOn(t *testing.T) {
	t.Parallel()
	holdLogin := func(c *cluster) func(context.Context, pg.Tx) error {
		return func(ctx context.Context, tx pg.Tx) error {
			_, err := tx.Exec(ctx, "SELECT 1 FROM suppliers.supplier_logins WHERE supplier_id = $1 FOR UPDATE", c.supplier)
			return err
		}
	}
	// The same event a commit writes, uncommitted: the commit waits for it at its first event.
	holdEvent := func(c *cluster) func(context.Context, pg.Tx) error {
		return func(ctx context.Context, tx pg.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO suppliers.sync_events (supplier_id, version, seq, kind, subject, old_value, new_value, why) VALUES ($1, 1, 0, 'x', '', '', '', '')", c.supplier)
			return err
		}
	}
	swap := func(s *persist.Session) func() error {
		return func() error { return s.Logins().Swap(context.Background(), 1, []byte(`{"refresh":"late"}`)) }
	}
	commit := func(s *persist.Session) func() error {
		return func() error {
			return s.Store().Commit(context.Background(), 0, syncer.Commit{State: stateAt(1), Events: []syncer.Event{{Kind: syncer.GroupAdded}}})
		}
	}
	takeOver := func(c *cluster) func(context.Context) error {
		b := c.node("b")
		return func(ctx context.Context) error {
			c.clock.advance(ttl + time.Second)
			_, err := b.Acquire(ctx, c.supplier)
			return err
		}
	}
	replaceCredentials := func(c *cluster) func(context.Context) error {
		db := c.open()
		return func(ctx context.Context) error {
			return db.Tx(ctx, platform, func(tx pg.Tx) error {
				_, err := tx.Exec(ctx, "UPDATE suppliers.suppliers SET config_gen = config_gen + 1 WHERE id = $1", c.supplier)
				return err
			})
		}
	}
	for name, k := range map[string]struct {
		stall    func(*cluster) func(context.Context, pg.Tx) error
		write    func(*persist.Session) func() error
		intrude  func(*cluster) func(context.Context) error
		wantVoid error
	}{
		"login swap, lease taken over":      {holdLogin, swap, takeOver, adapter.ErrStaleLogin},
		"login swap, credentials replaced":  {holdLogin, swap, replaceCredentials, adapter.ErrStaleLogin},
		"step commit, lease taken over":     {holdEvent, commit, takeOver, syncer.ErrFenced},
		"step commit, credentials replaced": {holdEvent, commit, replaceCredentials, syncer.ErrFenced},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newCluster(t)
			s := c.acquire(c.node("a"))
			intruded, err := c.heldUp(k.stall(c), k.write(s), k.intrude(c))
			switch {
			case !intruded && err != nil:
				t.Errorf("the write that nothing overtook failed: %v", err)
			case intruded && !errors.Is(err, k.wantVoid):
				t.Errorf("the change got through while the write waited, and the write ended with %v, want %v", err, k.wantVoid)
			}
		})
	}
}

// TestAStepThatBeganBeforeTheCredentialsWereReplacedIsVoid: the credentials' generation is read when the lease is taken
// and a step names it, so a round that began before they were replaced leaves nothing, not even when no one took the
// lease: its results, a "needs attention" from the old credentials' refusal among them, would otherwise settle on a
// supplier that has been put right.
func TestAStepThatBeganBeforeTheCredentialsWereReplacedIsVoid(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	s := c.acquire(c.node("a"))
	err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "UPDATE suppliers.suppliers SET config_gen = config_gen + 1 WHERE id = $1", c.supplier)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	step := syncer.Commit{
		State:  syncer.State{Version: 1, Status: syncer.NeedsAttention, Reason: "rejected"},
		Events: []syncer.Event{{Kind: syncer.GroupAdded, Subject: "1"}},
		Plans:  []syncer.Plan{{Endpoint: "1/m", Fingerprint: "f", Schedule: `{"v":1}`, Basis: "published"}},
	}
	if err := s.Store().Commit(t.Context(), 0, step); !errors.Is(err, syncer.ErrFenced) {
		t.Fatalf("a step of the old generation: %v, want ErrFenced", err)
	}
	var status string
	err = c.open().ReadTx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT status FROM suppliers.suppliers WHERE id = $1", c.supplier).Scan(&status)
	})
	if err != nil || status != "connecting" || c.count("sync_events")+c.count("sync_plans") != 0 {
		t.Errorf("the void step left status %q, %d events, %d plans (%v)", status, c.count("sync_events"), c.count("sync_plans"), err)
	}
	if st, err := s.Store().Load(t.Context()); err != nil || st.Version != 0 {
		t.Errorf("state after the void step: %+v, %v", st, err)
	}
}

// TestAStateSurvivesBeingStored: every member of the state, in every shape it takes, comes back from the database as it
// was committed; a stored state with a member this program does not know, or in a format it does not read, is refused
// rather than read as something else; and a supplier never synced has the zero state.
func TestAStateSurvivesBeingStored(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	store := c.acquire(c.node("a")).Store()
	if st, err := store.Load(t.Context()); err != nil || st.Version != 0 || st.Status != syncer.Normal {
		t.Errorf("the state of a supplier never synced: %+v, %v", st, err)
	}
	at := time.Date(2026, 10, 15, 12, 0, 0, 123456789, time.UTC)
	want := syncer.State{
		Version: 7, ConfigGen: 2, Status: syncer.NeedsAttention, Reason: "rejected",
		Tasks:   [4]syncer.TaskState{{Due: at, BackoffUntil: at.Add(time.Minute), Failures: 3, LastOK: at, Halted: true}},
		Balance: "12.5", BalanceAt: at, Funding: adapter.FundingWallet, LowNotified: true,
		Groups: []syncer.GroupSnap{{Ref: "1", Name: "g", Ratio: "1.5", Facts: `{"a":1}`, Models: []syncer.ModelSnap{
			{Name: "m", Formats: []int{0, 2}, Without: protocol.Images | protocol.Tools, Plan: "ab12"},
		}}},
		Keys:      []adapter.KeyState{{Ref: "7", GroupRef: "1", Status: adapter.KeyQuotaExhausted, ResetAt: at}},
		Plans:     map[string]string{"1/m": "ab12"},
		Confirmed: map[string]string{"m": `{"v":1}`},
		Scan:      adapter.ScanState([]byte{0, 255, 'x'}), ScanOpen: true,
		Removal: syncer.RemovalStep(3), GraceUntil: at, RescanFrom: at, RemovalAt: at, Through: at,
	}
	if err := store.Commit(t.Context(), 0, syncer.Commit{State: want}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("the state came back different (-committed +loaded):\n%s", diff)
	}

	for name, update := range map[string]string{
		"a member this program does not know": `UPDATE suppliers.sync_states SET state = state || '{"Unknown": true}'`,
		"a format this program does not read": `UPDATE suppliers.sync_states SET format = 2`,
	} {
		err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
			_, err := tx.Exec(t.Context(), update)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(t.Context()); err == nil {
			t.Errorf("a state with %s was read", name)
		}
		err = c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE suppliers.sync_states SET format = 1, state = state - 'Unknown'`)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Load(t.Context()); err != nil {
		t.Errorf("the state after it was put right: %v", err)
	}
}
