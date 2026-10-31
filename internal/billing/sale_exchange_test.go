package billing

import (
	"bytes"
	"fmt"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func exchangeRule(multiplier string) Rule {
	return Rule{ID: uuid.NewV7(), Enabled: true, Method: Method{Kind: "reference_multiplier", CurrencyConversion: "exchange", Multiplier: &multiplier}}
}

func TestSellExchange(t *testing.T) {
	t.Parallel()
	rate := platform.ExchangeRate{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Numerator: 10, Denominator: 71}
	for _, tc := range []struct {
		name, from, to, price, multiplier, want string
		rates                                   []platform.ExchangeRate
	}{
		{"forward", "CNY", "USD", "0.000000000000001", "0.1", "0.000000000000001", []platform.ExchangeRate{rate}},
		{"reverse", "USD", "CNY", "0.000001", "1.5", "0.00001065", []platform.ExchangeRate{rate}},
		{"multiply before rounding", "CNY", "USD", "0.000000000000001", "71", "0.00000000000001", []platform.ExchangeRate{rate}},
		{"same currency", "USD", "USD", "0.000001", "1.5", "0.0000015", nil},
		{"zero", "USD", "CNY", "0", "1", "0", []platform.ExchangeRate{rate}},
		{"missing", "CNY", "USD", "0.000001", "1", "", nil},
		{"wrong pair", "CNY", "EUR", "0.000001", "1", "", []platform.ExchangeRate{rate}},
		{"overflow", "USD", "CNY", "1000", "1", "", []platform.ExchangeRate{rate}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := reference(t, uuid.NewV7(), fmt.Sprintf(`{"v":1,"currency":%q,"rates":{"input":%q}}`, tc.from, tc.price))
			ref.Currency = tc.from
			got, err := Sell(exchangeRule(tc.multiplier), ref, currency(t, tc.to), tc.rates...)
			if tc.want == "" {
				if err == nil {
					t.Fatal("accepted unconvertible price")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := got.MarshalJSON()
			if err != nil || !strings.Contains(string(raw), `"input":"`+tc.want+`"`) {
				t.Fatalf("got %s %v want %s", raw, err, tc.want)
			}
		})
	}
}

func FuzzSaleExchangeRoundTrip(f *testing.F) {
	for seed := range uint64(4) {
		f.Add(seed, ^seed)
	}
	f.Fuzz(func(t *testing.T, a, b uint64) {
		rng := rand.New(rand.NewPCG(a, b)) //nolint:gosec // G404: deterministic property inputs.
		cny := currency(t, "CNY")
		for range 100 {
			checkSaleExchangeRoundTrip(t, rng, cny)
		}
	})
}

func checkSaleExchangeRoundTrip(t *testing.T, rng *rand.Rand, cny platform.Currency) {
	t.Helper()

	n, d, p := rng.Int64N(1000)+1, rng.Int64N(1000)+1, rng.Int64N(1e12)+1
	rate := platform.ExchangeRate{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Numerator: n, Denominator: d}
	ref := reference(t, uuid.NewV7(), fmt.Sprintf(`{"v":1,"currency":"CNY","rates":{"input":%q}}`, big.NewRat(p, 1e15).FloatString(15)))
	ref.Currency = "CNY"
	sold, err := Sell(exchangeRule("1"), ref, usd, rate)
	if err != nil {
		t.Fatal(err)
	}
	q := exchangePriceAtoms(t, sold)
	want := ceilExchange(big.NewInt(p*n), big.NewInt(d))
	if q.Cmp(want) != 0 {
		t.Fatalf("forward %s want %s", q, want)
	}
	ref.Schedule, ref.Currency = sold, "USD"
	back, err := Sell(exchangeRule("1"), ref, cny, rate)
	if err != nil {
		t.Fatal(err)
	}
	delta := exchangePriceAtoms(t, back)
	delta.Sub(delta, big.NewInt(p))
	// The first rounding adds less than one quote atom; the round trip adds at most ceil(d/n) base atoms.
	bound := ceilExchange(big.NewInt(d), big.NewInt(n))
	if delta.Sign() < 0 || delta.Cmp(bound) > 0 {
		t.Fatalf("round trip error %s exceeds %s", delta, bound)
	}
}

func ceilExchange(n, d *big.Int) *big.Int {
	return new(big.Int).Quo(new(big.Int).Add(n, new(big.Int).Sub(d, big.NewInt(1))), d)
}

func exchangePriceAtoms(t *testing.T, s catalog.Schedule) *big.Int {
	t.Helper()
	raw, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Rates map[string]string `json:"rates"`
	}
	if err := jsonx.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	price, ok := new(big.Rat).SetString(value.Rates["input"])
	if !ok {
		t.Fatal("missing input")
	}
	price.Mul(price, big.NewRat(1e15, 1))
	if !price.IsInt() {
		t.Fatal("nonintegral price atoms")
	}
	return price.Num()
}

func TestExchangePublicationAndPreviewFreezeEvidence(t *testing.T) {
	t.Parallel()

	f := exchangePrices(t)
	cny := currency(t, "CNY")
	costs := []PriceCost{priceCost(t, f.model, "0.000000001")}
	costs[0].Currency, costs[0].CurrencyCode = cny, "CNY"
	in := rule(Selector{Kind: "model", ID: f.model.String()}, exchangeRule("1").Method)
	var r Rule
	var rate platform.ExchangeRate
	var published Sold
	err := f.tx(func(tx pg.Tx) error {
		var err error
		r, err = CreateRule(t.Context(), tx, system, f.site, cny, in, "test exchange sale")
		if err != nil {
			return err
		}
		row, err := SetExchangeRate(t.Context(), tx, system, fxInput("71", "10", "initial", 0))
		if err != nil {
			return err
		}
		rate = row.Basis()
		published = checkExchangePublication(t, f, tx, r, cny, costs, rate)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := readExchangeSaleVersion(t, f, published.Version)
	err = f.tx(func(tx pg.Tx) error {
		row, err := SetExchangeRate(t.Context(), tx, system, fxInput("72", "10", "adjustment", 1))
		if err != nil {
			return err
		}
		next := checkExchangePublication(t, f, tx, r, cny, costs, row.Basis())
		if next.Version == published.Version {
			t.Fatal("new rate did not publish a new sale version")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after := readExchangeSaleVersion(t, f, published.Version)
	if !bytes.Equal(before.schedule, after.schedule) || !bytes.Equal(before.basis, after.basis) {
		t.Fatal("stored price version or its evidence mutated after rate update")
	}
	checkStoredExchangeSaleVersion(t, after, rate)
}

func checkExchangePublication(t *testing.T, f *priceFixture, tx pg.Tx, r Rule, cny platform.Currency, costs []PriceCost, rate platform.ExchangeRate) Sold {
	t.Helper()
	preview, err := PreviewPrices(t.Context(), tx, f.site, cny, f.catalog.snapshot(), []Rule{r}, []catalog.ModelID{f.model}, costs, rate)
	if err != nil {
		t.Fatal(err)
	}
	sold, err := RefreshPrices(t.Context(), tx, f.site, cny, f.catalog.snapshot(), costs, rate)
	if err != nil {
		t.Fatal(err)
	}
	published := sold[f.model]
	_, state, err := PriceState(t.Context(), tx, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(state) != 1 || state[0].Candidate != preview[f.model].Candidate {
		t.Fatal("preview and publication differ")
	}
	if published.Version == uuid.Nil() || !strings.Contains(string(state[0].Basis), rate.ID.String()) {
		t.Fatal("missing frozen sale exchange evidence")
	}
	return published
}

func TestExchangeReferenceSelectionAndMissingRate(t *testing.T) {
	t.Parallel()

	f := newSnapshotter(t)
	id := f.model("acme/exchange-selection")
	cny := currency(t, "CNY")
	selected := exchangeRule("1")
	selected.Target = Selector{Kind: "model", ID: id.String()}
	fallback := Rule{ID: uuid.NewV7(), Enabled: true, Target: Selector{Kind: "all"}, Method: Method{Kind: "fixed", Schedule: jsonx.Raw(`{"v":1,"currency":"CNY","rates":{"input":"1","output":"1"}}`)}}
	snap := f.snapshot()
	m, _ := snap.Model(id)
	costs := []PriceCost{priceCost(t, id, "0.000000001")}
	costs[0].Currency, costs[0].CurrencyCode = cny, "CNY"
	p, err := proposePrice(defaultPricePolicy(), []Rule{selected, fallback}, m, snap, cny, costs, fallbacks{})
	if err != nil || len(p.Schedule) != 0 || !strings.Contains(p.Problem, "no exchange rate") || !strings.Contains(string(p.Basis), selected.ID.String()) {
		t.Fatalf("missing rate fell through: %+v %v", p, err)
	}
	// A different foreign reference must not displace the preferred USD source.
	publishExchangeReference(t, f, id, "EUR")
	ref := saleReference(f.snapshot(), id, cny, selected.Method)
	if ref.Currency != "USD" {
		t.Fatalf("preferred foreign source: %s", ref.Currency)
	}
	publishExchangeReference(t, f, id, "CNY")
	ref = saleReference(f.snapshot(), id, cny, selected.Method)
	if ref.Currency != "CNY" {
		t.Fatalf("preferred local source: %s", ref.Currency)
	}
	sold, err := Sell(selected, ref, cny)
	if err != nil || sold.Currency() != cny {
		t.Fatalf("local reference needed FX: %v", err)
	}
}

func publishExchangeReference(t *testing.T, f *snapshotter, id catalog.ModelID, code string) {
	t.Helper()
	_, err := f.cat.PublishPrice(t.Context(), catalog.PublishPriceRequest{
		Actor: system, Key: uuid.NewV7(), Reason: "additional currency reference", Model: id, Source: catalog.SourceManual,
		Schedule: jsonx.Raw(fmt.Sprintf(`{"v":1,"currency":%q,"rates":{"input":"0.000001","output":"0.000002"}}`, code)), Evidence: jsonx.Raw(saleProof),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.cat.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func exchangePrices(t *testing.T) *priceFixture {
	t.Helper()
	f := newPrices(t)
	defaults := SaveFallbacksRequest{Currency: "CNY", Markup: "1.5", PerUse: map[string]string{"code_execution": "0.00000005", "web_search": "0.00000002"}, Reason: "CNY site configuration"}
	defaults.SetActor(system)
	if err := f.tx(func(tx pg.Tx) error { return SaveFallbacks(t.Context(), tx, defaults) }); err != nil {
		t.Fatal(err)
	}
	return f
}

type storedExchangeSaleVersion struct{ schedule, basis []byte }

func readExchangeSaleVersion(t *testing.T, f *priceFixture, version uuid.UUID) storedExchangeSaleVersion {
	t.Helper()
	var stored storedExchangeSaleVersion
	if err := f.tx(func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT schedule,basis FROM billing.price_versions WHERE id=$1", version).Scan(&stored.schedule, &stored.basis)
	}); err != nil {
		t.Fatal(err)
	}
	return stored
}

func checkStoredExchangeSaleVersion(t *testing.T, after storedExchangeSaleVersion, rate platform.ExchangeRate) {
	t.Helper()
	var schedule struct {
		Rates map[string]string `json:"rates"`
	}
	if err := jsonx.Unmarshal(after.schedule, &schedule); err != nil {
		t.Fatal(err)
	}
	if schedule.Rates["input"] != "0.0000071" {
		t.Fatalf("converted price: %s", after.schedule)
	}
	var evidence struct {
		Exchange platform.ExchangeRate `json:"reference_exchange"`
	}
	if err := jsonx.Unmarshal(after.basis, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Exchange != rate {
		t.Fatalf("stored rate changed: %+v, want %+v", evidence.Exchange, rate)
	}
}
