package billing

import (
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func TestPublicationPreviewIsReadOnly(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	site := uuid.NewV7()
	if err := f.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		got, err := PreviewPrices(t.Context(), tx, site, usd, f.catalog.snapshot(), []Rule{f.rule}, []catalog.ModelID{f.model}, f.costs)
		if err != nil {
			return err
		}
		if len(got[f.model].Schedule) == 0 {
			t.Fatal("read-only preview missing default-policy price")
		}
		var count int
		if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM billing.price_policies WHERE site_id=$1", site).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("preview created a policy")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFloorCapIgnoresDecreases(t *testing.T) {
	t.Parallel()

	previous := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.00001"}}`)
	next := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.0000011","output":"0.000004"}}`)
	if got := automaticPriceStatus(PricePolicy{ChangeRatio: "0.1", MaxIncreaseRatio: "0.5"}, previous, next, next, true); got != "published" {
		t.Fatalf("output decrease blocked a required safe input increase: %s", got)
	}
}

// TestEndpointChangeGateCatchesMovesInsideTheEnvelope covers the case the model-level gate cannot see: one endpoint's
// price moves while the dearest endpoint keeps the model envelope unchanged.
func TestEndpointChangeGateCatchesMovesInsideTheEnvelope(t *testing.T) {
	t.Parallel()

	endpoint := uuid.NewV7()
	previous := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","output":"1"}}`)
	moved := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"2","output":"1"}}`)
	floor := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","output":"1"}}`)
	current := Sold{Version: uuid.NewV7(), Schedule: previous, Endpoints: EndpointPrices{endpoint: previous}}
	policy := PricePolicy{ChangeRatio: "0.1", MaxIncreaseRatio: "1"}
	if got := automaticEndpointPriceStatus(policy, current, EndpointPrices{endpoint: moved}, EndpointPrices{endpoint: floor}, true); got != "pending" {
		t.Fatalf("endpoint move status = %s, want pending", got)
	}
	aboveCap := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"5","output":"1"}}`)
	if got := automaticEndpointPriceStatus(policy, current, EndpointPrices{endpoint: aboveCap}, EndpointPrices{endpoint: floor}, true); got != "pending" {
		t.Fatalf("safe endpoint above cap status = %s, want pending", got)
	}
	floor = moved
	if got := automaticEndpointPriceStatus(policy, current, EndpointPrices{endpoint: aboveCap}, EndpointPrices{endpoint: floor}, true); got != "blocked" {
		t.Fatalf("unsafe endpoint above cap status = %s, want blocked", got)
	}

	a, b := uuid.NewV7(), uuid.NewV7()
	aOld := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"10","output":"1"}}`)
	bOld := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","output":"1"}}`)
	aNew := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"12.5","output":"1"}}`)
	bNew := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"2.5","output":"1"}}`)
	aFloor := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"11","output":"1"}}`)
	bFloor := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","output":"1"}}`)
	mixed := Sold{Version: uuid.NewV7(), Schedule: aOld, Endpoints: EndpointPrices{a: aOld, b: bOld}}
	if got := automaticEndpointPriceStatus(policy, mixed, EndpointPrices{a: aNew, b: bNew}, EndpointPrices{a: aFloor, b: bFloor}, true); got != "pending" {
		t.Fatalf("mixed safe and unsafe endpoints = %s, want pending (the safe endpoint needs confirmation, the unsafe one does not block)", got)
	}
	bNew = mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","output":"1"}}`)
	onlyA := Sold{Version: uuid.NewV7(), Schedule: aOld, Endpoints: EndpointPrices{a: aOld}}
	if got := automaticEndpointPriceStatus(policy, onlyA, EndpointPrices{a: aOld, b: bNew}, EndpointPrices{a: aFloor, b: bFloor}, true); got != "pending" {
		t.Fatalf("new endpoint = %s, want pending rather than stopping existing endpoints", got)
	}
	shared := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"10","output":"1"}}`)
	if got := automaticSharedEndpointPriceStatus(policy, mixed, shared, EndpointPrices{a: aFloor, b: bFloor}); got != "pending" {
		t.Fatalf("switch to a shared price = %s, want pending for the endpoint that moved from 1 to 10", got)
	}
	// A necessary floor raise within the automatic cap must publish even when the proposal returns to a shared price.
	one := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","output":"1"}}`)
	floorRaise := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1.2","output":"1"}}`)
	onlyOne := Sold{Version: uuid.NewV7(), Schedule: one, Endpoints: EndpointPrices{a: one}}
	if got := automaticSharedEndpointPriceStatus(policy, onlyOne, floorRaise, EndpointPrices{a: floorRaise}); got != "published" {
		t.Fatalf("necessary floor raise to a shared price = %s, want published", got)
	}
	// The floor raise on one endpoint must not approve an ordinary move on another in the same shared proposal.
	twoOld := Sold{Version: uuid.NewV7(), Schedule: one, Endpoints: EndpointPrices{a: one, b: one}}
	if got := automaticSharedEndpointPriceStatus(policy, twoOld, floorRaise, EndpointPrices{a: floorRaise, b: one}); got != "pending" {
		t.Fatalf("floor raise plus another endpoint's move = %s, want pending", got)
	}
	// A new endpoint is a new charge in either direction, even when the shared price itself does not move.
	nextFloors := EndpointPrices{a: one, b: one}
	if got := automaticSharedEndpointPriceStatus(policy, onlyOne, one, nextFloors); got != "pending" {
		t.Fatalf("new endpoint behind a shared price = %s, want pending", got)
	}
	sharedCurrent := Sold{Version: uuid.NewV7(), Schedule: one, endpointCosts: EndpointPrices{a: one}}
	if got := automaticEndpointPriceStatus(policy, sharedCurrent, EndpointPrices{a: one, b: one}, EndpointPrices{a: one, b: one}, true); got != "pending" {
		t.Fatalf("new endpoint from a shared price = %s, want pending", got)
	}
	// Without cost evidence an old shared version cannot tell an existing endpoint from a new one; wait rather than
	// publish a new charge silently.
	noEvidence := Sold{Version: uuid.NewV7(), Schedule: one}
	if got := automaticEndpointPriceStatus(policy, noEvidence, EndpointPrices{a: one}, EndpointPrices{a: one}, true); got != "pending" {
		t.Fatalf("endpoint proposal from a shared version without cost evidence = %s, want pending", got)
	}
}

