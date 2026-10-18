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

func fixed(schedule string) Method { return Method{Kind: "fixed", Schedule: jsonx.Raw(schedule)} }

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
		{"a fixed price is the rule's own", fixed(fixedUS), `"input":"0.000002"`, nil},
		{"cost plus has no price in M1", plus("0.25"), "", ErrUnpriced},
		{"an exchange rate has none either", Method{Kind: "reference_multiplier", Multiplier: new("2"), CurrencyConversion: "exchange"}, "", ErrUnpriced},
		{"a multiplier read at face without a multiplier", Method{Kind: "reference_multiplier", CurrencyConversion: "face"}, "", ErrUnpriced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := Sell(Rule{RuleInput: RuleInput{Enabled: true, Method: tc.method}, ID: uuid.NewV7()}, ref, usd)
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
	const cny = `{"v":1,"currency":"USD","rates":{"input":"0.0000001","output":"0.0000005"}}`
	ref := reference(t, uuid.NewV7(), cny)
	r := Rule{RuleInput: RuleInput{Enabled: true, Method: multiplied("1.2")}, ID: uuid.NewV7(), Rev: 3}
	first, err := Sell(r, ref, usd)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Sell(r, ref, usd)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := first.MarshalJSON()
	b, _ := second.MarshalJSON()
	original, _ := ref.Schedule.MarshalJSON()
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

// TestPriceVersion: a sale price's version changes with the rule's rev or the reference price's row, and never with
// anything else, so a stored price is told from a new one by its version alone.
func TestPriceVersion(t *testing.T) {
	t.Parallel()
	ref := reference(t, uuid.NewV7(), `{"v":1,"currency":"USD","rates":{"input":"1"}}`)
	r := Rule{RuleInput: RuleInput{Enabled: true, Method: multiplied("1.2")}, ID: uuid.NewV7(), Rev: 1}
	base := priceVersion(r, ref)
	if again := priceVersion(r, ref); again != base {
		t.Errorf("the version is not stable: %s, %s", base, again)
	}
	r.Rev++
	if next := priceVersion(r, ref); next == base {
		t.Error("a new rev kept the version")
	}
	r.Rev--
	other := reference(t, uuid.NewV7(), `{"v":1,"currency":"USD","rates":{"input":"1"}}`)
	if next := priceVersion(r, other); next == base {
		t.Error("another reference price kept the version")
	}
	if v := base.String()[14]; v != '8' {
		t.Errorf("the version is a version-%c UUID, want 8: %s", v, base)
	}
}

// TestPlace: a rule that is not stored yet is tried where its target and audience put it, after the rules it ties with,
// so a preview never displaces a stored rule it is no more specific than.
func TestPlace(t *testing.T) {
	t.Parallel()
	all := Rule{RuleInput: RuleInput{Target: Selector{Kind: "all"}, Audience: Selector{Kind: "all"}}, ID: uuid.MustParse(model)}
	author := Rule{RuleInput: RuleInput{Target: Selector{Kind: "author", ID: "acme"}, Audience: Selector{Kind: "all"}}, ID: uuid.NewV7()}
	modelRule := Rule{RuleInput: RuleInput{Target: Selector{Kind: "model", ID: model}, Audience: Selector{Kind: "all"}}, ID: uuid.NewV7()}
	stored := []Rule{modelRule, author, all}
	placed := Place(Rule{RuleInput: RuleInput{Target: Selector{Kind: "author", ID: "other"}, Audience: Selector{Kind: "all"}}}, stored)
	var order []string
	for _, r := range placed {
		order = append(order, r.Target.Kind+"/"+r.Target.ID)
	}
	want := []string{"model/" + model, "author/acme", "author/other", "all/"}
	if !slices.Equal(order, want) {
		t.Errorf("a new author rule was placed as %v, want %v", order, want)
	}
}
