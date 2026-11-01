package billing

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// reference is a model's reference price in USD, prices as the catalog publishes them.
func reference(t *testing.T, id uuid.UUID, schedule string) catalog.ReferencePrice {
	t.Helper()
	s, err := catalog.ParseSchedule([]byte(schedule))
	if err != nil {
		t.Fatal(err)
	}
	return catalog.ReferencePrice{ID: id, Model: catalog.ModelID(uuid.NewV7()), Currency: "USD", Schedule: s}
}

func multiplied(multiplier string) Method {
	return Method{Kind: "reference_multiplier", Multiplier: &multiplier, CurrencyConversion: "face"}
}

func fixed() Method { return Method{Kind: "fixed", Schedule: jsonx.Raw(fixedUS)} }

// TestFreeModelPublicationStaysZero: a fixed rule, like every other method, must not give a :free model a non-zero
// sale price: the publication is left unpriced instead, and the default audience cannot undercut the promise.
func TestFreeModelPublicationStaysZero(t *testing.T) {
	t.Parallel()
	m := catalog.Model{Slug: "acme/alpha:free"}
	zero, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	costs := []PriceCost{{
		Endpoint: uuid.NewV7(), Model: m.ID, Schedule: zero,
		UnitValue: "1", UnitValueID: uuid.NewV7(), Currency: usd, CurrencyCode: "USD",
	}}
	for _, tc := range []struct {
		name     string
		schedule string
		problem  bool
	}{
		{"a paid fixed rule", fixedUS, true},
		{"a zero fixed rule", `{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`, false},
	} {
		rule := Rule{Enabled: true, Target: Selector{Kind: "all"}, Method: Method{Kind: "fixed", Schedule: jsonx.Raw(tc.schedule)}, ID: uuid.NewV7(), Rev: 1}
		p, err := proposePrice(defaultPricePolicy(), []Rule{rule}, m, nil, usd, costs, fallbacks{})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.problem {
			if p.Problem != ErrFreePrice.Error() || len(p.Schedule) != 0 {
				t.Errorf("%s: problem %q, schedule %s; want the free-price refusal", tc.name, p.Problem, p.Schedule)
			}
			continue
		}
		if p.Problem != "" || len(p.Schedule) == 0 {
			t.Errorf("%s: problem %q, schedule %s; want a published zero price", tc.name, p.Problem, p.Schedule)
		}
	}
}

// TestSoldZeroChecksEveryAudience: a :free model is safe only if no published layer, not just the default, has a
// non-zero price.
func TestSoldZeroChecksEveryAudience(t *testing.T) {
	t.Parallel()
	zero, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	paid, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	s := Sold{Version: uuid.NewV7(), Schedule: zero, Audiences: CompilePrices([]AudiencePrice{{
		Target: Selector{Kind: "all"}, Sold: Sold{Version: uuid.NewV7(), Schedule: paid},
	}})}
	if s.Zero() {
		t.Error("Sold.Zero accepted a non-zero audience layer")
	}
}

