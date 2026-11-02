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
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// A routing snapshot is what routing selects from, so what gets into it, and what is left out and why, is the whole of
// this file. The sources are real: the catalog is the module's own over a migrated database, the prices are
// explicit fixture prices using billing.Sell, and only the suppliers' endpoints are handed in, because what suppliers.Routable
// returns from its first sync is a table this file has no opinion about (suppliers' own tests cover it).

// specAll is a model whose spec declares everything an endpoint needs, with tools and prompt caching: what Claude Code
// and Codex ask for.
const specAll = `{"items":["input","output","cache_read","cache_write"],"tiers":["standard"],` +
	`"context_length":200000,"max_output_tokens":8192,"streams":true,` +
	`"abilities":["tools","images","documents","reasoning","prompt_cache","structured_output","web_search","code_execution"]}`

// specNoServing is a spec that lists what the upstream may report but says nothing about what an endpoint offers.
const specNoServing = `{"items":["input","output"],"tiers":["standard"]}`

// specSearch is a spec whose upstream may report a per-use item: a sale price need not price it, because every currency
// has a default price for it (docs/domains/billing.md「价目表缺项」).
const specSearch = `{"items":["input","output","web_search"],"tiers":["standard"],` +
	`"context_length":200000,"max_output_tokens":8192,"streams":true,"abilities":["tools","web_search"]}`

// reference is a model's reference price in USD, prices as the catalog publishes them.
const reference = `{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.000015","cache_read":"0.0000003","cache_write":"0.00000375"}}`

// searchReference prices specSearch's items too: the catalog lists no model whose reference price leaves one out.
const searchReference = `{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.000015","web_search":"0.01"}}`

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
	health *quality.Monitor
	models *ModelList

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
	router, err := routing.New(routing.Config{MaxStale: time.Hour, Segments: billing.NewSegmentPolicy(billing.Segments{Output: 1000, Overshoot: 2 * time.Second})})
	if err != nil {
		t.Fatal(err)
	}
	f := &routeFixture{t: t, db: db, cat: cat, router: router, logs: new(strings.Builder), health: quality.New(nil), models: NewModelList()}
	f.served = f.seed("acme/served", specAll)
	f.nospec = f.seed("acme/nospec", specNoServing)
	f.unpriced = f.seed("other/unpriced", specAll)
	f.unlistedID = catalog.ModelID(uuid.NewV7())
	if err := cat.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.prices, err = f.fixturePrices(); err != nil {
		t.Fatal(err)
	}
	return f
}

// seed lists and names a model with the spec and a reference price: the catalog lists no model without one.
func (f *routeFixture) seed(slug, spec string) catalog.ModelID {
	f.t.Helper()
	return f.seedPrice(slug, spec, reference)
}

// seedPrice is seed with a reference price of its own: the catalog lists no model whose reference price leaves out an
// item its spec says the upstream may report.
func (f *routeFixture) seedPrice(slug, spec, price string) catalog.ModelID {
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
		Op: op(), Model: catalog.ModelID(id), Source: catalog.SourceManual, Schedule: jsonx.Raw(price),
		Evidence: jsonx.Raw(`{"url":"https://example.com/pricing"}`),
	}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.cat.ListModel(ctx, catalog.ModelRequest{Op: op(), Model: catalog.ModelID(id), Rev: 0}); err != nil {
		f.t.Fatal(err)
	}
	return catalog.ModelID(id)
}

// fixturePrices constructs explicit model prices; publication behavior belongs to billing and real-node tests.
func (f *routeFixture) fixturePrices() (map[catalog.ModelID]billing.Sold, error) {
	snap, err := f.cat.Current()
	if err != nil {
		return nil, err
	}
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		return nil, err
	}
	var method billing.Method
	if err := jsonx.Unmarshal([]byte(saleRule), &method); err != nil {
		return nil, err
	}
	out := map[catalog.ModelID]billing.Sold{}
	for m := range snap.Models() {
		if m.Author != "acme" {
			continue
		}
		ref, _ := snap.ReferencePrice(m.ID, "USD")
		schedule, err := billing.Sell(billing.Rule{Method: method}, ref, currency)
		if err != nil {
			return nil, err
		}
		out[m.ID] = billing.Sold{Version: uuid.NewV7(), Schedule: schedule}
	}
	return out, nil
}

