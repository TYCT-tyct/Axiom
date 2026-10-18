package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// A routing snapshot is what routing selects from, so what gets into it, and what is left out and why, is the whole of
// this file. The sources are real: the catalog is the module's own over a migrated database, the prices are
// billing.SalePrices over a real rule, and only the suppliers' endpoints are handed in, because what suppliers.Routable
// returns from its first sync is a table this file has no opinion about (suppliers' own tests cover it).

// specAll is a model whose spec declares everything an endpoint needs, with tools and prompt caching: what Claude Code
// and Codex ask for.
const specAll = `{"items":["input","output","cache_read","cache_write"],"tiers":["standard"],` +
	`"context_length":200000,"max_output_tokens":8192,"streams":true,` +
	`"abilities":["tools","images","documents","reasoning","prompt_cache","structured_output","web_search","code_execution"]}`

// specNoServing is a spec that lists what the upstream may report but says nothing about what an endpoint offers.
const specNoServing = `{"items":["input","output"],"tiers":["standard"]}`

const reference = `{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.000015","cache_read":"0.0000003","cache_write":"0.00000375"}}`

// saleRule is the method of a rule for acme's models: 1.5 times the reference price, in the main site's currency.
const saleRule = `{"kind":"reference_multiplier","multiplier":"1.5","currency_conversion":"face"}`

// routeFixture is a catalog on a migrated database with its sale prices, and the router the snapshots go to. The
// suppliers' side, and what billing met but cannot price, are set by each test.
type routeFixture struct {
	t      *testing.T
	db     *pg.DB
	cat    *catalog.Catalog
	router *routing.Router
	prices map[catalog.ModelID]billing.Sold
	items  []billing.UnknownItem

	logs  *strings.Builder
	deps  RouteDeps
	count Count

	served     catalog.ModelID // listed, priced, and its spec declares everything
	nospec     catalog.ModelID // listed and priced, but its spec says nothing of what an endpoint offers
	unpriced   catalog.ModelID // listed with a spec, and no rule prices it
	unlistedID catalog.ModelID // not in the catalog at all
}

