package payments

import (
	"context"
	"fmt"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestPollBackoffRespectsValidityBounds(t *testing.T) {
	for _, delay := range []int{30, 60, 120, 240, 480, 600} {
		for _, offset := range []int{-15, 15} {
			t.Run(fmt.Sprintf("%d/%d", delay, offset), func(t *testing.T) { testBackoffBound(t, delay, offset) })
		}
	}
}

func testBackoffBound(t *testing.T, delay, offset int) {
	t.Helper()
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	o := w.mustPlace(yuan, "backoff-bound")
	// Compare persisted boundaries, without sleeping for the channel's next due time. The 15s fence exceeds the 5s call budget.
	w.exec(fmt.Sprintf(`UPDATE payments.orders SET next_query_at=transaction_timestamp()-interval '1 second',
 query_delay_seconds=%d, expires_at=transaction_timestamp()+make_interval(secs=>%d)`, delay, delay+offset))
	if err := w.p.queryScheduled(t.Context(), o.ID); err != nil {
		t.Fatal(err)
	}
	var afterExpiry bool
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT next_query_at>expires_at FROM payments.orders WHERE id=$1", uuid.UUID(o.ID)).Scan(&afterExpiry)
	}); err != nil {
		t.Fatal(err)
	}
	if afterExpiry != (offset < 0) {
		t.Fatalf("delay %d with %d seconds validity: next query after expiry=%v", delay, delay+offset, afterExpiry)
	}
}

func TestDailyQueryDefiniteRefusalCompletesPage(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(context.Context, string, string) (Payment, error) { return Payment{}, &Rejected{Code: "missing"} }})
	m := w.waiting()
	o := w.mustPlace(yuan, "daily-refused")
	w.exec("UPDATE payments.orders SET created_at=transaction_timestamp()-interval '1 day'")
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	if next, err := w.p.reconcileQueries(t.Context(), m.ID, day, uuid.UUID{}); err != nil || next != (uuid.UUID{}) {
		t.Fatal("definite query refusal retained page", next, err)
	}
	if state, _, _ := w.stored(o.ID); state != "pending" {
		t.Fatal("query refusal failed the order", state)
	}
}

func TestPollBackoffStartsAfterThirtySeconds(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	o := w.mustPlace(yuan, "initial-query")
	var scheduled bool
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT next_query_at=created_at+interval '30 seconds' AND query_delay_seconds=30 FROM payments.orders WHERE id=$1`, uuid.UUID(o.ID)).Scan(&scheduled)
	}); err != nil {
		t.Fatal(err)
	}
	if !scheduled {
		t.Fatal("first poll query is not scheduled thirty seconds after order creation")
	}
}
