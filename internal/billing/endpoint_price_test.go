package billing

import (
	"bytes"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func rateOf(t *testing.T, schedule catalog.Schedule, item catalog.Item) string {
	t.Helper()
	rate, ok := schedule.BaseRate(item)
	if !ok {
		t.Fatalf("%s is not priced", item)
	}
	return rate.String()
}

// TestEnvelopeKeepsSingleEndpointFallbacks pins the canonical envelope of one endpoint to the empty-schedule fold the
// shared-price path used before endpoint prices. That fold expands reasoning and image fallbacks, so a candidate built
// after an upgrade keeps the bytes it had before and an administrator's existing exception is not re-decided.
func TestEnvelopeKeepsSingleEndpointFallbacks(t *testing.T) {
	t.Parallel()

	c := priceCost(t, catalog.ModelID(uuid.NewV7()), "0.000001")
	converted, err := c.converted(usd)
	if err != nil {
		t.Fatal(err)
	}
	got, err := envelopeOf(EndpointPrices{c.Endpoint: converted})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	want, err := empty.Aligned(converted, false)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := got.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := want.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("single-endpoint envelope changed its canonical form:\n got %s\nwant %s", gotJSON, wantJSON)
	}
	if _, ok := got.BaseRate(catalog.ItemReasoning); !ok {
		t.Fatal("the envelope did not expand the reasoning fallback")
	}
}

//nolint:cyclop,gocognit // one publication through its per-endpoint prices, the model envelope and an old version
func TestCostPlusPricesEachEndpoint(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	f.method(plus("0"))
	second := priceCost(t, f.model, "0.000002")
	second.Schedule = mustSchedule(t, `{"v":1,"currency":"XXX","rates":{"input":"0.000002","output":"0.000001"}}`)
	f.costs = append(f.costs, second)
	sold := f.refresh()[f.model]
	if sold.Version == uuid.Nil() || len(sold.Endpoints) != 2 {
		t.Fatalf("sold %+v", sold)
	}
	cheap, ok := sold.PriceFor(f.costs[0].Endpoint)
	if !ok {
		t.Fatal("the first endpoint has no price")
	}
	dear, ok := sold.PriceFor(second.Endpoint)
	if !ok {
		t.Fatal("the second endpoint has no price")
	}
	if got := rateOf(t, cheap, catalog.ItemInput); got != "0.0000005" {
		t.Errorf("first endpoint input %s", got)
	}
	if got := rateOf(t, cheap, catalog.ItemOutput); got != "0.000001" {
		t.Errorf("first endpoint output %s", got)
	}
	if got := rateOf(t, dear, catalog.ItemInput); got != "0.000002" {
		t.Errorf("second endpoint input %s", got)
	}
	// The model-level schedule is the dearest endpoint's price, not the first or the last converted: an old node
	// charging it still covers every endpoint's floor.
	if got := rateOf(t, sold.Schedule, catalog.ItemInput); got != "0.000002" {
		t.Errorf("model input %s", got)
	}
	for _, c := range f.costs {
		cost, err := c.converted(usd)
		if err != nil {
			t.Fatal(err)
		}
		floor, err := cost.Scaled("1", usd)
		if err != nil {
			t.Fatal(err)
		}
		if safe, err := sold.Schedule.CoversCost(floor); err != nil || !safe {
			t.Errorf("the model schedule does not cover endpoint %s's floor: %v", c.Endpoint, err)
		}
	}

	read := f.published()
	for _, id := range []uuid.UUID{f.costs[0].Endpoint, second.Endpoint} {
		want, _ := sold.PriceFor(id)
		got, ok := read.PriceFor(id)
		if !ok || rateOf(t, got, catalog.ItemInput) != rateOf(t, want, catalog.ItemInput) {
			t.Errorf("the published version read back differs for %s", id)
		}
	}
	// A version written before this release has no endpoints object: every endpoint charges its one schedule. Price
	// versions are immutable, so the old version is a new row with the one schedule and no endpoints, made current.
	schedule, err := sold.Schedule.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	oldID := uuid.NewV7()
	if err := f.tx(func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO billing.price_versions
			(id, site_id, model_id, candidate_id, schedule, basis, overridden, actor, reason)
			VALUES ($1, $2, $3, $4, $5, '{}', false, 'billing.test', 'a version before endpoint prices')`,
			oldID, f.site, uuid.UUID(f.model), uuid.NewV7(), schedule); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE billing.price_publications SET current_id = $1
			WHERE site_id = $2 AND model_id = $3`, oldID, f.site, uuid.UUID(f.model))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	old := f.published()
	for _, id := range []uuid.UUID{f.costs[0].Endpoint, second.Endpoint} {
		got, ok := old.PriceFor(id)
		if !ok || rateOf(t, got, catalog.ItemInput) != rateOf(t, old.Schedule, catalog.ItemInput) {
			t.Errorf("an old version did not serve endpoint %s at its one price", id)
		}
	}
}