// routable is one suppliers.Routable of a model on a supplier, as the sync writes them.
func routable(model catalog.ModelID, formats ...protocol.Format) suppliers.Routable {
	cost, err := catalog.ParseSchedule([]byte(strings.Replace(reference, `"rates":{`, `"rates":{"web_search":"0.01","code_execution":"0.01",`, 1)))
	if err != nil {
		panic(err)
	}
	return suppliers.Routable{
		ID: uuid.NewV7(), Supplier: uuid.NewV7(), Model: uuid.UUID(model), Upstream: "upstream-name",
		Funds: &suppliers.Funds{Balance: platform.AmountExact(1_000_000_000_000)}, Schedule: cost,
		Formats: formats, Streams: true, Cancels: true,
	}
}

// build runs RouteDeps.Build over the endpoints and the unknown items the test gives, and publishes what it made.
func (f *routeFixture) build(endpoints []suppliers.Routable) *routing.Snapshot {
	f.t.Helper()
	f.deps = RouteDeps{
		Endpoints: func(context.Context) ([]suppliers.Routable, error) { return endpoints, nil },
		Catalog:   f.cat,
		Prices: func(context.Context, *catalog.Snapshot, []suppliers.Routable, []platform.ExchangeRate) (map[catalog.ModelID]billing.Sold, error) {
			return f.prices, nil
		},
		Models:       f.models,
		UnknownItems: func(context.Context) ([]billing.UnknownItem, error) { return f.items, nil },
		Quality:      f.health,
		Router:       f.router,
		Logger:       slog.New(slog.NewTextHandler(f.logs, nil)),
	}
	snap, list, count, err := f.deps.Build(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	f.count = count
	f.deps.Publish(snap, list, count)
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
	r.Codename = "g-public"
	snap := f.build([]suppliers.Routable{r})
	if want := (Count{Endpoints: 1, Offered: 1}); f.count != want {
		t.Errorf("the build made %+v, want %+v", f.count, want)
	}
	if got := f.planned(call(f.served)); len(got) != 1 || got[0] != r.ID {
		t.Fatalf("routing plans to %v, want the endpoint %s", got, r.ID)
	}
	in := call(f.served)
	in.Override.OnlyNames = []string{r.Codename}
	plan, _, err := f.router.Plan(in)
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
	// The spec records no image size: the entry says so by leaving it 0, which the reservation takes to be the whole
	// context, so it never falls below what a request with an image can spend; the charge's estimate is billing's.
	if first, _ := billing.InputBound(billing.Demand{Images: 1}, built.Limits); first != built.Limits.ContextLength {
		t.Errorf("an image is bounded at %d tokens, want the context %d", first, built.Limits.ContextLength)
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
	// The spec claims temperature, so a request that requires every parameter it sets still finds the endpoint.
	in := call(f.served)
	in.Override.RequireParameters, in.Override.RequiredParameters = true, protocol.ParameterSet(1)<<protocol.ParamTemperature
	if got := f.planned(in); len(got) != 1 {
		t.Errorf("a call requiring temperature was planned to %d endpoints, want the one whose spec claims it", len(got))
	}
	// A stream is what a Claude Code or Codex turn is: an endpoint whose supplier cannot pass one must not be planned to.
	f.build([]suppliers.Routable{withStreams(routable(f.served, protocol.Messages), false)})
	if _, _, err := f.router.Plan(call(f.served)); err == nil {
		t.Error("a stream was planned to an endpoint whose supplier's policy passes no stream")
	}
}

func TestRouteBuildConvertsNativeClientsToChat(t *testing.T) {
	f := newRouteFixture(t)
	f.build([]suppliers.Routable{routable(f.served, protocol.Chat)})
	for _, client := range []protocol.Format{protocol.Messages, protocol.Responses} {
		in := call(f.served, func(r *protocol.Request) { r.Format = client })
		plan, _, err := f.router.Plan(in)
		if err != nil {
			t.Fatal(err)
		}
		entries := plan.Entries
		if len(entries) != 1 || entries[0].Target.Format != protocol.Chat || !entries[0].Limits.OpenAIUsage {
			t.Fatalf("client %v: entries %+v", client, entries)
		}
		in.NativeOnly = true
		if _, _, err := f.router.Plan(in); err == nil {
			t.Fatalf("native-only client %v was converted", client)
		}
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
		{f.unpriced, "no published sale price is available for this node; check the price publication list and local costs"},
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
// that finished late never replaces a newer view of the world, and neither does its model list: the two are one build's.
func TestRouteBuildPublishesOnlyNewerSnapshots(t *testing.T) {
	f := newRouteFixture(t)
	a, b := routable(f.served, protocol.Messages), routable(f.served, protocol.Messages)
	older := f.build([]suppliers.Routable{a})
	newer := f.build([]suppliers.Routable{a, b})
	listed := f.models.published()
	f.deps.Publish(older, &openRouterListing{built: listed.built.Add(-time.Second)}, Count{Endpoints: 1, Offered: 1}) // late, and older: refused
	if got := f.planned(call(f.served)); len(got) != 2 {
		t.Errorf("routing plans to %d endpoints, want the newer snapshot's 2", len(got))
	}
	if f.models.published() != listed {
		t.Error("a build that finished late replaced the model list")
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
			d.Prices = func(context.Context, *catalog.Snapshot, []suppliers.Routable, []platform.ExchangeRate) (map[catalog.ModelID]billing.Sold, error) {
				return nil, boom
			}
		},
		"the unknown billing items": func(d *RouteDeps) {
			d.UnknownItems = func(context.Context) ([]billing.UnknownItem, error) { return nil, boom }
		},
	} {
		d := RouteDeps{
			Endpoints: func(context.Context) ([]suppliers.Routable, error) { return nil, nil },
			Catalog:   f.cat,
			Prices: func(context.Context, *catalog.Snapshot, []suppliers.Routable, []platform.ExchangeRate) (map[catalog.ModelID]billing.Sold, error) {
				return map[catalog.ModelID]billing.Sold{}, nil
			},
			UnknownItems: func(context.Context) ([]billing.UnknownItem, error) { return nil, nil },
			Quality:      quality.New(nil), Router: f.router, Logger: slog.New(slog.DiscardHandler),
		}
		set(&d)
		if _, _, _, err := d.Build(t.Context()); err == nil || !strings.Contains(err.Error(), boom.Error()) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v, want it named and the cause kept", name, err)
		}
	}
}

// TestRouteBuildLeavesOutAModelWhoseSalePriceIsShort: a sale price that does not price an item the model reports (a
// fixed price list is not checked against the model when it is made) would answer every call 503 once a call reported
// that item, so the model is left out with the reason in the log. A per-use item is not short: its site's default price
// covers it (the test after this one).
func TestRouteBuildLeavesOutAModelWhoseSalePriceIsShort(t *testing.T) {
	f := newRouteFixture(t)
	short, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"}}`))
	if err != nil {
		t.Fatal(err)
	}
	f.prices[f.served] = billing.Sold{Version: uuid.NewV7(), Schedule: short}
	f.build([]suppliers.Routable{routable(f.served, protocol.Messages)})
	if want := (Count{Endpoints: 0, Offered: 1}); f.count != want {
		t.Errorf("the build made %+v, want %+v", f.count, want)
	}
	if logs := f.logs.String(); !strings.Contains(logs, "its sale price lacks a price") {
		t.Errorf("the log does not say why:\n%s", logs)
	}
}

// TestRouteBuildSellsAModelWhoseSalePriceLeavesAPerUseItemOut: a fixed price list that prices no web search still sells a
// model whose spec says its upstream may report one, because admission fills in the site's default price for it. Leaving
// the model out would answer plain text calls 404 too (docs/domains/billing.md「价目表缺项」).
func TestRouteBuildSellsAModelWhoseSalePriceLeavesAPerUseItemOut(t *testing.T) {
	f := newRouteFixture(t)
	model := f.seedPrice("acme/searcher", specSearch, searchReference)
	short, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"}}`))
	if err != nil {
		t.Fatal(err)
	}
	f.prices[model] = billing.Sold{Version: uuid.NewV7(), Schedule: short}
	r := routable(model, protocol.Messages)
	f.build([]suppliers.Routable{r})
	if want := (Count{Endpoints: 1, Offered: 1}); f.count != want {
		t.Fatalf("the build made %+v, want %+v: the search has a default price", f.count, want)
	}
	if got := f.planned(call(model)); len(got) != 1 || got[0] != r.ID {
		t.Errorf("routing plans to %v, want the endpoint %s", got, r.ID)
	}
}

// TestRouteBuildTellsAStandingReasonOnce: the snapshot is rebuilt every few seconds, and a model left out for the same
// reason at each build is one line in the log, not one every 5 seconds; a reason that changes, or comes back after the
// model was routable, is told again.
func TestRouteBuildTellsAStandingReasonOnce(t *testing.T) {
	f := newRouteFixture(t)
	f.build([]suppliers.Routable{routable(f.unpriced, protocol.Messages)})
	deps, offered := f.deps, []suppliers.Routable{routable(f.unpriced, protocol.Messages)}
	deps.Endpoints = func(context.Context) ([]suppliers.Routable, error) { return offered, nil }
	for range 3 {
		if _, _, _, err := deps.Build(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	reason := "no published sale price is available for this node; check the price publication list and local costs"
	if n := strings.Count(f.logs.String(), reason); n != 1 {
		t.Fatalf("the reason was told %d times over 4 builds, want once", n)
	}
	offered = nil // the model goes, and then comes back with the same reason
	if _, _, _, err := deps.Build(t.Context()); err != nil {
		t.Fatal(err)
	}
	offered = []suppliers.Routable{routable(f.unpriced, protocol.Messages)}
	if _, _, _, err := deps.Build(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(f.logs.String(), reason); n != 2 {
		t.Errorf("the reason was told %d times after the model left and came back, want twice", n)
	}
}

// TestRouteBuildRanksByWhatQualityMeasured: the snapshot carries the health, speed and time to first token real traffic
// measured, so the fastest goes by them, and a session no longer sticks to an endpoint whose health fell below 0.5.
func TestRouteBuildRanksByWhatQualityMeasured(t *testing.T) {
	f := newRouteFixture(t)
	fast, slow := routable(f.served, protocol.Messages), routable(f.served, protocol.Messages)
	answer := func(id uuid.UUID, gen time.Duration) {
		now := time.Now()
		f.health.Observe(t.Context(), quality.Observation{
			Endpoint: id, Done: true, OutputTokens: 100, Sent: now.Add(-gen - time.Second), FirstToken: now.Add(-gen), Ended: now,
		})
	}
	for range 5 {
		answer(fast.ID, time.Second)
		answer(slow.ID, 2*time.Second)
	}
	fastest := routing.Fastest
	in := call(f.served)
	in.Override.Sort, in.Sticky = &fastest, routing.EndpointID(slow.ID)
	f.build([]suppliers.Routable{slow, fast})
	if got := f.planned(in); len(got) != 2 || got[0] != slow.ID {
		t.Fatalf("route %v: the session's healthy endpoint is not first", got)
	}
	in.Sticky = routing.EndpointID{}
	if got := f.planned(in); got[0] != fast.ID {
		t.Fatalf("route %v: the fastest by measurement is not first", got)
	}
	for range 4 { // health 0.8⁴ ≈ 0.41
		f.health.Observe(t.Context(), quality.Observation{
			Endpoint: slow.ID, Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{Status: 500}},
			Ended: time.Now(),
		})
	}
	f.build([]suppliers.Routable{slow, fast})
	in.Sticky = routing.EndpointID(slow.ID)
	if got := f.planned(in); got[0] != fast.ID {
		t.Errorf("route %v: the session still sticks to an endpoint whose health is below 0.5", got)
	}
}