// TestSell: a sale price is the reference price times the rule's multiplier, or the rule's own price list, and nothing
// else in M1 gives one. The rounding happens once, against the price the catalog holds, so the same inputs always give
// the same schedule.
func TestSell(t *testing.T) {
	t.Parallel()
	const haiku = `{"v":1,"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":100000,` +
		`"rates":{"input":"0.0000005","output":"0.0000025"}}]},"currency":"USD",` +
		`"rates":{"input":"0.0000001","output":"0.0000005"}}`
	ref := reference(t, uuid.NewV7(), haiku)
	for _, tc := range []struct {
		name    string
		method  Method
		want    string // a fragment of the canonical form
		refusal error
	}{
		{"1.2 up", multiplied("1.2"), `"input":"0.00000012"`, nil},
		{"0.5 down", multiplied("0.5"), `"input":"0.00000005"`, nil},
		{"a millionth", multiplied("0.000001"), `"input":"0.0000000000001"`, nil},
		{"no finite decimal rounds up once", multiplied("0.3333333333333333333"), `"input":"0.000000033333334"`, nil},
		{"a fixed price is the rule's own", fixed(), `"input":"0.000002"`, nil},
		{"cost plus has no price in M1", plus("0.25"), "", ErrUnpriced},
		{"same currency needs no exchange rate", Method{Kind: "reference_multiplier", Multiplier: new("2"), CurrencyConversion: "exchange"}, `"input":"0.0000002"`, nil},
		{"a multiplier read at face without a multiplier", Method{Kind: "reference_multiplier", CurrencyConversion: "face"}, "", ErrUnpriced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := Sell(Rule{Enabled: true, Method: tc.method, ID: uuid.NewV7()}, ref, usd)
			if tc.refusal != nil {
				if !errors.Is(err, tc.refusal) {
					t.Fatalf("Sell = %v, want %v", err, tc.refusal)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := s.MarshalJSON()
			if err != nil || !strings.Contains(string(body), tc.want) || s.Currency() != usd {
				t.Errorf("Sell = %s, %v; want %s in USD", body, err, tc.want)
			}
		})
	}
}

// TestSellIsPure: the same rule and reference price give the same price list, in the site's currency, and the
// reference price itself is left as it was.
func TestSellIsPure(t *testing.T) {
	t.Parallel()
	const published = `{"v":1,"currency":"USD","rates":{"input":"0.0000001","output":"0.0000005"}}`
	ref := reference(t, uuid.NewV7(), published)
	r := Rule{Enabled: true, Method: multiplied("1.2"), ID: uuid.NewV7(), Rev: 3}
	first, err := Sell(r, ref, usd)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Sell(r, ref, usd)
	if err != nil {
		t.Fatal(err)
	}
	a, err := first.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	original, err := ref.Schedule.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Errorf("two sales differ:\n%s\n%s", a, b)
	}
	if want := `"input":"0.0000001"`; !strings.Contains(string(original), want) {
		t.Errorf("the reference price moved: %s", original)
	}
	if !strings.Contains(string(a), `"currency":"USD"`) {
		t.Errorf("the sale is not in the site's currency: %s", a)
	}
}

// TestSellNeedsAReferencePrice: a rule that reads the reference price does not sell a model the catalog publishes none
// for, nor one whose prices are not in the site's currency: face reads the same numbers as the site's, and there is no
// exchange rate in M1 to turn another currency's into them.
func TestSellNeedsAReferencePrice(t *testing.T) {
	t.Parallel()
	r := Rule{Enabled: true, Method: multiplied("1.5"), ID: uuid.NewV7()}
	if _, err := Sell(r, catalog.ReferencePrice{}, usd); !errors.Is(err, ErrUnpriced) {
		t.Errorf("Sell without a reference price = %v, want ErrUnpriced", err)
	}
	other := reference(t, uuid.NewV7(), `{"v":1,"currency":"CNY","rates":{"input":"0.0000007"}}`)
	other.Currency = "CNY"
	if _, err := Sell(r, other, usd); !errors.Is(err, ErrUnpriced) {
		t.Errorf("Sell at face from a price in CNY = %v, want ErrUnpriced", err)
	}
	if _, err := Sell(Rule{Enabled: true, Method: fixed(), ID: uuid.NewV7()}, catalog.ReferencePrice{}, usd); err != nil {
		t.Errorf("a fixed price needs no reference price: %v", err)
	}
}

// TestSellRefusesConstraints: a rule that carries a constraint is refused, whichever member it is and whether or not its
// method would otherwise price: a sale price made from it would ignore the limit.
func TestSellRefusesConstraints(t *testing.T) {
	t.Parallel()
	r := func(s string) *string { return &s }
	ref := reference(t, uuid.NewV7(), `{"v":1,"currency":"USD","rates":{"input":"0.0000001"}}`)
	for name, c := range map[string]Constraints{
		"a minimum margin":    {MinMarginRatio: r("0.1")},
		"a reference floor":   {ReferenceMinRatio: r("0.5")},
		"a reference ceiling": {ReferenceMaxRatio: r("3")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rule := Rule{Enabled: true, Method: fixed(), ID: uuid.NewV7(), Rev: 1, Constraints: c}
			if _, err := Sell(rule, ref, usd); !errors.Is(err, ErrRuleRefuses) {
				t.Errorf("Sell of a rule with %s = %v, want ErrRuleRefuses", name, err)
			}
		})
	}
}

