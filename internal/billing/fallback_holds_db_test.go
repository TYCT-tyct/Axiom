package billing

import (
	"bytes"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"
	"uuid"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// TestFallbackHoldsPages: pages of the listing, with windows that end at the same moment across a page's edge, give
// every hold once, in (window, request) order.
func TestFallbackHoldsPages(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, Config{})
	want := make([]uuid.UUID, 5)
	for i := range want {
		h := b.hold(100)
		b.settleWith(h, outcome(Committed, FinishStop, false, fetches(t, 50, "1")))
		want[i] = h.id
	}
	b.sql("UPDATE billing.reservations SET correct_by = '2030-01-01T00:00:00Z'")
	slices.SortFunc(want, func(x, y uuid.UUID) int { return bytes.Compare(x[:], y[:]) })
	var got []uuid.UUID
	var after *FallbackHold
	for range len(want) {
		page := b.fallbackHoldsPage(after, 2)
		for _, h := range page {
			got = append(got, h.Request)
		}
		if len(page) < 2 {
			break
		}
		after = &page[len(page)-1]
	}
	if !slices.Equal(got, want) {
		t.Errorf("paged %v, want %v", got, want)
	}
}

// TestUnknownItemsOnEveryAttempt: an attempt that reported a field nothing prices and then failed takes its own model
// out of routing, though the fallback's model is charged, and so does a supplier record of it; the charge follows the
// charged attempt alone.
func TestUnknownItemsOnEveryAttempt(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, Config{})
	h, first := b.holdOnTwoModels()
	o := Outcome{Attempts: []AttemptResult{
		{Seq: 1, Entry: 0, Cost: 5, Segments: []Segment{fetches(t, 50, "2")}, Result: FailedBeforeCommit, Finish: FinishError},
		{Seq: 2, Entry: 1, Cost: 7, Segments: []Segment{used(t, 50, 20, 0)}, Result: Committed, Finish: FinishStop},
	}}
	if f := b.settleWith(h, o); f.Charged != platform.AmountExact(70) || f.Estimated {
		t.Errorf("Settle = %+v, want the fallback's 70, exact", f)
	}
	b.correct(h, 1, 1, 9, unknown(t, 50, "usage.new_thing_tokens", "5"))
	b.expect("settled", 70)
	items := b.unknownItems()
	for i := range items {
		items[i].Seen = time.Time{}
	}
	want := []UnknownItem{
		{first.Model.ID, "usage.new_thing_tokens", h.id, time.Time{}},
		{first.Model.ID, "usage.server_tool_use.web_fetch_requests", h.id, time.Time{}},
	}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("unknown items %+v, want %+v", items, want)
	}
}

// TestRecordBeforeItsAttempt: a supplier record with a field nothing prices, of an attempt no Settle or cleanup has
// recorded yet, applies nothing; the redelivery of the same revision, after Settle or cleanup, notes the field against
// the attempt's own model and applies the record. The gateway's usage of the attempt does not have the field.
func TestRecordBeforeItsAttempt(t *testing.T) {
	t.Run("then Settle", recordThenSettle)
	t.Run("then cleanup", recordThenCleanup)
}

const newThing = "usage.new_thing_tokens"

func recordThenSettle(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, Config{})
	h, first := b.holdOnTwoModels()
	record := Correction{Request: h.id, Seq: 1, Revision: 1, Usage: usages([]Segment{unknown(t, 50, newThing, "5")}), Cost: 9}
	b.correctEarly(record)
	o := Outcome{Attempts: []AttemptResult{
		{Seq: 1, Entry: 0, Cost: 5, Segments: []Segment{used(t, 50, 0, 0)}, Result: FailedBeforeCommit, Finish: FinishError},
		{Seq: 2, Entry: 1, Cost: 7, Segments: []Segment{used(t, 50, 20, 0)}, Result: Committed, Finish: FinishStop},
	}}
	if f := b.settleWith(h, o); f.Charged != platform.AmountExact(70) || f.Estimated {
		t.Errorf("Settle = %+v, want 70, exact", f)
	}
	b.redeliver(record, first.Model.ID)
	if cost := b.cost(h, 1); cost != 9 {
		t.Errorf("attempt 1 cost %d, want the record's 9", cost)
	}
	b.expect("settled", 70)
}

func recordThenCleanup(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, lapsedWindow)
	q := metered(t)
	q.Model.ID = catalog.ModelID(uuid.NewV7())
	h := b.holdOn(q, 1000)
	b.supplies.journal[h.id] = JournalAttempt{Seq: 1, Key: "client:abc"}
	record := Correction{Request: h.id, Seq: 1, Revision: 1, Usage: usages([]Segment{unknown(t, 400, newThing, "40")}), Cost: 100}
	b.correctEarly(record)
	b.cleanup()
	b.expect("cleanup_pending", 0)
	b.redeliver(record, q.Model.ID)
	b.expect("cleanup_settled", 150) // ⌈1.5 × 100⌉
}

