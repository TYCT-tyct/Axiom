package billing

import (
	"errors"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// snapshotter is the catalog's tables and a catalog on them, for the tests that price models from a snapshot.
type snapshotter struct {
	t    *testing.T
	db   *pg.DB
	cat  *catalog.Catalog
	opts int
}

const (
	saleSpec = `{"items":["input","output"],"tiers":[],"context_length":128000}`
	// saleReady is a reference price the sale prices in these tests are made from.
	saleReady  = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"}}`
	saleProof  = `{"url":"https://example.com/pricing","quote":"input $1 per million"}`
	saleReason = "a sale price"
)

func newSnapshotter(t *testing.T) *snapshotter {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t, catalog.Migrations).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	cat, err := catalog.Open(db, pg.PlatformScope(), catalog.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return &snapshotter{t: t, db: db, cat: cat}
}

// model seeds a listed model with a reference price in USD, and returns its ID.
func (f *snapshotter) model(slug string) catalog.ModelID {
	f.t.Helper()
	ctx := f.t.Context()
	op := func() catalog.Op {
		f.opts++
		return catalog.Op{Actor: audit.System("billing.test"), Key: uuid.NewV7(), Reason: saleReason}
	}
	author, _, _ := strings.Cut(slug, "/")
	if _, err := f.cat.SaveAuthor(ctx, catalog.SaveAuthorRequest{Op: op(), Slug: author, Name: author}); err != nil {
		f.t.Fatal(err)
	}
	created, err := f.cat.CreateModel(ctx, catalog.CreateModelRequest{
		Op: op(), Slug: slug, VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: jsonx.Raw(saleSpec),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := uuid.Parse(created.Subject)
	if err != nil {
		f.t.Fatal(err)
	}
	model := catalog.ModelID(id)
	if _, err := f.cat.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: model, Name: slug, Visibility: catalog.VisibilityPublic}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.cat.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: op(), Model: model, Source: catalog.SourceManual, Schedule: jsonx.Raw(saleReady), Evidence: jsonx.Raw(saleProof),
	}); err != nil {
		f.t.Fatal(err)
	}
	var rev int64
	if err := f.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(ctx, "SELECT rev FROM catalog.model WHERE id = $1", id).Scan(&rev)
	}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.cat.ListModel(ctx, catalog.ModelRequest{Op: op(), Model: model, Rev: rev}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.cat.Refresh(ctx); err != nil {
		f.t.Fatal(err)
	}
	return model
}

func (f *snapshotter) snapshot() *catalog.Snapshot {
	f.t.Helper()
	snap, err := f.cat.Current()
	if err != nil {
		f.t.Fatal(err)
	}
	return snap
}

// TestSalePricesTakesTheMostSpecificRule: three rules price the same models, and the one for a model itself wins over
// the one for its author, which wins over the one for everything; a rule that is off, or sets no price in M1's ways,
// leaves the model to the next; a model no rule prices has no sale price at all.
func TestSalePricesTakesTheMostSpecificRule(t *testing.T) {
	t.Parallel()
	f := newSnapshotter(t)
	priced, unbilled := f.model("acme/priced"), f.model("beta/unbilled")
	rules := []Rule{
		{Enabled: true, Target: Selector{Kind: "model", ID: priced.String()}, Audience: Selector{Kind: "all"}, Method: multiplied("3"), ID: uuid.NewV7(), Rev: 1},
		{Enabled: true, Target: Selector{Kind: "author", ID: "acme"}, Audience: Selector{Kind: "all"}, Method: multiplied("2"), ID: uuid.NewV7(), Rev: 1},
		{Enabled: true, Target: Selector{Kind: "all"}, Audience: Selector{Kind: "all"}, Method: multiplied("1.5"), ID: uuid.NewV7(), Rev: 1},
	}
	sold, unpriced := SalePrices(rules, f.snapshot(), []catalog.ModelID{priced, unbilled}, usd)
	if len(unpriced) != 0 {
		t.Fatalf("SalePrices reported %v", unpriced)
	}
	// The reference price is 0.000001 an input token, so each rule's multiplier reads as its own input price.
	if got := text(t, sold, priced); !strings.Contains(got, `"input":"0.000003"`) {
		t.Errorf("the model's rule did not win: %s", got)
	}
	if got := text(t, sold, unbilled); !strings.Contains(got, `"input":"0.0000015"`) {
		t.Errorf("the rule for everything priced the other model as %s, want its 1.5 times", got)
	}
	// A disabled model rule lets the author's through.
	rules[0].Enabled = false
	sold, unpriced = SalePrices(rules, f.snapshot(), []catalog.ModelID{priced, unbilled}, usd)
	if len(unpriced) != 0 {
		t.Fatalf("SalePrices reported %v", unpriced)
	}
	if got := text(t, sold, priced); !strings.Contains(got, `"input":"0.000002"`) {
		t.Errorf("a disabled rule priced the model as %s, want the author's 2 times", got)
	}
}

