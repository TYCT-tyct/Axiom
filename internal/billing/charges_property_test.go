package billing

import (
	"math/rand/v2"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

// FuzzChargeItemsMatchSettlement compares exact subtotals with settlement's pricing primitives, then checks the
// rounded settlement and public reconciliation. Committed seeds each exercise hundreds of multi-sampling calls.
func FuzzChargeItemsMatchSettlement(f *testing.F) {
	for seed := range uint64(4) {
		f.Add(seed, ^seed)
	}
	f.Fuzz(func(t *testing.T, a, b uint64) {
		r := rand.New(rand.NewPCG(a, b)) //nolint:gosec // reproducible test inputs
		for range 200 {
			checkRandomCharge(t, r)
		}
	})
}

func checkRandomCharge(t *testing.T, r *rand.Rand) {
	t.Helper()
	s := randomChargeSchedule(t, r)
	defaults := fallbacks{perUse: map[catalog.Item]platform.UnitPrice{catalog.ItemWebSearch: mustUnitPrice("0.0137"), catalog.ItemCodeExecution: mustUnitPrice("0.021")}}
	tiers := catalog.ServiceTierSet(r.IntN(16)) //nolint:gosec // the four declared service-tier bits
	waived := r.IntN(2) == 0
	var segments []Segment
	d := ChargeDetails{Items: []ChargeItem{}}
	var exact platform.Exact
	for range r.IntN(5) {
		seg := randomChargeSegment(t, r)
		segments = append(segments, seg)
		d.Items = append(d.Items, chargeItems(s, defaults, seg, waived, tiers)...)
		amount := settlementSubtotal(t, s, defaults, seg, waived, tiers)
		var err error
		exact, err = exact.Add(amount)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(segments) > 0 {
		if rate, band, ok := requestRate(s, defaults, segments[len(segments)-1], tiers); ok {
			last := segments[len(segments)-1]
			d.Items = append(d.Items, ChargeItem{
				Item: catalog.ItemRequest.String(), Quantity: 1, Unit: catalog.ItemRequest.Unit().String(),
				UnitPrice: rate.String(), Amount: chargeDecimal(rate.Mul(1)), ServiceTier: last.Usage.ServiceTier().String(),
				ContextBand: band,
			})
			amount, err := exact.Add(rate.Mul(1))
			if err != nil {
				t.Fatal(err)
			}
			exact = amount
		}
	}
	p, err := priceSegments(s, defaults, segments, waived, tiers)
	if err != nil {
		t.Fatal(err)
	}
	// Charge less than the rounded subtotal too, so reconciliation cannot depend on a fully paid call.
	got, err := reconcileCharges(d, p.price, p.price/2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subtotal != chargeDecimal(exact) || p.price != ceilAmount(exact) {
		t.Fatalf("itemized %s, exact settlement %s, rounded %d", got.Subtotal, chargeDecimal(exact), p.price)
	}
}

func settlementSubtotal(t *testing.T, s catalog.Schedule, f fallbacks, seg Segment, waived bool, tiers catalog.ServiceTierSet) platform.Exact {
	t.Helper()
	cost, missing, err := segmentCost(s, seg.Usage, waived)
	if undeclaredTier(tiers, seg.Usage.ServiceTier()) {
		cost, missing, err = platform.Exact{}, ^catalog.ItemSet(0), nil
	}
	if err != nil {
		t.Fatal(err)
	}
	cost, err = addFallbacks(s, f, cost, seg.Usage.Unknown(), chargedItems(waived))
	if err != nil {
		t.Fatal(err)
	}
	cost, err = addMissing(s, f, cost, seg.Usage, missing, chargedItems(waived))
	if err != nil {
		t.Fatal(err)
	}
	return cost
}

func randomChargeSchedule(t *testing.T, r *rand.Rand) catalog.Schedule {
	t.Helper()
	var raw map[string]any
	if err := jsonx.Unmarshal([]byte(randomPrices(r)), &raw); err != nil {
		t.Fatal(err)
	}
	rates := raw["rates"].(map[string]any)
	for it := range catalog.NumItems {
		if r.IntN(3) == 0 {
			delete(rates, it.String())
		} else if r.IntN(2) == 0 {
			rates[it.String()] = "0.000000000000001"
		}
	}
	body, err := jsonx.MarshalCanonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	return mustSchedule(t, string(body))
}

func randomChargeSegment(t *testing.T, r *rand.Rand) Segment {
	t.Helper()
	extreme := r.IntN(8) == 0
	counts, reported := randomCounts(r, extreme)
	unknown := randomUnknowns(r, extreme)
	tier := []catalog.ServiceTier{catalog.ServiceTierStandard, catalog.ServiceTierFlex, catalog.ServiceTierPriority, catalog.ServiceTierFast}[r.IntN(4)]
	u, err := catalog.NewUsage(counts, tier, unknown)
	if err != nil {
		t.Fatal(err)
	}
	if r.IntN(2) == 0 {
		physical := []int64{0, 100, 101, 2000, 2001, maxUnknownCount}[r.IntN(6)]
		u, err = u.WithPhysicalInput(physical)
		if err != nil {
			t.Fatal(err)
		}
	}
	return Segment{Usage: u, Reported: reported}
}

// randomCounts is the item counts of a random usage and which of them the upstream reported. The request item is never
// there: the gateway charges it once per call, never as upstream-reported usage.
func randomCounts(r *rand.Rand, extreme bool) (map[catalog.Item]int64, catalog.ItemSet) {
	counts := map[catalog.Item]int64{}
	var reported catalog.ItemSet
	for it := range catalog.NumItems {
		if it == catalog.ItemRequest {
			continue
		}
		counts[it] = r.Int64N(4097)
		if r.IntN(3) == 0 {
			counts[it] = 0
		}
		if extreme && r.IntN(2) == 0 {
			counts[it] = maxUnknownCount
		}
		if r.IntN(2) == 0 {
			reported.Add(it)
		}
	}
	return counts, reported
}

// randomUnknowns is up to three fields the protocol could not map, sometimes with a value no number can hold.
func randomUnknowns(r *rand.Rand, extreme bool) []catalog.UnknownUsage {
	fields := []string{"usage.new_tokens", "usage.new_uses", "usage.prompt_tokens_details.cached_tokens_details.audio_tokens"}
	values := []string{"0", "1", "2.5", "invalid", "-1", "4"}
	if extreme {
		values = append(values, "10000000000", "10000000001")
	}
	var unknown []catalog.UnknownUsage
	for range r.IntN(4) {
		unknown = append(unknown, catalog.UnknownUsage{Field: fields[r.IntN(len(fields))], Value: values[r.IntN(len(values))]})
	}
	return unknown
}
