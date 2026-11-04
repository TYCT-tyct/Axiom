package main

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/gateway"
	"axiom/internal/platform/pg"
)

// TestTestWorldOutlastsTheProductionAdmissionBudget: a call's admission may wait on the database of a test world, which
// other tests share, for longer than production's admission budget. The test world must give its node the capacity for
// that wait; the tests of the budget itself set their own, as internal/gateway's rigs do.
func TestTestWorldOutlastsTheProductionAdmissionBudget(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	w.price()
	n := w.start()
	t.Cleanup(func() {
		if err := n.stop(); err != nil {
			t.Error(err)
		}
	})
	waitUntil(t, 15*time.Second, func() bool { return n.ready(t) })

	account := uuid.MustParse(w.query("SELECT account_id::text FROM accounts.users WHERE email='rich@example.com'"))
	admin, err := pg.Open(t.Context(), w.database.Superuser+"&pool_max_conns=2", pg.WithCompletionReserve(1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	locked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unlock)
	holding := make(chan error, 1)
	go func() {
		holding <- admin.Tx(context.WithoutCancel(t.Context()), pg.PlatformScope(), func(tx pg.Tx) error {
			var balance int64
			if err := tx.QueryRow(t.Context(), "SELECT balance FROM billing.balances WHERE account_id=$1 FOR UPDATE", account).Scan(&balance); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-holding:
		t.Fatal(err)
	}

	answered := make(chan struct {
		got reply
		err error
	}, 1)
	go func() {
		got, err := w.tryCall(n, "/v1/messages", w.richKey, messagesCall)
		answered <- struct {
			got reply
			err error
		}{got, err}
	}()
	// The test's only request is this call, so a transaction waiting on a lock here is its admission, held at the
	// account's balance row.
	waitUntil(t, 10*time.Second, func() bool {
		return w.query(`SELECT count(*)::text FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`) != "0"
	})
	// The wait is what this test is about: the row stays locked past the budget production gives an admission, as
	// internal/gateway's budget tests wait a budget. The margin puts the release outside it even when the machine pauses.
	time.Sleep(gateway.DefaultConfig().AdmissionBudget + 200*time.Millisecond)
	unlock()
	if err := <-holding; err != nil {
		t.Fatal(err)
	}
	out := <-answered
	if out.err != nil {
		t.Fatal(out.err)
	}
	if out.got.status != http.StatusOK {
		t.Fatalf("call: %d %s", out.got.status, out.got.body)
	}
}