// holdOnTwoModels reserves 100 text bytes on two metered entries of their own models, and returns the first.
func (b *bank) holdOnTwoModels() (*Hold, Quotable) {
	b.t.Helper()
	first, second := metered(b.t), metered(b.t)
	first.Model.ID, second.Model.ID = catalog.ModelID(uuid.NewV7()), catalog.ModelID(uuid.NewV7())
	var h *Hold
	if err := b.app.Tx(b.t.Context(), b.admitted.Scope, func(tx pg.Tx) (err error) {
		h, _, err = b.billing.Reserve(b.t.Context(), tx, b.admitted, uuid.NewV7(), Demand{TextBytes: 100}, []Quotable{first, second})
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return h, first
}

// correctEarly: c, of an attempt not recorded yet, applies nothing and notes nothing.
func (b *bank) correctEarly(c Correction) {
	b.t.Helper()
	if err := b.billing.Correct(b.t.Context(), c); !errors.Is(err, ErrNotRecorded) {
		b.t.Fatalf("Correct before the attempt is recorded = %v, want ErrNotRecorded", err)
	}
	if items := b.unknownItems(); len(items) != 0 {
		b.t.Fatalf("unknown items %+v before the attempt is recorded", items)
	}
}

// redeliver applies c again and finds newThing noted against model, for c's request.
func (b *bank) redeliver(c Correction, model catalog.ModelID) {
	b.t.Helper()
	if err := b.billing.Correct(b.t.Context(), c); err != nil {
		b.t.Fatal(err)
	}
	if items := b.unknownItems(); len(items) != 1 || items[0].Model != model || items[0].Field != newThing || items[0].Request != c.Request {
		b.t.Errorf("unknown items %+v, want %s on the attempt's model", items, newThing)
	}
}

// TestCloseHold: the fallback holds are listed and counted on the gauges, an estimate on unreported usage is not; an
// operator closes one, audited, and cleanup settles it at its charge and releases the rest.
func TestCloseHold(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, Config{})
	reader := sdkmetric.NewManualReader()
	if err := RegisterMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("billing"), b.app, b.platform); err != nil {
		t.Fatal(err)
	}
	fallback, guessed := b.hold(1000), b.hold(1000)
	b.settleWith(fallback, outcome(Committed, FinishStop, false, fetches(t, 50, "2")))
	b.settleWith(guessed, outcome(Committed, FinishStop, false, used(t, 300, 20, 0, in)))
	if holds := b.fallbackHolds(); len(holds) != 1 || holds[0].Request != fallback.id || holds[0].Held != 1100-170 || holds[0].By.IsZero() {
		t.Fatalf("FallbackHolds = %+v; want the fallback estimate holding 930", holds)
	}
	wantGauges(t, reader, 1, 930)
	if err := b.closeHold(fallback.id, " "); err == nil {
		t.Error("a close without a reason went through")
	}
	if err := b.closeHold(fallback.id, "no record is coming"); err != nil {
		t.Fatal(err)
	}
	if err := b.closeHold(fallback.id, "again"); !errors.Is(err, ErrNotFound) {
		t.Errorf("closing again = %v, want ErrNotFound", err)
	}
	b.cleanup()
	if s := b.count("SELECT count(*) FROM billing.reservations WHERE state = 'settled' AND charged = 170 AND request_id = '" + fallback.id.String() + "'"); s != 1 {
		t.Error("the closed hold did not settle at 170")
	}
	if n := b.count("SELECT reserved FROM billing.balances"); n != 1100-320 {
		t.Errorf("%d reserved, want the other estimate's 780 alone", n)
	}
	wantGauges(t, reader, 0, 0)
	if n := b.count("SELECT count(*) FROM platform.audit_log WHERE action = 'hold.close'"); n != 1 {
		t.Errorf("%d audit entries, want 1", n)
	}
	b.balanced()
}

func (b *bank) fallbackHolds() []FallbackHold { return b.fallbackHoldsPage(nil, FallbackHoldsPage) }

func (b *bank) fallbackHoldsPage(after *FallbackHold, limit int) (holds []FallbackHold) {
	b.t.Helper()
	if err := b.app.ReadTx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		holds, err = FallbackHolds(b.t.Context(), tx, after, limit)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return holds
}

func (b *bank) closeHold(request uuid.UUID, reason string) error {
	r := CloseHoldRequest{Request: request, Reason: reason}
	r.SetActor(audit.System("billing.test"))
	return b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error { return CloseHold(b.t.Context(), tx, r) })
}

func wantGauges(t *testing.T, reader *sdkmetric.ManualReader, holds, held int64) {
	t.Helper()
	if g := gauges(t, reader); g["axiom.billing.fallback_holds"] != holds || g["axiom.billing.fallback_holds.held"] != held {
		t.Errorf("gauges %v, want %d and %d", g, holds, held)
	}
}

func gauges(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int64{}
	for _, sm := range data.ScopeMetrics {
		for _, m := range sm.Metrics {
			if g, ok := m.Data.(metricdata.Gauge[int64]); ok && len(g.DataPoints) == 1 {
				seen[m.Name] = g.DataPoints[0].Value
			}
		}
	}
	return seen
}
