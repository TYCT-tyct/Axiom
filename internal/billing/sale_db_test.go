package billing

import (
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

// TestProposalsSelectRules exercises the production proposal path, including cost-plus and refusals.
func TestProposalsSelectRules(t *testing.T) {
	f := newSnapshotter(t)
	id := f.model("acme/priced")
	snap := f.snapshot()
	model, _ := snap.Model(id)
	makeRule := func(target Selector, method Method) Rule {
		return Rule{Enabled: true, Target: target, Audience: Selector{Kind: "all"}, Method: method, ID: uuid.NewV7(), Rev: 1}
	}
	specific := makeRule(Selector{Kind: "model", ID: id.String()}, multiplied("3"))
	broad := makeRule(Selector{Kind: "all"}, multiplied("1.5"))
	author := makeRule(Selector{Kind: "author", ID: "acme"}, multiplied("2"))
	disabled := specific
	disabled.Enabled = false
	constrained := specific
	constrained.Constraints.MinMarginRatio = new("0.1")
	for _, c := range []struct {
		name    string
		rules   []Rule
		input   string
		problem bool
	}{
		{"specific", []Rule{specific, author, broad}, "0.000003", false},
		{"disabled", []Rule{disabled, author, broad}, "0.000002", false},
		{"no rules", nil, "", true},
		{"constraint refuses fallback", []Rule{constrained, broad}, "", true},
		{"cost plus", []Rule{makeRule(Selector{Kind: "all"}, plus("0.25")), broad}, "0.000000625", false},
		{"overflow", []Rule{makeRule(Selector{Kind: "all"}, multiplied("1000000000000")), broad}, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := proposePrice(defaultPricePolicy(), c.rules, model, snap, usd, []PriceCost{priceCost(t, id, "0.0000005")}, fallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			assertProposalPrice(t, p, c.input, c.problem)
		})
	}
}

func assertProposalPrice(t *testing.T, p Publication, want string, problem bool) {
	t.Helper()
	if (p.Problem != "") != problem {
		t.Fatalf("problem=%s", p.Problem)
	}
	if problem {
		if len(p.Schedule) != 0 {
			t.Fatal("invalid proposal was priced")
		}
		return
	}
	schedule := mustSchedule(t, string(p.Schedule))
	input, _ := schedule.BaseRate(catalog.ItemInput)
	if input.String() != want {
		t.Fatalf("input=%s want %s", input, want)
	}
}
