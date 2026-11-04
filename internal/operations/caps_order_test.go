package operations_test

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

// TestCapLockOrder: every path takes the approval signals' lock last. An approval of a top-up waits for the counters a
// proposal holds before it takes that lock, so the proposal, which takes it after the counters, finishes first instead
// of both waiting on each other; and the sweep of reservations skips one whose operation a cancellation holds rather
// than locking the reservation the cancellation will want next. The order is made by holding a transaction open until
// the other is seen waiting, never by time.
func TestCapLockOrder(t *testing.T) {
	t.Parallel()
	r := newReservations(t)
	asked := r.pending()
	var proposal pg.Tx
	var approved error
	r.whileWaited(t, func(tx pg.Tx) error {
		proposal = tx
		_, err := tx.Exec(t.Context(), `SELECT id FROM operations.cap_counters WHERE site_id IS NULL ORDER BY object_kind, window_kind FOR UPDATE`)
		return err
	}, func() (err error) {
		approved, err = r.tryApprove(asked)
		return err
	}, func() {
		if err := alert.Persist(t.Context(), proposal, uuid.NewV7(), alert.Signal{Kind: "operations.approval_pending", Key: uuid.NewV7().String(), Severity: "warning"}); err != nil {
			t.Errorf("the proposal's signal behind the approval: %v", err)
		}
	})
	if approved != nil || r.state(asked) != "reserved" {
		t.Errorf("the approval after the proposal: %v %s", approved, r.state(asked))
	}

	due := r.direct(1)
	r.windowsEnded(t, due)
	if err := r.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT 1 FROM operations.operations WHERE id = $1 FOR UPDATE`, due.ID); err != nil {
			return err
		}
		if n := r.expireReservations(t); n != 0 {
			t.Errorf("the sweep took the reservation of an operation another transaction holds: %d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := r.expireReservations(t); n != 1 {
		t.Errorf("the sweep after the operation was let go: %d", n)
	}
}

// TestWindowEnded: a proposal checks, after the last lock it waits for, whether its UTC day is over by the database's
// clock as it is now, not as its transaction began.
func TestWindowEnded(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	for _, c := range []struct {
		day  string
		want bool
	}{{"date_trunc('day', clock_timestamp(), 'UTC')", false}, {"date_trunc('day', clock_timestamp(), 'UTC') - interval '1 day'", true}} {
		if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
			var day time.Time
			if err := tx.QueryRow(t.Context(), "SELECT "+c.day).Scan(&day); err != nil {
				return err
			}
			ended, err := operations.New().WindowEnded(t.Context(), tx, day)
			if err == nil && ended != c.want {
				t.Errorf("the day of %s ended: %v", c.day, ended)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}
