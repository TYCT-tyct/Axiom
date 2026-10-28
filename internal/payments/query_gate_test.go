package payments

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestMerchantQueryCapAcrossServices(t *testing.T) {
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var calls atomic.Int32
	plugin := fakePlugin{create: goodCheckout, query: func(ctx context.Context, _, _ string) (Payment, error) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			return Payment{State: PayPending}, nil
		case <-ctx.Done():
			return Payment{}, ctx.Err()
		}
	}}
	w := newTestWorld(t, plugin)
	w.waiting()
	o := w.mustPlace(yuan, "shared-cap")
	other, err := Open(Config{DB: w.db, Platform: w.p.platform, Keyring: w.p.keyring, Plugins: []Plugin{plugin}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for _, p := range []*Payments{w.p, other} {
		go func() { done <- p.queryOrder(t.Context(), o) }()
	}
	for range 2 {
		<-entered
	}
	err = other.queryOrder(t.Context(), o)
	close(release)
	if !errors.Is(err, ErrQueryPaused) {
		t.Fatalf("third query exceeded merchant cap: %v", err)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("two service instances exceeded two calls")
	}
}

func TestQueryLeaseReclaimsCrashAndFencesOldCompletion(t *testing.T) {
	w := newWorld(t)
	m := w.waiting()
	merchant := MerchantID(m.ID)
	first, err := w.p.acquireQuery(t.Context(), merchant)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.p.acquireQuery(t.Context(), merchant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.p.acquireQuery(t.Context(), merchant); !errors.Is(err, ErrQueryPaused) {
		t.Fatal("third lease allowed", err)
	}
	w.exec("UPDATE payments.query_health SET first_until=transaction_timestamp()-interval '1 second'")
	replacement, err := w.p.acquireQuery(t.Context(), merchant)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.p.finishQuery(t.Context(), merchant, first, queryUnknown); err != nil {
		t.Fatal(err)
	}
	if _, err = w.p.acquireQuery(t.Context(), merchant); !errors.Is(err, ErrQueryPaused) {
		t.Fatal("stale completion released replacement", err)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.query_health WHERE failures=0"); n != 1 {
		t.Fatal("stale completion changed breaker")
	}
	for _, token := range []uuid.UUID{second, replacement} {
		if err = w.p.finishQuery(t.Context(), merchant, token, queryUnknown); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.p.finishQuery(t.Context(), merchant, replacement, queryUnknown); err != nil {
		t.Fatal(err)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.query_health WHERE failures=2 AND first_token IS NULL AND second_token IS NULL"); n != 1 {
		t.Fatal("completion was counted twice or failed to release")
	}
}

func TestUnusedQuerySlotDoesNotResetUnknownStreak(t *testing.T) {
	w := newWorld(t)
	m := w.waiting()
	for _, outcome := range []queryOutcome{queryUnknown, queryUnused, queryUnknown} {
		token, err := w.p.acquireQuery(t.Context(), MerchantID(m.ID))
		if err != nil {
			t.Fatal(err)
		}
		if err = w.p.finishQuery(t.Context(), MerchantID(m.ID), token, outcome); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.query_health WHERE failures=2"); n != 1 {
		t.Fatal("unused scheduled claim reset unknown streak")
	}
	if err := w.tx(w.accountB(), func(tx pg.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM payments.query_health").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("account saw merchant query health")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInFlightSuccessCannotReopenPausedMerchant(t *testing.T) {
	w := newWorld(t)
	m := w.waiting()
	merchant := MerchantID(m.ID)
	inFlight, err := w.p.acquireQuery(t.Context(), merchant)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		token, err := w.p.acquireQuery(t.Context(), merchant)
		if err != nil {
			t.Fatal(err)
		}
		if err = w.p.finishQuery(t.Context(), merchant, token, queryUnknown); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.p.finishQuery(t.Context(), merchant, inFlight, queryKnown); err != nil {
		t.Fatal(err)
	}
	if _, err = w.p.acquireQuery(t.Context(), merchant); !errors.Is(err, ErrQueryPaused) {
		t.Fatal("old success reopened circuit", err)
	}
	if w.count(w.p.platform, "SELECT count(*) FROM platform.alert_signals WHERE body->>'resolved'='true'") != 0 {
		t.Fatal("old success resolved active pause")
	}
}

func TestQueryHealthUsesTheClockAfterLocking(t *testing.T) {
	w := newWorld(t)
	m := w.waiting()
	if _, err := w.p.acquireQuery(t.Context(), MerchantID(m.ID)); err != nil {
		t.Fatal(err)
	}
	// The controlled clock returns a different value until this transaction owns the tuple lock (xmax).
	w.exec(`CREATE FUNCTION payments.clock_timestamp() RETURNS timestamptz LANGUAGE sql VOLATILE AS $$
 SELECT CASE WHEN h.xmax::text=pg_catalog.pg_current_xact_id()::text
 THEN timestamptz '2000-01-01 12:00:00+00' ELSE timestamptz '1999-01-01 12:00:00+00' END
 FROM payments.query_health h $$`)
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), "SET LOCAL search_path=payments,pg_catalog"); err != nil {
			return err
		}
		health, err := queries.LockQueryHealth(t.Context(), tx, m.ID)
		if err != nil {
			return err
		}
		want := time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)
		if !health.ObservedAt.Equal(want) {
			t.Fatal("query pause policy read the clock before its state lock", health.ObservedAt)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