func newRouteFixture(t *testing.T) *routeFixture {
	t.Helper()
	database := pgtest.New(t, catalog.Migrations, billing.Migrations())
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	cat, err := catalog.Open(db, pg.PlatformScope(), catalog.Config{MaxStale: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	router, err := routing.New(routing.Config{MaxStale: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	f := &routeFixture{t: t, db: db, cat: cat, router: router, logs: new(strings.Builder)}
	f.served = f.seed("acme/served", specAll)
	f.nospec = f.seed("acme/nospec", specNoServing)
	f.unpriced = f.seed("other/unpriced", specAll)
	f.unlistedID = catalog.ModelID(uuid.NewV7())
	// The rule is inserted directly: billing's own tests cover creating one, and this file is about what a rule
	// already there does to a snapshot.
	f.exec("INSERT INTO billing.pricing_rules (id, site_id, enabled, target_kind, target_id, audience_kind, audience_id, method, constraints) "+
		"VALUES ($1, $2, true, 'author', 'acme', 'all', '', $3::jsonb, '{}'::jsonb)", uuid.NewV7(), uuid.NewV7(), saleRule)
	if err := cat.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.prices, err = f.salePrices(); err != nil {
		t.Fatal(err)
	}
	return f
}

// seed lists and names a model with the spec and a reference price: the catalog lists no model without one.
func (f *routeFixture) seed(slug, spec string) catalog.ModelID {
	f.t.Helper()
	ctx := f.t.Context()
	op := func() catalog.Op {
		return catalog.Op{Actor: audit.System("gateway.test"), Key: uuid.NewV7(), Reason: "test"}
	}
	author, _, _ := strings.Cut(slug, "/")
	if _, err := f.cat.SaveAuthor(ctx, catalog.SaveAuthorRequest{Op: op(), Slug: author, Name: author}); err != nil {
		f.t.Fatal(err)
	}
	made, err := f.cat.CreateModel(ctx, catalog.CreateModelRequest{
		Op: op(), Slug: slug, VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: jsonx.Raw(spec),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := uuid.Parse(made.Subject)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.cat.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: catalog.ModelID(id), Name: slug, Visibility: catalog.VisibilityPublic}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.cat.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: op(), Model: catalog.ModelID(id), Source: catalog.SourceManual, Schedule: jsonx.Raw(reference),
		Evidence: jsonx.Raw(`{"url":"https://example.com/pricing"}`),
	}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.cat.ListModel(ctx, catalog.ModelRequest{Op: op(), Model: catalog.ModelID(id), Rev: 0}); err != nil {
		f.t.Fatal(err)
	}
	return catalog.ModelID(id)
}

func (f *routeFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if err := f.db.Tx(f.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(f.t.Context(), sql, args...)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
}

// salePrices is billing.SalePrices over the rules of every site: the fixture has one site's rule, which prices every
// model, and billing prices a model once however many sites there are.
func (f *routeFixture) salePrices() (map[catalog.ModelID]billing.Sold, error) {
	var rules []billing.Rule
	var site uuid.UUID
	if err := f.db.Tx(f.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(f.t.Context(), "SELECT site_id FROM billing.pricing_rules LIMIT 1").Scan(&site)
	}); err != nil {
		return nil, err
	}
	if err := f.db.Tx(f.t.Context(), pg.PlatformScope(), func(tx pg.Tx) (err error) {
		rules, err = billing.Rules(f.t.Context(), tx, site)
		return err
	}); err != nil {
		return nil, err
	}
	snap, err := f.cat.Current()
	if err != nil {
		return nil, err
	}
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		return nil, err
	}
	return billing.SalePrices(rules, snap, usd)
}

// routable is one suppliers.Routable of a model on a supplier, as the sync writes them.
func routable(model catalog.ModelID, formats ...protocol.Format) suppliers.Routable {
	return suppliers.Routable{
		ID: uuid.NewV7(), Supplier: uuid.NewV7(), Model: uuid.UUID(model), Upstream: "upstream-name",
		Formats: formats, Streams: true, Cancels: true,
	}
}

// build runs RouteDeps.Build over the endpoints and the unknown items the test gives.
func (f *routeFixture) build(endpoints []suppliers.Routable) *routing.Snapshot {
	f.t.Helper()
	f.deps = RouteDeps{
		Endpoints:    func(context.Context) ([]suppliers.Routable, error) { return endpoints, nil },
		Catalog:      f.cat,
		Prices:       func(context.Context) (map[catalog.ModelID]billing.Sold, error) { return f.prices, nil },
		UnknownItems: func(context.Context) ([]billing.UnknownItem, error) { return f.items, nil },
		Router:       f.router,
		Logger:       slog.New(slog.NewTextHandler(f.logs, nil)),
	}
	snap, count, err := f.deps.Build(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	f.count = count
	f.deps.Publish(snap, count)
	return snap
}

// planned is the endpoints routing plans to for a request.
func (f *routeFixture) planned(in routing.Input) []uuid.UUID {
	f.t.Helper()
	plan, wait, err := f.router.Plan(in)
	if err != nil || wait != nil {
		f.t.Fatalf("Plan(%s) = %v, %v", in.Req.Model, wait, err)
	}
	out := make([]uuid.UUID, len(plan.Entries))
	for i, e := range plan.Entries {
		out[i] = uuid.UUID(e.Endpoint)
	}
	return out
}

// call is a plan request for a small streaming Messages call of the model, at any service tier the catalog's models
// have, with the changes the test makes to it.
func call(model catalog.ModelID, edits ...func(*protocol.Request)) routing.Input {
	req := &protocol.Request{
		Format: protocol.Messages, Model: "m", Stream: true, Choices: 1, TextBytes: 100,
		ServiceTiers: 1 << catalog.ServiceTierStandard,
	}
	for _, edit := range edits {
		edit(req)
	}
	return routing.Input{Req: req, Models: []catalog.ModelID{model}}
}

// TestRouteBuildMapsWhatRoutableGives: an endpoint of a listed, priced model whose spec declares everything is in the
// snapshot with the protocol, limits and abilities its spec and its supplier's formats say, priced at the sale price,
// under the one zero-ID preset, and routing plans with it.
func TestRouteBuildMapsWhatRoutableGives(t *testing.T) {
	f := newRouteFixture(t)
	r := routable(f.served, protocol.Messages, protocol.Responses)
	snap := f.build([]suppliers.Routable{r})
	if want := (Count{Endpoints: 1, Offered: 1}); f.count != want {
		t.Errorf("the build made %+v, want %+v", f.count, want)
	}
	if got := f.planned(call(f.served)); len(got) != 1 || got[0] != r.ID {
		t.Fatalf("routing plans to %v, want the endpoint %s", got, r.ID)
	}
	plan, _, err := f.router.Plan(call(f.served))
	if err != nil {
		t.Fatal(err)
	}
	f.checkEntry(r, plan.Entries[0])
	if snap == nil {
		t.Error("no snapshot")
	}
}

// checkEntry: what the snapshot holds of the endpoint is read back through the router's own view: Plan carries only
// what a reservation and an attempt need, so the rest is checked where it is written, in endpointOf, by the fields that
// reach the plan.
func (f *routeFixture) checkEntry(r suppliers.Routable, built routing.Entry) {
	f.t.Helper()
	t := f.t
	if built.Limits.ContextLength != 200000 || built.MaxOutput != 8192 {
		t.Errorf("context %d, max output %d; want the spec's 200000 and 8192", built.Limits.ContextLength, built.MaxOutput)
	}
	if built.Price.Version != f.prices[f.served].Version {
		t.Errorf("price version %s, want the sale price's %s", built.Price.Version, f.prices[f.served].Version)
	}
	if built.Segmentable {
		t.Error("the endpoint is segmentable, though nothing measures its output speed: the whole output limit is reserved")
	}
	if got := uuid.UUID(built.Supplier); got != r.Supplier {
		t.Errorf("the plan's supplier is %s, want %s", got, r.Supplier)
	}
	if built.Target.Model != "upstream-name" || !built.Target.Streams {
		t.Errorf("target %+v, want the supplier's upstream name and streams", built.Target)
	}
}

// TestRouteBuildHoldsWhatRoutingFiltersBy: the parts of an endpoint that routing filters on are in the snapshot as the
// spec and the supplier's formats say — the abilities, the native and served formats, the service tiers — so a call
// that needs one of them still finds the endpoint, and a stream the supplier's policy cannot pass finds none.
func TestRouteBuildHoldsWhatRoutingFiltersBy(t *testing.T) {
	f := newRouteFixture(t)
	f.build([]suppliers.Routable{routable(f.served, protocol.Messages, protocol.Responses)})
	if got := f.planned(call(f.served, func(r *protocol.Request) { r.Needs = protocol.Tools })); len(got) != 1 {
		t.Fatalf("a call that needs tools was planned to %d endpoints, want the one whose spec declares tools", len(got))
	}
	if got := f.planned(call(f.served, func(r *protocol.Request) { r.Format = protocol.Chat })); len(got) != 1 {
		t.Errorf("a Chat call was planned to %d endpoints, want the one serving Chat by conversion", len(got))
	}
	// A stream is what a Claude Code or Codex turn is: an endpoint whose supplier cannot pass one must not be planned to.
	f.build([]suppliers.Routable{withStreams(routable(f.served, protocol.Messages), false)})
	if _, _, err := f.router.Plan(call(f.served)); err == nil {
		t.Error("a stream was planned to an endpoint whose supplier's policy passes no stream")
	}
}

// withStreams is the endpoint as a supplier whose policy passes no stream.
func withStreams(r suppliers.Routable, streams bool) suppliers.Routable {
	r.Streams = streams
	return r
}

// TestRouteBuildLeavesOutWhatItCannotDescribe: a model whose spec says nothing of what an endpoint offers, a model no
// rule prices, and an endpoint of a model the catalog does not hold are all left out, the log says which and why, and
// the snapshot still holds the one model that can be routed. Nothing is guessed,
// and a model that cannot be routed leaves a line an administrator can act on.
func TestRouteBuildLeavesOutWhatItCannotDescribe(t *testing.T) {
	f := newRouteFixture(t)
	f.build([]suppliers.Routable{
		routable(f.served, protocol.Messages),
		routable(f.nospec, protocol.Messages),
		routable(f.unpriced, protocol.Messages),
		routable(f.unlistedID, protocol.Messages),
	})
	if want := (Count{Endpoints: 1, Offered: 4}); f.count != want {
		t.Errorf("the build made %+v, want %+v", f.count, want)
	}
	logs := f.logs.String()
	for _, want := range []struct {
		model  catalog.ModelID
		reason string
	}{
		{f.nospec, "no context length, output limit, streaming or abilities"},
		{f.unpriced, "no pricing rule gives it a sale price"},
		{f.unlistedID, "the catalog has no such model"},
	} {
		model := uuid.UUID(want.model).String()
		if !strings.Contains(logs, model) || !strings.Contains(logs, want.reason) {
			t.Errorf("the log does not say %s is left out for %q:\n%s", model, want.reason, logs)
		}
	}
	if strings.Contains(logs, uuid.UUID(f.served).String()) {
		t.Errorf("the log names the model that was routed:\n%s", logs)
	}
}

// TestRouteBuildCountsWhatSuppliersOffer: a node whose suppliers offer nothing may serve, and one whose suppliers offer
// only endpoints that cannot be routed may not: it would answer every call 404 no_endpoint with the reason only in the
// log. That is what readiness is decided on (cmd/axiom's announceReady).
func TestRouteBuildCountsWhatSuppliersOffer(t *testing.T) {
	f := newRouteFixture(t)
	f.build(nil)
	if !f.count.Routable() || f.count.Endpoints != 0 || f.count.Offered != 0 {
		t.Errorf("%+v: a node with no endpoint yet is routable", f.count)
	}
	f.build([]suppliers.Routable{routable(f.unpriced, protocol.Messages)})
	if f.count.Routable() || f.count.Endpoints != 0 || f.count.Offered != 1 {
		t.Errorf("%+v: a node whose only endpoint has no sale price is not routable", f.count)
	}
	f.build([]suppliers.Routable{routable(f.served, protocol.Messages)})
	if !f.count.Routable() {
		t.Errorf("%+v: a node with a priced endpoint is routable", f.count)
	}
}

// TestRouteBuildFlagsAModelThatReportedAnUnpricedItem: an endpoint whose model a usage report put an unpriced item on
// stays in the snapshot, flagged, so routing refuses it with the reason instead of the node pretending the model is
// fine (docs/domains/billing.md「价目表缺项」).
func TestRouteBuildFlagsAModelThatReportedAnUnpricedItem(t *testing.T) {
	f := newRouteFixture(t)
	f.items = []billing.UnknownItem{{Model: f.served, Field: "web_search", Request: uuid.NewV7()}}
	snap := f.build([]suppliers.Routable{routable(f.served, protocol.Messages)})
	if f.count.Endpoints != 1 {
		t.Errorf("%+v: the flagged endpoint was left out", f.count)
	}
	if _, _, err := f.router.Plan(call(f.served)); err == nil {
		t.Errorf("routing planned to a model the price list cannot price: %v", err)
	}
	if snap == nil {
		t.Error("no snapshot")
	}
}

// TestRouteBuildPublishesOnlyNewerSnapshots: the router refuses a snapshot built before the one it holds, so a rebuild
// that finished late never replaces a newer view of the world.
func TestRouteBuildPublishesOnlyNewerSnapshots(t *testing.T) {
	f := newRouteFixture(t)
	a, b := routable(f.served, protocol.Messages), routable(f.served, protocol.Messages)
	older := f.build([]suppliers.Routable{a})
	newer := f.build([]suppliers.Routable{a, b})
	f.deps.Publish(older, Count{Endpoints: 1, Offered: 1}) // late, and older: refused
	if got := f.planned(call(f.served)); len(got) != 2 {
		t.Errorf("routing plans to %d endpoints, want the newer snapshot's 2", len(got))
	}
	if newer == nil || older == nil {
		t.Fatal("no snapshot")
	}
}

// TestRouteBuildReportsAFailedSource: a source that cannot be read is an error, not an empty snapshot: the node fails
// its start on it (cmd/axiom) and a rebuild keeps the last snapshot it had.
func TestRouteBuildReportsAFailedSource(t *testing.T) {
	f := newRouteFixture(t)
	boom := fmt.Errorf("the database is gone")
	for name, set := range map[string]func(*RouteDeps){
		"the endpoints": func(d *RouteDeps) {
			d.Endpoints = func(context.Context) ([]suppliers.Routable, error) { return nil, boom }
		},
		"the sale prices": func(d *RouteDeps) {
			d.Prices = func(context.Context) (map[catalog.ModelID]billing.Sold, error) { return nil, boom }
		},
		"the unknown billing items": func(d *RouteDeps) {
			d.UnknownItems = func(context.Context) ([]billing.UnknownItem, error) { return nil, boom }
		},
	} {
		d := RouteDeps{
			Endpoints: func(context.Context) ([]suppliers.Routable, error) { return nil, nil },
			Catalog:   f.cat,
			Prices: func(context.Context) (map[catalog.ModelID]billing.Sold, error) {
				return map[catalog.ModelID]billing.Sold{}, nil
			},
			UnknownItems: func(context.Context) ([]billing.UnknownItem, error) { return nil, nil },
			Router:       f.router, Logger: slog.New(slog.DiscardHandler),
		}
		set(&d)
		if _, _, err := d.Build(t.Context()); err == nil || !strings.Contains(err.Error(), boom.Error()) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v, want it named and the cause kept", name, err)
		}
	}
}