// TestUnpriced: the gate that keeps a model off routing holds its sale price to what no default covers: a token item
// or a service tier the model reports without a price is short, but a known per-use item is not, because admission fills
// in its site's default for it (docs/domains/billing.md「定价」,「价目表缺项」).
func TestUnpriced(t *testing.T) {
	t.Parallel()
	s := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"}}`)
	priced := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","output":"2","file_search":"0.0025"}}`)
	for _, tc := range []struct {
		name string
		r    catalog.Reportable
		want catalog.Reportable
	}{
		{"everything it reports, priced", catalog.Reportable{Items: 1<<in | 1<<catalog.ItemOutput}, catalog.Reportable{}},
		{"a token item it lacks", catalog.Reportable{Items: 1<<in | 1<<catalog.ItemCacheRead}, catalog.Reportable{Items: 1 << catalog.ItemCacheRead}},
		{"a per-use item it lacks is the site's default", catalog.Reportable{Items: 1<<in | 1<<webSearch | 1<<codeExec}, catalog.Reportable{}},
		{"a file search it lacks is not the site's default", catalog.Reportable{Items: 1 << catalog.ItemFileSearch}, catalog.Reportable{Items: 1 << catalog.ItemFileSearch}},
		{"a service tier it does not offer", catalog.Reportable{ServiceTiers: 1 << catalog.ServiceTierPriority}, catalog.Reportable{ServiceTiers: 1 << catalog.ServiceTierPriority}},
		{"reasoning falls back to output", catalog.Reportable{Items: 1 << catalog.ItemReasoning}, catalog.Reportable{}},
		{
			"both short",
			catalog.Reportable{Items: 1 << catalog.ItemAudioIn, ServiceTiers: 1 << catalog.ServiceTierFlex},
			catalog.Reportable{Items: 1 << catalog.ItemAudioIn, ServiceTiers: 1 << catalog.ServiceTierFlex},
		},
	} {
		if got := Unpriced(s, tc.r); got != tc.want {
			t.Errorf("%s: Unpriced = %+v, want %+v", tc.name, got, tc.want)
		}
	}
	if got := Unpriced(priced, catalog.Reportable{Items: 1 << catalog.ItemFileSearch}); got != (catalog.Reportable{}) {
		t.Errorf("a priced file search: Unpriced = %+v, want nothing", got)
	}
}

// TestPlace: a rule that is not stored yet is tried where its target and audience put it, after the rules it ties with,
// so a preview never displaces a stored rule it is no more specific than.
func TestPlace(t *testing.T) {
	t.Parallel()
	all := Rule{Target: Selector{Kind: "all"}, Audience: Selector{Kind: "all"}, ID: uuid.MustParse(model)}
	author := Rule{Target: Selector{Kind: "author", ID: "acme"}, Audience: Selector{Kind: "all"}, ID: uuid.NewV7()}
	modelRule := Rule{Target: Selector{Kind: "model", ID: model}, Audience: Selector{Kind: "all"}, ID: uuid.NewV7()}
	stored := []Rule{modelRule, author, all}
	placed := Place(Rule{Target: Selector{Kind: "author", ID: "other"}, Audience: Selector{Kind: "all"}}, stored)
	var order []string
	for _, r := range placed {
		order = append(order, r.Target.Kind+"/"+r.Target.ID)
	}
	want := []string{"model/" + model, "author/acme", "author/other", "all/"}
	if !slices.Equal(order, want) {
		t.Errorf("a new author rule was placed as %v, want %v", order, want)
	}
}
