package billing

import (
	"errors"
	"math/big"
	"math/rand/v2"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func priceCost(t testing.TB, model catalog.ModelID, price string) PriceCost {
	return PriceCost{Endpoint: uuid.NewV7(), Model: model, Schedule: mustSchedule(t, `{"v":1,"currency":"XXX","rates":{"input":"`+price+`","output":"0.000001"}}`), UnitValue: "1", UnitValueID: uuid.NewV7(), Currency: usd, CurrencyCode: "USD"}
}

type priceFixture struct {
	rules
	catalog *snapshotter
	model   catalog.ModelID
	costs   []PriceCost
	rule    Rule
}

func newPrices(t testing.TB) *priceFixture {
	f := &priceFixture{rules: newRules(t), catalog: newSnapshotter(t)}
	f.model = f.catalog.model("acme/price-test")
	f.costs = []PriceCost{priceCost(t, f.model, "0.0000005")}
	var err error
	f.rule, err = f.create(rule(Selector{Kind: "all"}, faced("1")))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *priceFixture) refresh() map[catalog.ModelID]Sold {
	f.t.Helper()
	var sold map[catalog.ModelID]Sold
	snap := f.catalog.snapshot()
	err := f.tx(func(tx pg.Tx) (err error) {
		sold, err = RefreshPrices(f.t.Context(), tx, f.site, usd, snap, f.costs)
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return sold
}

func (f *priceFixture) state() Publication {
	f.t.Helper()
	var rows []Publication
	if err := f.tx(func(tx pg.Tx) (err error) { _, rows, err = PriceState(f.t.Context(), tx, f.site); return err }); err != nil {
		f.t.Fatal(err)
	}
	if len(rows) != 1 {
		f.t.Fatalf("publications: %v", rows)
	}
	return rows[0]
}

func (f *priceFixture) multiplier(value string) {
	f.t.Helper()
	in := f.rule.RuleInput
	in.Method = faced(value)
	if err := f.tx(func(tx pg.Tx) (err error) {
		f.rule, err = UpdateRule(f.t.Context(), tx, system, f.site, usd, f.rule.ID, f.rule.Rev, in, "price test", nil)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *priceFixture) publish(p Publication, confirmed, override bool) error {
	return f.tx(func(tx pg.Tx) error {
		_, err := PublishPrice(f.t.Context(), tx, system, f.site, f.model, p.Candidate, p.Current, confirmed, override, "explicit decision")
		return err
	})
}

func TestPricePublicationHoldsLargeMoves(t *testing.T) {
	f := newPrices(t)
	first := f.refresh()[f.model]
	if first.Version == uuid.Nil() {
		t.Fatal("initial safe price missing")
	}
	if again := f.refresh()[f.model]; again.Version != first.Version {
		t.Fatal("unchanged refresh published another version")
	}
	f.multiplier("1.1")
	small := f.refresh()[f.model]
	if small.Version == first.Version {
		t.Fatal("inclusive threshold blocked a 10% move")
	}
	f.multiplier("2")
	held := f.refresh()[f.model]
	p := f.state()
	if p.Status != "pending" || held.Version != small.Version {
		t.Fatalf("large move applied: %+v", p)
	}
	if err := f.publish(p, false, false); err == nil {
		t.Fatal("unconfirmed publication accepted")
	}
	if err := f.publish(p, true, false); err != nil {
		t.Fatal(err)
	}
	next := f.refresh()[f.model]
	if next.Version == held.Version {
		t.Fatal("confirmation did not publish")
	}
	if err := f.publish(p, true, false); !errors.Is(err, ErrStalePrice) {
		t.Fatalf("stale head accepted: %v", err)
	}
	// A cost/configuration change invalidates the administrator's candidate as well.
	f.multiplier("3")
	f.refresh()
	stale := f.state()
	f.costs[0].UnitValueID = uuid.NewV7()
	f.refresh()
	if err := f.publish(stale, true, false); !errors.Is(err, ErrStalePrice) {
		t.Fatalf("stale candidate: %v", err)
	}
}

func TestFloorPublicationAndScopedOverride(t *testing.T) {
	f := newPrices(t)
	first := f.refresh()[f.model]
	f.costs[0] = priceCost(t, f.model, "0.0000015")
	raised := f.refresh()[f.model]
	if raised.Version == uuid.Nil() || raised.Version == first.Version {
		t.Fatal("floor increase waited for ordinary threshold")
	}
	cost, err := CostEnvelope(f.costs, usd)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := raised.Schedule.CoversCost(cost); !ok || err != nil {
		t.Fatalf("below floor: %v", err)
	}
	f.costs[0] = priceCost(t, f.model, "0.00001")
	if _, ok := f.refresh()[f.model]; ok || f.state().Status != "blocked" {
		t.Fatal("unsafe old price retained beyond automatic cap")
	}
	p := f.state()
	if err := f.publish(p, true, true); err != nil {
		t.Fatal(err)
	}
	overridden := f.refresh()[f.model]
	if ok, err := overridden.Schedule.CoversCost(mustSchedule(t, string(p.Floor))); err != nil || ok {
		t.Fatal("explicit exception did not use requested price")
	}
	checkPriceOverrideAudit(t, f)
	f.costs[0].UnitValueID = uuid.NewV7()
	if _, ok := f.refresh()[f.model]; ok {
		t.Fatal("exception survived changed cost evidence")
	}
}

func TestPublicationIsAtomicAndConcurrent(t *testing.T) {
	f := newPrices(t)
	f.refresh()
	f.multiplier("2")
	f.refresh()
	p := f.state()
	rollback := errors.New("rollback")
	if err := f.tx(func(tx pg.Tx) error {
		if _, err := PublishPrice(t.Context(), tx, system, f.site, f.model, p.Candidate, p.Current, true, true, "rollback"); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if got := f.state(); *got.Current != *p.Current {
		t.Fatal("rolled-back publication escaped")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { results <- f.publish(p, true, false) })
	}
	wg.Wait()
	close(results)
	successes, stale := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrStalePrice) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("concurrent publication: %d success %d stale", successes, stale)
	}
}

func TestCostPlusAndUnknownCosts(t *testing.T) {
	f := newPrices(t)
	in := f.rule.RuleInput
	in.Method = plus("0.25")
	if err := f.tx(func(tx pg.Tx) (err error) {
		f.rule, err = UpdateRule(t.Context(), tx, system, f.site, usd, f.rule.ID, f.rule.Rev, in, "cost plus", nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sold := f.refresh()[f.model]
	p, _ := sold.Schedule.BaseRate(catalog.ItemInput)
	if p.String() != "0.000000625" {
		t.Fatalf("cost plus = %s", p)
	}
	f.costs[0].Exchange = platform.ExchangeRate{}
	f.costs[0].Currency = currencyOfPriceTest(t, "CNY")
	if _, ok := f.refresh()[f.model]; ok {
		t.Fatal("missing exchange sold at a guessed cost")
	}
}

func TestNeverPublishBelowFloorProperty(t *testing.T) {
	f := newPrices(t)
	if err := f.tx(func(tx pg.Tx) error {
		return SetPricePolicy(t.Context(), tx, system, f.site, PricePolicy{FloorMultiplier: "1.2", ChangeRatio: "0.1", MaxIncreaseRatio: "1"}, "property floor")
	}); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(234, 319)) //nolint:gosec // reproducible property-test input, not a secret
	publishedCount := 0
	for range 80 {
		n := rng.IntN(50) + 1
		f.costs[0] = priceCost(t, f.model, new(big.Rat).SetFrac64(int64(n), 10000000).FloatString(7))
		sold, ok := f.refresh()[f.model]
		if !ok {
			continue
		}
		publishedCount++
		cost, err := CostEnvelope(f.costs, usd)
		if err != nil {
			t.Fatal(err)
		}
		cost, err = cost.Scaled("1.2", usd)
		if err != nil {
			t.Fatal(err)
		}
		if safe, err := sold.Schedule.CoversCost(cost); !safe || err != nil {
			t.Fatalf("sold below known cost: %v", err)
		}
	}
	if publishedCount == 0 {
		t.Fatal("property did not exercise a published price")
	}
}

func TestPricePolicyValidationAndRevision(t *testing.T) {
	f := newPrices(t)
	for _, p := range []PricePolicy{{FloorMultiplier: "0.9", ChangeRatio: "0.1", MaxIncreaseRatio: "1"}, {FloorMultiplier: "1", ChangeRatio: "-1", MaxIncreaseRatio: "1"}} {
		if err := f.tx(func(tx pg.Tx) error { return SetPricePolicy(t.Context(), tx, system, f.site, p, "invalid") }); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
	p := PricePolicy{FloorMultiplier: "1.2", ChangeRatio: "0.1", MaxIncreaseRatio: "1"}
	if err := f.tx(func(tx pg.Tx) error { return SetPricePolicy(t.Context(), tx, system, f.site, p, "margin floor") }); err != nil {
		t.Fatal(err)
	}
	if err := f.tx(func(tx pg.Tx) error { return SetPricePolicy(t.Context(), tx, system, f.site, p, "stale") }); !errors.Is(err, ErrStalePrice) {
		t.Fatal(err)
	}
	f.refresh()
	state := f.state()
	var basis map[string]any
	if err := jsonx.Unmarshal(state.Basis, &basis); err != nil {
		t.Fatal(err)
	}
}

func TestReservationLocksPriceVersionsThroughRetry(t *testing.T) {
	b := newBank(t, 10000, 0, Config{})
	first, second := uuid.NewV7(), uuid.NewV7()
	q := []Quotable{b.exact, b.exact}
	q[0].Version = first
	q[1].Version = second
	var h *Hold
	if err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) (err error) {
		h, _, err = b.billing.Reserve(t.Context(), tx, b.admitted, uuid.NewV7(), Demand{TextBytes: 100}, q)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	out := outcome(Committed, FinishStop, used(t, 10, 0, 0))
	out.Attempts[0].Entry = 1
	for range 2 {
		final, err := b.billing.Settle(t.Context(), h, out)
		if err != nil {
			t.Fatal(err)
		}
		if final.PriceVersion == nil || *final.PriceVersion != second {
			t.Fatalf("settled version %v, want second route entry %s", final.PriceVersion, second)
		}
		assertChargeVersion(t, b, h.id, second)
	}
	q[1].Version = uuid.NewV7()
	if err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		var ids []uuid.UUID
		if err := tx.QueryRow(t.Context(), "SELECT price_versions FROM billing.reservations WHERE request_id=$1", h.id).Scan(&ids); err != nil {
			return err
		}
		if len(ids) != 2 || ids[0] != first || ids[1] != second {
			t.Fatalf("locked versions: %v", ids)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCostEnvelopeKeepsExactExchange(t *testing.T) {
	c := priceCost(t, catalog.ModelID(uuid.NewV7()), "0.000000075")
	c.UnitValue = "0.123456789"
	c.Currency = currencyOfPriceTest(t, "CNY")
	c.Exchange = platform.ExchangeRate{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Numerator: 10, Denominator: 71}
	schedule, err := CostEnvelope([]PriceCost{c}, usd)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := schedule.BaseRate(catalog.ItemInput)
	exact := new(big.Rat).Mul(mustUnitPrice("0.000000075").Rat(), mustUnitPrice(c.UnitValue).Rat())
	exact.Mul(exact, big.NewRat(10, 71))
	diff := new(big.Rat).Sub(got.Rat(), exact)
	if diff.Sign() < 0 || diff.Cmp(big.NewRat(1, 1000000000000000)) >= 0 {
		t.Fatalf("exchange rounding %s differs by %s", got, diff)
	}
}

func checkPriceOverrideAudit(t *testing.T, f *priceFixture) {
	t.Helper()
	var overrides, audits int
	if err := f.tx(func(tx pg.Tx) error {
		if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM billing.price_versions WHERE overridden").Scan(&overrides); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM platform.audit_log WHERE action='price.publish' AND detail->>'overridden'='true'").Scan(&audits)
	}); err != nil {
		t.Fatal(err)
	}
	if overrides != 1 || audits != 1 {
		t.Fatalf("exception/audit = %d/%d", overrides, audits)
	}
}

func currencyOfPriceTest(t *testing.T, s string) platform.Currency {
	t.Helper()
	c, err := platform.ParseCurrency(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPricePolicyAuditIdentifiesSite(t *testing.T) {
	f := newPrices(t)
	if err := f.tx(func(tx pg.Tx) error {
		p := PricePolicy{FloorMultiplier: "1.2", ChangeRatio: "0.1", MaxIncreaseRatio: "1"}
		return SetPricePolicy(t.Context(), tx, system, f.site, p, "site policy")
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.tx(func(tx pg.Tx) error {
		var target string
		var rev int
		if err := tx.QueryRow(t.Context(), "SELECT target_id, (detail->'after'->>'rev')::integer FROM platform.audit_log WHERE action='price_policy.update' AND target_kind='price_policy'").Scan(&target, &rev); err != nil {
			return err
		}
		if target != f.site.String() || rev != 1 {
			t.Fatalf("policy audit target/revision: %s/%d", target, rev)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCostPlusMarginAndMarkup(t *testing.T) {
	cost := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"},"service_tiers":{"flex":{"rates":{"input":"0.000002","output":"0.000004"}}}}`)
	price, err := costPrice(Method{Kind: "cost_plus", MarginRatio: new("0.2"), FixedMarkup: jsonx.Raw(`{"output":"0.000001"}`)}, cost, usd)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		tier catalog.ServiceTier
		want string
	}{{catalog.ServiceTierStandard, "0.0000035"}, {catalog.ServiceTierFlex, "0.000006"}} {
		u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemReasoning: 1}, c.tier, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, missing, err := price.Cost(u)
		if err != nil || missing != 0 || got.Cmp(mustUnitPrice(c.want).Mul(1)) != 0 {
			t.Fatalf("margin + fixed markup at %s: %v missing %v err %v", c.tier, got, missing, err)
		}
	}
}