func TestCostPlusIgnoresReferenceRevision(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	model, _ := f.catalog.snapshot().Model(f.model)
	rules := []Rule{f.rule}
	rules[0].Method = plus("0.25")
	before, err := proposePrice(defaultPricePolicy(), rules, model, f.catalog.snapshot(), usd, f.costs, fallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := f.catalog.snapshot().ReferencePrice(f.model, "USD")
	_, err = f.catalog.cat.PublishPrice(t.Context(), catalog.PublishPriceRequest{
		Actor: system, Key: uuid.NewV7(), Reason: "reference-only change", Model: f.model, Base: &ref.ID, Source: catalog.SourceManual,
		Schedule: jsonx.Raw(`{"v":1,"currency":"USD","rates":{"input":"0.000002","output":"0.000004"}}`), Evidence: jsonx.Raw(saleProof),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.catalog.cat.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := proposePrice(defaultPricePolicy(), rules, model, f.catalog.snapshot(), usd, f.costs, fallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if before.Candidate != after.Candidate {
		t.Fatal("unused reference changed cost-plus evidence")
	}
}

func TestPublicationSignalsOnlyOperationalDegradation(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name     string
		critical int
	}{{"no endpoint", 0}, {"no rule", 0}, {"floor cap", 1}} {
		t.Run(c.name, func(t *testing.T) {
			f := signalFixture(t, c.name)
			f.refresh()
			if c.name == "floor cap" {
				f.costs[0].UnitValueID = uuid.NewV7()
			} else {
				f.multiplier("1.1")
			}
			f.refresh()
			var count int
			if err := f.tx(func(tx pg.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='billing.price.review' AND body->>'severity'='critical' AND NOT coalesce((body->>'resolved')::boolean,false)`).Scan(&count)
			}); err != nil {
				t.Fatal(err)
			}
			if count != c.critical {
				t.Fatalf("critical=%d want %d", count, c.critical)
			}
		})
	}
}

func signalFixture(t *testing.T, kind string) *priceFixture {
	t.Helper()
	f := newPrices(t)
	switch kind {
	case "floor cap":
		f.refresh()
		f.reprice(0, "0.0001")
	case "no endpoint":
		f.costs = nil
	case "no rule":
		in := f.rule.RuleInput
		in.Enabled = false
		if err := f.tx(func(tx pg.Tx) (err error) {
			f.rule, err = UpdateRule(t.Context(), tx, system, f.site, usd, f.rule.ID, f.rule.Rev, in, nil, "no active rule", nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestPublishedOverrideOnlyAllowsAuditedLocalCosts(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	f.refresh()
	f.reprice(0, "0.0001")
	f.refresh()
	if err := f.publish(f.state(), true, true); err != nil {
		t.Fatal(err)
	}
	var prices map[catalog.ModelID]Sold
	if err := f.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) (err error) { prices, err = PublishedPrices(t.Context(), tx, f.site); return err }); err != nil {
		t.Fatal(err)
	}
	model, _ := f.catalog.snapshot().Model(f.model)
	if !prices[f.model].AllowsCosts(model, f.costs) {
		t.Fatal("audited local cost was refused")
	}
	f.costs[0].UnitValueID = uuid.NewV7()
	if prices[f.model].AllowsCosts(model, f.costs) {
		t.Fatal("new local cost borrowed an old override")
	}
}

func TestPublishedPricesExpireWhenPublisherStops(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	f.refresh()
	if err := f.tx(func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "UPDATE billing.price_policies SET confirmed_at='epoch' WHERE site_id=$1", f.site)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		sold, err := PublishedPrices(t.Context(), tx, f.site)
		if len(sold) != 0 {
			t.Fatal("stopped publisher left indefinitely serving heads")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