// TestSalePricesRefusesAStoredConstraint: a rule stored with a constraint that matches a model ends that model's
// pricing: the broader rule after it does not price the model, and the other model is priced alike whichever order the
// two come in. The broader rule's fixed price is cached by rule, so neither that price nor the refusal crosses models.
func TestSalePricesRefusesAStoredConstraint(t *testing.T) {
	t.Parallel()
	f := newSnapshotter(t)
	hit, other := f.model("acme/hit"), f.model("beta/other")
	r := func(s string) *string { return &s }
	constrained := Rule{
		Enabled: true, Target: Selector{Kind: "model", ID: hit.String()}, Audience: Selector{Kind: "all"},
		Method: multiplied("2"), Constraints: Constraints{MinMarginRatio: r("0.1")}, ID: uuid.NewV7(), Rev: 1,
	}
	global := Rule{Enabled: true, Target: Selector{Kind: "all"}, Audience: Selector{Kind: "all"}, Method: fixed(), ID: uuid.NewV7(), Rev: 1}
	rules := []Rule{constrained, global}
	for name, models := range map[string][]catalog.ModelID{
		"the constrained model first": {hit, other},
		"the other model first":       {other, hit},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			checkRefusal(t, rules, f.snapshot(), models, hit, other, constrained.ID)
		})
	}
}

// checkRefusal checks one model order for TestSalePricesRefusesAStoredConstraint.
func checkRefusal(t *testing.T, rules []Rule, snap *catalog.Snapshot, models []catalog.ModelID, hit, other catalog.ModelID, ruleID uuid.UUID) {
	t.Helper()
	sold, unpriced := SalePrices(rules, snap, models, usd)
	if _, ok := sold[hit]; ok {
		t.Error("the model the constrained rule names was sold")
	}
	if got, want := text(t, sold, other), `"input":"0.000002"`; !strings.Contains(got, want) {
		t.Errorf("the other model sold as %s, want the global fixed price containing %s", got, want)
	}
	if len(unpriced) != 1 {
		t.Fatalf("reported %v, want one refusal", unpriced)
	}
	refusal, ok := errors.AsType[*RuleRefusal](unpriced[0])
	if !ok || refusal.Rule != ruleID || refusal.Model != hit {
		t.Errorf("the refusal %v, want rule %s for model %s", unpriced[0], ruleID, hit)
	}
	if !errors.Is(unpriced[0], ErrRuleRefuses) {
		t.Errorf("the refusal is not ErrRuleRefuses: %v", unpriced[0])
	}
}

// TestSalePricesLeavesWhatNoRulePrices: with no rule, or only one whose method sets no price in M1, a model has no sale
// price; it is never sold at its reference price.
func TestSalePricesLeavesWhatNoRulePrices(t *testing.T) {
	t.Parallel()
	f := newSnapshotter(t)
	model := f.model("acme/priced")
	for name, rules := range map[string][]Rule{
		"no rules": nil,
		"a rule that is off": {
			{Enabled: false, Target: Selector{Kind: "all"}, Audience: Selector{Kind: "all"}, Method: multiplied("2"), ID: uuid.NewV7()},
		},
		"cost plus, which M1 does not price": {
			{Enabled: true, Target: Selector{Kind: "all"}, Audience: Selector{Kind: "all"}, Method: plus("0.25"), ID: uuid.NewV7()},
		},
		"an exchange rate, which M1 does not have": {
			{
				Enabled: true, Target: Selector{Kind: "all"}, Audience: Selector{Kind: "all"},
				Method: Method{Kind: "reference_multiplier", Multiplier: new("2"), CurrencyConversion: "exchange"}, ID: uuid.NewV7(),
			},
		},
		"a target that prices no model in M1": {
			{Enabled: true, Target: Selector{Kind: "source_type", ID: "relay"}, Audience: Selector{Kind: "all"}, Method: multiplied("2"), ID: uuid.NewV7()},
		},
		"a multiplier the reference price cannot be scaled by": {
			{Enabled: true, Target: Selector{Kind: "all"}, Audience: Selector{Kind: "all"}, Method: multiplied("1000000000000"), ID: uuid.NewV7()},
		},
	} {
		t.Run(name, func(t *testing.T) {
			sold, unpriced := SalePrices(rules, f.snapshot(), []catalog.ModelID{model}, usd)
			if _, ok := sold[model]; ok {
				t.Errorf("%s gave the model a sale price", name)
			}
			if strings.Contains(name, "cannot be scaled") != (len(unpriced) == 1) {
				t.Errorf("%s reported %v: only a price the catalog cannot hold is reported", name, unpriced)
			}
		})
	}
}

// TestSalePricesPricesOnlyWhatIsAsked: the models the caller names are the ones priced, so a site's whole catalog is
// never walked for a preview of a few models.
func TestSalePricesPricesOnlyWhatIsAsked(t *testing.T) {
	t.Parallel()
	f := newSnapshotter(t)
	asked, unasked := f.model("acme/asked"), f.model("beta/unasked")
	rules := []Rule{
		{Enabled: true, Target: Selector{Kind: "all"}, Audience: Selector{Kind: "all"}, Method: multiplied("2"), ID: uuid.NewV7(), Rev: 1},
	}
	sold, unpriced := SalePrices(rules, f.snapshot(), []catalog.ModelID{asked}, usd)
	if len(unpriced) != 0 {
		t.Fatalf("SalePrices reported %v", unpriced)
	}
	if len(sold) != 1 {
		t.Errorf("sold %d models, want the one asked for: %v", len(sold), sold)
	}
	if _, ok := sold[unasked]; ok {
		t.Error("a model the caller did not ask for was priced")
	}
}

// text is one model's sale price in its canonical form, for a test to look for a price in; empty when it has none.
func text(t *testing.T, sold map[catalog.ModelID]Sold, id catalog.ModelID) string {
	t.Helper()
	s, ok := sold[id]
	if !ok {
		return ""
	}
	body, err := s.Schedule.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
