package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// rebuildEvery is how often Run replaces the routing snapshot: the sources it is built from (the catalog's prices, the
// suppliers' endpoints, the endpoints' specs) are all replaced whole in the background every few seconds, so the
// snapshot that merges them follows at the same pace (docs/domains/routing.md「快照从哪来」).
const rebuildEvery = 5 * time.Second

// RouteDeps are the sources of a routing snapshot and the router it is published to. Every one is required.
type RouteDeps struct {
	// Endpoints are the endpoints suppliers can call now: *suppliers.Service's Routable, which reads the snapshot the
	// last Reload built and never the database.
	Endpoints func(context.Context) ([]suppliers.Routable, error)
	Catalog   *catalog.Catalog
	// Prices is each model's sale price in the main site's currency: billing.SalePrices over the site's pricing rules
	// and the catalog's reference prices.
	Prices func(context.Context) (map[catalog.ModelID]billing.Sold, error)
	// UnknownItems is billing.UnknownItems: the usage fields nothing prices, one of which takes its model out of
	// routing until an administrator prices it (docs/domains/billing.md「价目表缺项」).
	UnknownItems func(context.Context) ([]billing.UnknownItem, error)
	Router       *routing.Router
	Every        time.Duration // how often Run rebuilds; rebuildEvery when zero
	Logger       *slog.Logger
	// Built, when set, is told what each published snapshot holds, after it is the router's. The node reports itself
	// ready there and nowhere else, so readiness is never a promise made before a snapshot exists.
	Built func(Count)
	// Quality is what real traffic measured of each endpoint, the same Monitor the executor reports attempts to.
	Quality *quality.Monitor

	told map[catalog.ModelID]string // why each model left out was last reported, so a standing reason is told once
}

// Count is what one build made of the endpoints suppliers offer: how many are in the snapshot, and how many there were
// to begin with. The difference is the endpoints of models the catalog or the price list cannot describe.
type Count struct {
	Endpoints int // in the snapshot
	Offered   int // suppliers.Routable gave
}

// Routable reports whether the node can route any model, or has no endpoint at all to route: a supplier's first sync
// has not reached it yet. Anything else is a model whose spec or price is missing, which is no state to serve in.
func (c Count) Routable() bool { return c.Endpoints > 0 || c.Offered == 0 }

// Build assembles the snapshot from one reading of every source and returns it with what it holds. Nothing is published:
// the caller either publishes it (Publish, which Run does) or decides from the count whether the node may report itself
// ready (cmd/axiom).
//
// An endpoint whose model the catalog cannot describe is left out, and so are all of the model's endpoints: one whose
// model has no spec, one no pricing rule gives a sale price. A model whose upstream reported a field nothing prices is
// in the snapshot, flagged, so that routing refuses it with a reason an administrator can read. Each of these is a line
// in the log, since a model that cannot be routed is a price list or a spec to fix. A snapshot with no endpoint at all
// is a valid one: every call is then answered 404 no_endpoint, which is what a node whose suppliers have not synced yet
// has to say.
func (d *RouteDeps) Build(ctx context.Context) (*routing.Snapshot, Count, error) {
	catalogSnap, err := d.Catalog.Current()
	if err != nil {
		return nil, Count{}, fmt.Errorf("gateway: the catalog: %w", err)
	}
	routable, err := d.Endpoints(ctx)
	if err != nil {
		return nil, Count{}, fmt.Errorf("gateway: the endpoints: %w", err)
	}
	prices, err := d.Prices(ctx)
	if err != nil {
		return nil, Count{}, fmt.Errorf("gateway: the sale prices: %w", err)
	}
	items, err := d.UnknownItems(ctx)
	if err != nil {
		return nil, Count{}, fmt.Errorf("gateway: the unknown billing items: %w", err)
	}
	blocked := make(map[catalog.ModelID]struct{}, len(items))
	for _, u := range items {
		blocked[u.Model] = struct{}{}
	}
	built := make([]routing.Endpoint, 0, len(routable))
	left := map[catalog.ModelID]*leftOut{}
	now := time.Now()
	for _, r := range routable {
		e, why := endpointOf(catalogSnap, prices, blocked, r, d.Quality.Measured(r.ID, now))
		if why != "" {
			note(left, catalog.ModelID(r.Model), why)
			continue
		}
		built = append(built, e)
	}
	d.report(left)
	// Dated after the sources were read, so the snapshot is never older than the state it shows.
	snap, err := d.Router.NewSnapshot(time.Now(), built, presets())
	if err != nil {
		return nil, Count{}, err
	}
	return snap, Count{Endpoints: len(built), Offered: len(routable)}, nil
}

