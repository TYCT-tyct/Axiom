package billing

import (
	"bytes"
	"reflect"
	"slices"
	"testing"
	"time"
	"uuid"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// TestFallbackHoldsPages: pages of the listing, with windows that end at the same moment across a page's edge, give
// every hold once, in (window, request) order. A charge holds nothing once it is settled
// (docs/domains/billing.md「用量来源与暂估」), so the rows are put back the way a database written before the correction
// window was removed has them.
func TestFallbackHoldsPages(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, Config{})
	want := make([]uuid.UUID, 5)
	for i := range want {
		h := b.hold(100)
		b.settleWith(h, outcome(Committed, FinishStop, false, fetches(t, "1")))
		want[i] = h.id
	}
	b.sql("UPDATE billing.reservations SET state = 'estimated', correct_by = '2030-01-01T00:00:00Z'")
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

// TestUnknownItemsOnEveryAttempt: an attempt that reported fields nothing prices and then failed takes its own model
// out of routing, though the other entry's attempt is the one charged, and the charge follows the charged attempt
// alone.
func TestUnknownItemsOnEveryAttempt(t *testing.T) {
	const newThing = "usage.new_thing_tokens"
	b := fallbackBank(t, 10_000, 0, Config{})
	h, first := b.holdOnTwoModels()
	failed := segment(t, map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20},
		catalog.UnknownUsage{Field: newThing, Value: "5"},
		catalog.UnknownUsage{Field: "usage.server_tool_use.web_fetch_requests", Value: "2"})
	o := Outcome{Attempts: []AttemptResult{
		{Seq: 1, Entry: 0, Cost: 5, Segments: []Segment{failed}, Result: FailedBeforeCommit, Finish: FinishError},
		{Seq: 2, Entry: 1, Cost: 7, Segments: []Segment{used(t, 50, 20, 0)}, Result: Committed, Finish: FinishStop},
	}}
	if f := b.settleWith(h, o); f.Charged != platform.AmountExact(70) || f.Estimated {
		t.Errorf("Settle = %+v, want the fallback's 70, exact", f)
	}
	b.expect("settled", 70)
	items := b.unknownItems()
	for i := range items {
		items[i].Seen = time.Time{}
	}
	want := []UnknownItem{
		{first.Model.ID, newThing, h.id, time.Time{}},
		{first.Model.ID, "usage.server_tool_use.web_fetch_requests", h.id, time.Time{}},
	}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("unknown items %+v, want %+v", items, want)
	}
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

// TestSettledCallsHoldNothing: an estimate resting on usage only a fallback price covers is charged for good when it is
// settled, so the listing of holds and its gauges stay empty and the rest of the reservation goes back at once.
func TestSettledCallsHoldNothing(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, Config{})
	reader := sdkmetric.NewManualReader()
	if err := RegisterMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("billing"), b.app, b.platform); err != nil {
		t.Fatal(err)
	}
	fallback, guessed := b.hold(1000), b.hold(1000)
	b.settleWith(fallback, outcome(Committed, FinishStop, false, fetches(t, "2")))
	b.settleWith(guessed, outcome(Committed, FinishStop, false, used(t, 300, 20, 0, in)))
	if holds := b.fallbackHolds(); len(holds) != 0 {
		t.Errorf("FallbackHolds = %+v, want none", holds)
	}
	wantGauges(t, reader, 0, 0)
	if n := b.count("SELECT reserved FROM billing.balances"); n != 0 {
		t.Errorf("%d reserved, want 0: a settled call holds nothing", n)
	}
	if n := b.count("SELECT count(*) FROM billing.reservations WHERE state = 'settled' AND charged > 0"); n != 2 {
		t.Errorf("%d settled charges, want 2", n)
	}
	if n := b.count("SELECT count(*) FROM platform.audit_log WHERE action = 'hold.close'"); n != 0 {
		t.Errorf("%d audits of a hold closed, want none: no hold can be closed any more", n)
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
