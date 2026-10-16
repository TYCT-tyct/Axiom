package gateway

import (
	"strings"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

const (
	// The OpenAI formats may bill the cache write as well, so a schedule that serves them prices it.
	cheap     = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001"}}`
	dear      = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000004","cache_write":"0.000001"}}`
	basicSpec = `{"items":["input","output"],"tiers":[],"context_length":128000}`
	evidence  = `{"url":"https://example.com/pricing","quote":"input $1 per million"}`
)

func (r *rig) seedCatalog() {
	var err error
	if r.catalog, err = catalog.Open(r.db, platformScope, catalog.Config{MaxStale: time.Hour}); err != nil {
		r.t.Fatal(err)
	}
	r.alpha = r.model("acme/alpha")
	r.beta = r.model("acme/beta")
	r.bare = r.model("anthropic/claude-test", "claude-test")
	if err := r.catalog.Refresh(r.t.Context()); err != nil {
		r.t.Fatal(err)
	}
}

// model lists a model with its slug as a public name, and the other names given.
func (r *rig) model(slug string, names ...string) catalog.ModelID {
	r.t.Helper()
	ctx := r.t.Context()
	op := func() catalog.Op {
		return catalog.Op{Actor: audit.System("gateway.test"), Key: uuid.NewV7(), Reason: "test"}
	}
	author, _, _ := strings.Cut(slug, "/")
	must := func(res catalog.Result, err error) catalog.Result {
		r.t.Helper()
		if err != nil {
			r.t.Fatal(err)
		}
		return res
	}
	must(r.catalog.SaveAuthor(ctx, catalog.SaveAuthorRequest{Op: op(), Slug: author, Name: author}))
	res := must(r.catalog.CreateModel(ctx, catalog.CreateModelRequest{
		Op: op(), Slug: slug, VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: jsonx.Raw(basicSpec),
	}))
	id, err := uuid.Parse(res.Subject)
	if err != nil {
		r.t.Fatal(err)
	}
	model := catalog.ModelID(id)
	for _, name := range append([]string{slug}, names...) {
		must(r.catalog.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: model, Name: name, Visibility: catalog.VisibilityPublic}))
	}
	must(r.catalog.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: op(), Model: model, Source: catalog.SourceManual, Schedule: jsonx.Raw(cheap), Evidence: jsonx.Raw(evidence),
	}))
	var rev int64
	if err := r.db.Tx(ctx, platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(ctx, "SELECT rev FROM catalog.model WHERE id = $1", id).Scan(&rev)
	}); err != nil {
		r.t.Fatal(err)
	}
	must(r.catalog.ListModel(ctx, catalog.ModelRequest{Op: op(), Model: model, Rev: rev}))
	return model
}

// seedRouting installs a routing snapshot: alpha is served by alpha-1 and by alpha-2, which charges twice the output
// price, and beta by beta-1. The preset the keys have none of, the zero one, puts the cheapest first.
func (r *rig) seedRouting(o rigOptions) {
	var err error
	if r.router, err = routing.New(o.routing); err != nil {
		r.t.Fatal(err)
	}
	all := routing.Of(protocol.Messages, protocol.Responses, protocol.Chat)
	var endpoints []routing.Endpoint
	for _, s := range []struct {
		name  string
		model catalog.ModelID
		price string
	}{{"alpha-1", r.alpha, cheap}, {"alpha-2", r.alpha, dear}, {"beta-1", r.beta, cheap}} {
		schedule, err := catalog.ParseSchedule([]byte(s.price))
		if err != nil {
			r.t.Fatal(err)
		}
		e := routing.Endpoint{
			ID: routing.EndpointID(uuid.NewV7()), Supplier: routing.SupplierID(uuid.NewV7()), Model: s.model, Enabled: true,
			BalanceOK: true, Egresses: 1, Class: routing.ClassOfficial, Policy: routing.PolicyNoRetention, Verified: true,
			Quality: 1, Confidence: 1, Health: 1, Throughput: 100, Latency: 500 * time.Millisecond,
			ServiceTiers: 1<<catalog.ServiceTierStandard | 1<<catalog.ServiceTierPriority, Native: all, Served: all,
			Caps: ^protocol.Capabilities(0), Context: 1_000_000, MaxOutput: maxOutput, Streams: true, Cancels: true,
			ReasoningVisible: all, Price: routing.PriceRef{Version: uuid.NewV7(), Schedule: schedule},
		}
		if o.endpoints != nil {
			o.endpoints(s.name, &e)
		}
		r.endpoints[s.name] = e
		endpoints = append(endpoints, e)
	}
	preset := routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}
	if e, ok := r.endpoints[o.prefer]; ok {
		preset.Order = []routing.SupplierID{e.Supplier}
	}
	r.install(preset, endpoints...)
}

// install replaces the routing snapshot.
func (r *rig) install(preset routing.Preset, endpoints ...routing.Endpoint) {
	r.t.Helper()
	snap, err := r.router.NewSnapshot(time.Now(), endpoints, []routing.Preset{preset})
	if err != nil {
		r.t.Fatal(err)
	}
	r.router.Replace(snap)
}