// TestEndpointRaiseInsideTheEnvelopeBlocks: the dearest endpoint keeps the model envelope unchanged while a cheaper
// endpoint's cost rises threefold. The model-level gate would publish the cheaper endpoint's new price without
// confirmation; the endpoint gate must stop it.
func TestEndpointRaiseInsideTheEnvelopeBlocks(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	f.method(plus("0"))
	second := priceCost(t, f.model, "0.000002")
	// Keep the dearest endpoint's output high enough that scaling the cheaper endpoint's whole cost does not move
	// the model's output envelope either; otherwise the old model-level gate would also block and the test would not
	// prove the endpoint gate is reached.
	second.Schedule = mustSchedule(t, `{"v":1,"currency":"XXX","rates":{"input":"0.000002","output":"0.00001"}}`)
	f.costs = append(f.costs, second)
	if sold := f.refresh()[f.model]; sold.Version == uuid.Nil() {
		t.Fatal("the first publication has no version")
	}
	before, err := f.published().Schedule.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	f.costs[0].UnitValue = "3"
	f.refresh()
	after, err := catalog.ParseSchedule(f.state().Schedule)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, err := after.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, afterJSON) {
		t.Fatalf("the model envelope moved, so this test would not isolate the endpoint gate:\nbefore %s\nafter  %s", before, afterJSON)
	}
	if got := f.state().Status; got != "blocked" {
		t.Fatalf("status after a cheaper endpoint's cost rose = %s, want blocked", got)
	}
}

// TestSharedPriceAfterEndpointPricesIsGated covers switching a model with separate endpoint prices back to one fixed
// price. The model envelope is unchanged, but the cheaper endpoint's old price changes from 0.5e-6 to 2e-6, so the
// shared model-level gate alone would publish it without confirmation.
func TestSharedPriceAfterEndpointPricesIsGated(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	f.method(plus("0"))
	f.costs = append(f.costs, priceCost(t, f.model, "0.000002"))
	before := f.refresh()[f.model]
	if before.Version == uuid.Nil() {
		t.Fatal("the first publication has no version")
	}
	f.method(Method{Kind: "fixed", Schedule: jsonx.Raw(`{"v":1,"currency":"USD","rates":{"input":"0.000002","output":"0.000001"}}`)})
	f.refresh()
	if got := f.state().Status; got != "pending" {
		t.Fatalf("shared price after endpoint prices = %s, want pending", got)
	}
	if got := f.published().Version; got != before.Version {
		t.Fatalf("pending publication replaced the current version: %s, want %s", got, before.Version)
	}
}

//nolint:cyclop // one per-endpoint floor check against both endpoint and model prices
func TestCostPlusFloorsEachEndpoint(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	f.method(plus("0"))
	second := priceCost(t, f.model, "0.000002")
	second.Schedule = mustSchedule(t, `{"v":1,"currency":"XXX","rates":{"input":"0.000002","output":"0.000001"}}`)
	f.costs = append(f.costs, second)
	if err := f.tx(func(tx pg.Tx) error {
		return SetPricePolicy(t.Context(), tx, system, f.site, PricePolicy{FloorMultiplier: "2", ChangeRatio: "0.1", MaxIncreaseRatio: "1"}, "test floor")
	}); err != nil {
		t.Fatal(err)
	}
	sold := f.refresh()[f.model]
	cheap, ok := sold.PriceFor(f.costs[0].Endpoint)
	if !ok {
		t.Fatal("the first endpoint has no price")
	}
	dear, ok := sold.PriceFor(second.Endpoint)
	if !ok {
		t.Fatal("the second endpoint has no price")
	}
	if got := rateOf(t, cheap, catalog.ItemInput); got != "0.000001" {
		t.Errorf("first endpoint input %s, want its own doubled cost", got)
	}
	if got := rateOf(t, dear, catalog.ItemInput); got != "0.000004" {
		t.Errorf("second endpoint input %s, want its own doubled cost", got)
	}
	if got := rateOf(t, sold.Schedule, catalog.ItemInput); got != "0.000004" {
		t.Errorf("model input %s", got)
	}
	cheapCost, err := f.costs[0].converted(usd)
	if err != nil {
		t.Fatal(err)
	}
	cheapFloor, err := cheapCost.Scaled("2", usd)
	if err != nil {
		t.Fatal(err)
	}
	if safe, err := cheap.CoversCost(cheapFloor); err != nil || !safe {
		t.Errorf("the cheap endpoint does not cover its own floor: %v", err)
	}
	// The cheap endpoint is below the model-level floor, which is what per-endpoint flooring exists to allow.
	if safe, err := cheap.CoversCost(sold.Schedule); err != nil {
		t.Fatal(err)
	} else if safe {
		t.Error("the cheap endpoint unexpectedly covers the dearest endpoint's price")
	}
}

func TestWithCostsKeepsOnlySafeEndpoints(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	f.method(plus("0"))
	second := priceCost(t, f.model, "0.000002")
	second.Schedule = mustSchedule(t, `{"v":1,"currency":"XXX","rates":{"input":"0.000002","output":"0.000001"}}`)
	f.costs = append(f.costs, second)
	sold := f.refresh()[f.model]
	// Raise the cheap endpoint's local cost above its published price; the dearest endpoint is still safe.
	f.costs[0].Schedule = mustSchedule(t, `{"v":1,"currency":"XXX","rates":{"input":"0.000001","output":"0.000001"}}`)
	filtered := sold.WithCosts(catalogModel(t, f), f.costs)
	if filtered.Version == uuid.Nil() {
		t.Fatal("one unsafe endpoint withheld the whole model")
	}
	if _, ok := filtered.PriceFor(f.costs[0].Endpoint); ok {
		t.Error("the unsafe endpoint is still priced")
	}
	if _, ok := filtered.PriceFor(second.Endpoint); !ok {
		t.Error("the safe endpoint was dropped")
	}
	if !filtered.Available() {
		t.Error("the model is not available with one safe endpoint")
	}
}

func catalogModel(t *testing.T, f *priceFixture) catalog.Model {
	t.Helper()
	m, ok := f.catalog.snapshot().Model(f.model)
	if !ok {
		t.Fatal("the fixture's model is not in its catalog")
	}
	return m
}