// Publish replaces the router's snapshot with one Build made and tells Built what it holds. Nothing else takes a
// snapshot to the router, so what Built hears of is always a snapshot routing already plans with.
func (d *RouteDeps) Publish(s *routing.Snapshot, c Count) {
	d.Router.Replace(s)
	if d.Built != nil {
		d.Built(c)
	}
}

// Run rebuilds the snapshot every Every and replaces the router's with it, until ctx ends. A failed build is logged and
// the previous snapshot stays, which then goes stale on its own after routing's MaxStale: a node that cannot read its
// endpoints stops routing rather than routing to the endpoints it had.
func (d *RouteDeps) Run(ctx context.Context) {
	every := d.Every
	if every <= 0 {
		every = rebuildEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		if s, n, err := d.Build(ctx); err != nil {
			if ctx.Err() == nil {
				d.Logger.Error("gateway: the routing snapshot was not rebuilt; the last one stands until it is stale", "err", err)
			}
		} else {
			d.Publish(s, n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// leftOut is why a model takes no requests, and how many of its endpoints it takes with it.
type leftOut struct {
	reason    string
	endpoints int
}

func note(left map[catalog.ModelID]*leftOut, model catalog.ModelID, reason string) {
	if l, ok := left[model]; ok {
		l.endpoints++
		return
	}
	left[model] = &leftOut{reason: reason, endpoints: 1}
}

// report says which models are not routable, and why, so that a call that finds no endpoint has its cause in the log.
// A reason that stands from one build to the next is told once, not at every build (every 5 seconds): it is told again
// when it changes or comes back.
func (d *RouteDeps) report(left map[catalog.ModelID]*leftOut) {
	for model := range d.told {
		if _, still := left[model]; !still {
			delete(d.told, model)
		}
	}
	for model, l := range left {
		if d.told[model] == l.reason {
			continue
		}
		if d.told == nil {
			d.told = map[catalog.ModelID]string{}
		}
		d.told[model] = l.reason
		d.Logger.Warn("gateway: a model's endpoints are left out of the routing snapshot",
			"model", model.String(), "endpoints", l.endpoints, "reason", l.reason)
	}
}

// endpointOf is one supplier endpoint as routing holds it, or the reason it cannot be one. The rules are the assembly
// ones of docs/domains/routing.md「快照从哪来」: nothing is guessed — a model without a spec, or without a sale price,
// has no routing entry at all.
func endpointOf(snap *catalog.Snapshot, prices map[catalog.ModelID]billing.Sold, blocked map[catalog.ModelID]struct{}, r suppliers.Routable,
	measured quality.Measures,
) (routing.Endpoint, string) {
	model, ok := snap.Model(catalog.ModelID(r.Model))
	if !ok {
		return routing.Endpoint{}, "the catalog has no such model"
	}
	serving, ok := model.Serving()
	if !ok {
		return routing.Endpoint{}, "its spec declares no context length, output limit, streaming or abilities"
	}
	sold, ok := prices[catalog.ModelID(r.Model)]
	if !ok {
		return routing.Endpoint{}, "no pricing rule gives it a sale price"
	}
	reportable := model.Reportable()
	if billing.Unpriced(sold.Schedule, reportable) != (catalog.Reportable{}) {
		// Only a reference price was checked against what the model reports when it was listed; a fixed price was not.
		return routing.Endpoint{}, "its sale price lacks a price for an item or a service tier the model reports"
	}
	tiers := reportable.ServiceTiers
	tiers.Add(catalog.ServiceTierStandard) // every endpoint is called at the standard tier unless it says otherwise
	native := routing.Of(r.Formats...)
	served := native
	if native.Has(protocol.Messages) || native.Has(protocol.Responses) {
		served |= routing.Of(protocol.Chat) // a Chat request is converted to the first of those the endpoint speaks
	}
	_, blockedItem := blocked[catalog.ModelID(r.Model)]
	// The model streams only if its spec says the vendor does and the supplier's own policy says it can pass a stream:
	// the gateway reads every answer as one, so an endpoint lacking either cannot serve a generation.
	streams := serving.Streams && r.Streams
	e := routing.Endpoint{
		ID: routing.EndpointID(r.ID), Supplier: routing.SupplierID(r.Supplier), Model: catalog.ModelID(r.Model),
		// The platform's own: M1's suppliers are the platform's (suppliers' owner scope is not part of Routable), and
		// the node reaches every one of them from its own address.
		// BalanceOK is always true: suppliers' snapshot holds only suppliers with a balance above zero; whether this request
		// can be paid for is not asked per request yet.
		Enabled: true, BalanceOK: true, Egresses: 1,
		// Every supplier M1 buys from is a relay or reseller, which is what its adapter talks to; nothing records what
		// one does with the data it is sent, so an endpoint is taken to be the worst case rather than the best: a preset
		// that requires no retention must not route to it on the strength of a value nobody set.
		Class: routing.ClassRelay, Policy: routing.PolicyMayTrain,
		// Health, time to first token and speed are what real traffic measured, as of this build: the snapshot is at most
		// one rebuild behind the attempts (docs/domains/quality.md「质量分、吞吐与延迟」). Nothing scores quality yet;
		// one is the neutral value that neither favours nor penalizes an endpoint, assigned in this one place.
		Quality: 1, Confidence: 1, Health: measured.Health, Throughput: measured.Throughput, Latency: measured.Latency,
		ServiceTiers: tiers,
		Native:       native, Served: served,
		Caps:      capabilities(serving.Abilities),
		Context:   serving.Context,
		MaxOutput: serving.MaxOutput,
		// The spec records no image size and no page limit, so ImageTokens, PageTokens and FilePages stay 0: billing
		// takes an image or a file of unknown size to fill the whole window when it reserves, which must never be below
		// what a request spends, and charges an estimate when the upstream reports no input (billing.InputEstimate).
		Streams:     streams,
		Cancels:     r.Cancels,
		Reports:     reportable.Items,
		UnknownItem: blockedItem,
		Price:       routing.PriceRef{Version: sold.Version, Schedule: sold.Schedule},
		Target:      targetOf(r.Upstream, streams, model.Dialect()),
	}
	return e, ""
}

// targetOf is how an attempt is written for the endpoint: its upstream name and whether it streams, and the spec's
// dialect as it is. An unset claim stays unset in the Target; protocol decides what a request that needs it is to do
// (internal/protocol/README.md「跨格式转换」), so no default for the model is made here.
func targetOf(upstream string, streams bool, d catalog.Dialect) protocol.Target {
	return protocol.Target{
		Model: upstream, Streams: streams, Thinking: d.Thinking, EncryptedReasoning: d.EncryptedReasoning, MidSystem: d.MidSystem,
		UntrustedSystemAfterTools: d.UntrustedSystemAfterTools, DefaultSamplingOnly: d.DefaultSamplingOnly,
		ForcedToolChoice: d.ForcedToolChoice, CacheBreakpoints: d.CacheBreakpoints,
	}
}

// capabilities is a spec's abilities as protocol's bits. The two enumerations happen to be in the same order, but the
// mapping is written out so that a change in either of them is a missing line here and not a silent shift.
func capabilities(abilities catalog.AbilitySet) (c protocol.Capabilities) {
	for _, a := range [...]struct {
		ability catalog.Ability
		bit     protocol.Capabilities
	}{
		{catalog.AbilityTools, protocol.Tools},
		{catalog.AbilityImages, protocol.Images},
		{catalog.AbilityDocuments, protocol.Documents},
		{catalog.AbilityReasoning, protocol.Reasoning},
		{catalog.AbilityPromptCache, protocol.PromptCache},
		{catalog.AbilityStructuredOutput, protocol.StructuredOutput},
		{catalog.AbilityWebSearch, protocol.WebSearch},
		{catalog.AbilityCodeExecution, protocol.CodeExecution},
	} {
		if abilities.Has(a.ability) {
			c |= a.bit
		}
	}
	return c
}

// presets is the one preset M1 routes under: the zero ID, which a key without a preset of its own is routed by
// (admit.go routingAdmission), admitting every class and every data policy, since no endpoint carries a measured one
// yet. NewSnapshot refuses a preset that admits none. MaxAttempts is four: a route holds at most routing.MaxEntries
// entries and one request makes at most platform.MaxAttempts attempts, so four is never reached, and the router's
// default is written down here rather than left implicit.
func presets() []routing.Preset {
	return []routing.Preset{{Classes: routing.AllClasses, Policies: routing.AllPolicies, MaxAttempts: 4}}
}
