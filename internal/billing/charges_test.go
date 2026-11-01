package billing

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

func TestItemizedSalePrices(t *testing.T) {
	t.Parallel()

	s := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.0000000001","output":"0.0000000002","cache_read":"0.00000000003","cache_write":"0.0000000004","cache_write_1h":"0.0000000005","web_search":"0.01","code_execution":"0.02","file_search":"0.0025"},"context_tiers":{"basis":["input","cache_read","cache_write","cache_write_1h"],"method":"whole","tiers":[{"above":100,"rates":{"output":"0.0000000007"}}]},"service_tiers":{"priority":{"rates":{"input":"0.0000000008","output":"0.0000000009"},"context_tiers":[{"above":100,"rates":{"output":"0.000000001"}}]}}}`)
	f := fallbacks{perUse: map[catalog.Item]platform.UnitPrice{catalog.ItemWebSearch: mustUnitPrice("0.01"), catalog.ItemCodeExecution: mustUnitPrice("0.02")}}
	for _, tc := range []struct {
		name         string
		counts       map[catalog.Item]int64
		physical     *int64
		tier         catalog.ServiceTier
		tiers        catalog.ServiceTierSet
		unknown      []catalog.UnknownUsage
		waived       bool
		wantItems    int
		wantSubtotal string
		wantOutput   string
		fallback     bool
	}{
		{name: "cache overlap keeps the physical band", counts: map[catalog.Item]int64{catalog.ItemCacheRead: 80, catalog.ItemCacheWrite: 80, catalog.ItemOutput: 1}, physical: new(int64(100)), wantItems: 3, wantSubtotal: "0.0000000346", wantOutput: "0.0000000002"},
		{name: "long context", counts: map[catalog.Item]int64{catalog.ItemInput: 101, catalog.ItemOutput: 1}, wantItems: 2, wantSubtotal: "0.0000000108", wantOutput: "0.0000000007"},
		{name: "priority", counts: map[catalog.Item]int64{catalog.ItemInput: 1, catalog.ItemOutput: 1}, tier: catalog.ServiceTierPriority, wantItems: 2, wantSubtotal: "0.0000000017", wantOutput: "0.0000000009"},
		{name: "undeclared priority uses highest token price", counts: map[catalog.Item]int64{catalog.ItemInput: 1, catalog.ItemOutput: 1}, tier: catalog.ServiceTierPriority, tiers: 1 << catalog.ServiceTierStandard, wantItems: 2, wantSubtotal: "0.000000002", wantOutput: "0.000000001", fallback: true},
		{name: "missing cache price in priority", counts: map[catalog.Item]int64{catalog.ItemCacheWrite1h: 2}, tier: catalog.ServiceTierPriority, wantItems: 1, wantSubtotal: "0.000000002", fallback: true},
		{name: "waived model still charges per use", counts: map[catalog.Item]int64{catalog.ItemInput: 10, catalog.ItemOutput: 20, catalog.ItemWebSearch: 2, catalog.ItemCodeExecution: 3}, waived: true, wantItems: 2, wantSubtotal: "0.08"},
		{name: "waived model still charges file search", counts: map[catalog.Item]int64{catalog.ItemInput: 10, catalog.ItemOutput: 20, catalog.ItemFileSearch: 2}, waived: true, wantItems: 1, wantSubtotal: "0.005"},
		{name: "file search is charged beside tokens", counts: map[catalog.Item]int64{catalog.ItemInput: 10, catalog.ItemOutput: 20, catalog.ItemFileSearch: 1}, wantItems: 3, wantSubtotal: "0.002500005", wantOutput: "0.0000000002"},
		{name: "file search at a tier that does not price it uses its own dearest price", counts: map[catalog.Item]int64{catalog.ItemFileSearch: 1}, tier: catalog.ServiceTierPriority, wantItems: 1, wantSubtotal: "0.0025", fallback: true},
		{name: "unknowns hide upstream field names", unknown: []catalog.UnknownUsage{{Field: "supplier.secret_tokens", Value: "2"}, {Field: "supplier.secret", Value: "1.1"}, {Field: "prompt_tokens_details.cached_tokens_details.audio_tokens", Value: "9"}}, wantItems: 2, wantSubtotal: "0.040000002", fallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seg := chargeTestSegment(t, tc.counts, tc.tier, tc.unknown, tc.physical)
			items := chargeItems(s, f, seg, tc.waived, tc.tiers)
			if len(items) != tc.wantItems {
				t.Fatalf("items: %+v", items)
			}
			checkChargePrices(t, items, tc.wantSubtotal, tc.wantOutput, tc.fallback)
			priced, err := priceSegments(s, f, []Segment{seg}, tc.waived, tc.tiers)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reconcileCharges(ChargeDetails{Items: items}, priced.price, priced.price); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestFileSearchWithoutAnyPriceFallsBackToTokens: a per-use item with no site default is never charged another tool's
// price; a schedule that prices no file search at all charges it at the dearest token price and marks it estimated.
func TestFileSearchWithoutAnyPriceFallsBackToTokens(t *testing.T) {
	t.Parallel()
	s := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.0000000001","output":"0.0000000002"}}`)
	seg := chargeTestSegment(t, map[catalog.Item]int64{catalog.ItemFileSearch: 2}, catalog.ServiceTierStandard, nil, nil)
	items := chargeItems(s, fallbacks{}, seg, false, 0)
	if len(items) != 1 || items[0].Item != "file_search" || items[0].UnitPrice != "0.0000000002" {
		t.Fatalf("items: %+v", items)
	}
	checkChargePrices(t, items, "0.0000000004", "", true)
}

func TestReadChargesLocksSaleAndReconcilesDebit(t *testing.T) {
	t.Parallel()

	b := newBank(t, 10000, 0, Config{})
	q := metered(t)
	q.Version = uuid.NewV7()
	q.Price = mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.0000000001","output":"0.0000000002","web_search":"0.00000001"}}`)
	h := b.holdOn(q, 100)
	b.sql("UPDATE billing.balances SET balance=1")
	out := outcome(Committed, FinishStop, used(t, 3, 2, 0), used(t, 3, 2, 0))
	final := b.settleWith(h, out)
	first := readChargeTest(t, b, b.admitted.Scope, h.id)[h.id]
	if first.Subtotal != "0.0000000014" || first.Rounding != "0.0000000006" || first.BalanceAdjustment != "-0.000000001" || first.Charged != "0.000000001" || final.Charged.CeilAmount() != 1 {
		t.Fatalf("invoice: %+v, final %+v", first, final)
	}
	for j, item := range first.Items {
		if item.PriceVersion != q.Version.String() || item.Sampling != j/2+1 {
			t.Fatalf("item: %+v", item)
		}
	}
	b.settleWith(h, outcome(NotExecuted, FinishError, used(t, 100, 100, 1)))
	if retry := readChargeTest(t, b, b.admitted.Scope, h.id)[h.id]; !reflect.DeepEqual(first, retry) {
		t.Errorf("retry changed invoice: %+v / %+v", first, retry)
	}
	b.sql("UPDATE billing.reservations SET pricing_tiers=NULL")
	if got := readChargeTest(t, b, b.admitted.Scope, h.id); len(got) != 0 {
		t.Errorf("old writer invoice: %+v", got)
	}
}

func TestReadChargesExecutionAndScope(t *testing.T) {
	t.Parallel()

	b := newBank(t, 10000, 0, Config{})
	other := pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()})
	for _, tc := range []struct {
		result Result
		items  int
	}{{Committed, 3}, {Unknown, 1}, {NotExecuted, 0}} {
		h := b.hold(100)
		b.settleWith(h, outcome(tc.result, FinishStop, used(t, 3, 2, 1)))
		d, ok := readChargeTest(t, b, b.admitted.Scope, h.id)[h.id]
		if !ok || len(d.Items) != tc.items {
			t.Errorf("result %v: %+v", tc.result, d)
		}
		for _, item := range d.Items {
			if item.PriceVersion != "" {
				t.Errorf("invented version: %+v", item)
			}
		}
		if got := readChargeTest(t, b, other, h.id); len(got) != 0 {
			t.Errorf("other account: %+v", got)
		}
	}
}

func readChargeTest(t *testing.T, b *bank, scope pg.Scope, id uuid.UUID) map[uuid.UUID]ChargeDetails {
	t.Helper()
	var got map[uuid.UUID]ChargeDetails
	err := b.app.ReadTx(t.Context(), scope, func(tx pg.Tx) (err error) {
		got, err = NewChargeReader(nil, nil).ReadCharges(t.Context(), tx, []uuid.UUID{id, uuid.NewV7()})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func checkChargePrices(t *testing.T, items []ChargeItem, subtotal, output string, fallback bool) {
	t.Helper()
	for _, item := range items {
		if strings.Contains(item.Item, "supplier") {
			t.Errorf("unknown upstream field leaked: %+v", item)
		}
		if item.Item == "output" && item.UnitPrice != output {
			t.Errorf("output rate: %s", item.UnitPrice)
		}
		if item.Fallback != fallback || item.Estimated != fallback {
			t.Errorf("fallback/estimate: %+v", item)
		}
	}
	exact, err := platform.ParseExact(subtotal)
	if err != nil {
		t.Fatal(err)
	}
	d, err := reconcileCharges(ChargeDetails{Items: items}, ceilAmount(exact), ceilAmount(exact))
	if err != nil {
		t.Fatal(err)
	}
	if d.Subtotal != subtotal {
		t.Errorf("subtotal %s, want %s", d.Subtotal, subtotal)
	}
}

func assertTierChargeDetails(t *testing.T, b *bank, id uuid.UUID, declared bool, want int64) {
	t.Helper()
	d := readChargeTest(t, b, b.admitted.Scope, id)[id]
	if d.Subtotal != chargeDecimal(platform.AmountExact(want)) || d.Estimated == declared {
		t.Fatalf("tier invoice: %+v", d)
	}
	for _, item := range d.Items {
		if item.Fallback == declared || item.Estimated == declared {
			t.Errorf("tier item: %+v", item)
		}
	}
}

func chargeTestSegment(t *testing.T, counts map[catalog.Item]int64, tier catalog.ServiceTier, unknown []catalog.UnknownUsage, physical *int64) Segment {
	t.Helper()
	u, err := catalog.NewUsage(counts, tier, unknown)
	if err != nil {
		t.Fatal(err)
	}
	if physical != nil {
		u, err = u.WithPhysicalInput(*physical)
		if err != nil {
			t.Fatal(err)
		}
	}
	return Segment{Usage: u, Reported: ^catalog.ItemSet(0)}
}

func assertChargeVersion(t *testing.T, b *bank, id, version uuid.UUID) {
	t.Helper()
	d := readChargeTest(t, b, b.admitted.Scope, id)[id]
	if len(d.Items) == 0 {
		t.Fatal("missing item versions")
	}
	for _, item := range d.Items {
		if item.PriceVersion != version.String() {
			t.Fatalf("item version %s, want route entry %s", item.PriceVersion, version)
		}
	}
}

func TestChargeDetailsAtAmountLimit(t *testing.T) {
	t.Parallel()

	d, err := reconcileCharges(ChargeDetails{Items: []ChargeItem{{Item: "input", Quantity: 1e10, UnitPrice: "1000", Amount: "10000000000000"}}}, math.MaxInt64, 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.Subtotal != "10000000000000" || d.Rounding != "-9990776627963.145224193" || d.BalanceAdjustment != "-9223372036.8547758" || d.Charged != "0.000000007" {
		t.Fatalf("saturated invoice: %+v", d)
	}
}
