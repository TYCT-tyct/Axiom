package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// rebuildEvery is how often Run replaces the routing snapshot: the sources it is built from (the catalog's prices, the
// suppliers' endpoints, the endpoints' specs) are all replaced whole in the background every few seconds, so the
// snapshot that merges them follows at the same pace (docs/domains/routing.md「快照从哪来」).
const rebuildEvery = 5 * time.Second

// buildLimit bounds one of Run's builds: a snapshot is stamped with the moment its build began, so one that took longer
// than routing's default MaxStale (30s) is stale when published, and a build stuck on a source (a table lock) must free
// the slot for the next one.
const buildLimit = 30 * time.Second

// RouteDeps are the sources of a routing snapshot and the router it is published to.
type RouteDeps struct {
	// ExchangeRates reads once for the whole build. Nil means no configured pairs; production supplies the reader.
	ExchangeRates func(context.Context) ([]platform.ExchangeRate, error)
	// Endpoints are the endpoints suppliers can call now: *suppliers.Service's Routable, which reads the snapshot the
	// last Reload built and never the database.
	Endpoints func(context.Context) ([]suppliers.Routable, error)
	Catalog   *catalog.Catalog
	// Prices reads published sale versions and checks them against this build's local costs.
	// One build's reading prices both the routing snapshot and the model list, so
	// the price a client reads is the price a call is charged at.
	Prices func(context.Context, *catalog.Snapshot, []suppliers.Routable, []platform.ExchangeRate) (map[catalog.ModelID]billing.Sold, error)
	// ByokFee is the site's share of a model's reference price, charged when the account's own key serves it, as a
	// multiplier (billing.ByokFee). Nil is no fee; a node that cannot read it fails the build, because serving a key at a
	// price nobody confirmed is worse than not serving it.
	ByokFee func(context.Context) (string, error)
	// ByokRules are the accounts' no-fallback choices. They are read apart from the endpoints, so a rejected credential
	// does not lift the choice. Nil means none.
	ByokRules func(context.Context) ([]routing.ByokRule, error)
	// Presets reads the routing presets, the system allow-all among them (routing.Presets.Load); a build that cannot
	// read them keeps the last snapshot. Nil routes every key by the code-built allow-all preset, for tests.
	Presets func(context.Context) ([]routing.Preset, error)
	// SiteCurrency is the main site's currency. A BYOK endpoint may serve a model the site does not sell, so it has no
	// shared sale price to read the currency from; its fee is still in the site's currency.
	SiteCurrency platform.Currency
	// Models is where each published snapshot's model list in OpenRouter's shape goes: the handler serves it,
	// and it is built in the same build as the snapshot from the same reading (docs/domains/gateway.md「模型列表」).
	Models *ModelList
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

	// slot holds a token while a Rebuild runs: Build writes told, and Run and a write's Rebuild may meet. It is a
	// channel, not a mutex, so a write that waits for a stuck rebuild can give up when its own deadline passes.
	slot     chan struct{}
	slotOnce sync.Once
	told     map[catalog.ModelID]string // why each model left out was last reported, so a standing reason is told once
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

// Build assembles the snapshot from one reading of every source and returns it with the model list built beside it and
// what it holds. Nothing is published: the caller either publishes it (Publish, which Run does) or decides from the count
// whether the node may report itself ready (cmd/axiom).
//
// An endpoint whose model the catalog cannot describe is left out, and so are all of the model's endpoints when its
// spec is missing; an endpoint with no published sale version safe for this node is left out by itself, and the model
// stays when another endpoint built. A model whose upstream reported a field nothing prices is in the snapshot, flagged,
// so that routing refuses it with a reason an administrator can read. Each of these is a line in the log, since an
// endpoint that cannot be routed is a price list or a spec to fix. A snapshot with no endpoint at all
// is a valid one: every call is then answered 404 no_endpoint, which is what a node whose suppliers have not synced yet
// has to say.
//
// The model list comes from this same reading — the endpoints that made the snapshot, the catalog they were read from and
// the sale prices they were priced with — so the price a client reads is the price routing charges, and a build that fails
// publishes neither (Publish).
func (d *RouteDeps) Build(ctx context.Context) (*routing.Snapshot, *openRouterListing, Count, error) {
	// Start time bounds freshness even if reading a source stalls during the build.
	builtAt := time.Now()
	src, err := d.read(ctx)
	if err != nil {
		return nil, nil, Count{}, err
	}
	if err := d.Quality.Prepare(ctx, src.routable); err != nil {
		return nil, nil, Count{}, err
	}
	built := make([]routing.Endpoint, 0, len(src.routable))
	left := map[catalog.ModelID]*leftOut{}
	for _, r := range src.routable {
		e, why := endpointOf(src.catalog, src.prices, src.blocked, src.byokFee, src.rates, d.SiteCurrency, r, d.Quality.MeasuredFor(r, time.Now()))
		if why != "" {
			note(left, catalog.ModelID(r.Model), why)
			continue
		}
		e.LiveHealth = d.Quality.Live(r.ID, r.Credentials)
		e.Exchange = exchangeBasis(src.rates, r.Currency, routeCurrency(e))
		built = append(built, e)
	}
	// A failed endpoint is left out by itself; the model stays when another endpoint built. report names every model
	// with a failed endpoint, whether it still serves others or has no route at all, so an endpoint's absence has a
	// cause in the log too.
	d.report(left)
	snap, err := d.Router.NewSnapshot(builtAt, built, src.presets, src.byokRules...)
	if err != nil {
		return nil, nil, Count{}, err
	}
	list, err := newOpenRouterListing(src.catalog, src.prices, built)
	if err != nil {
		return nil, nil, Count{}, err
	}
	list.built = builtAt // the same stamp the snapshot takes, so the two are published and refused together
	return snap, list, Count{Endpoints: len(built), Offered: len(src.routable)}, nil
}

// sources is one reading of everything a snapshot is built from.
type sources struct {
	rates     []platform.ExchangeRate
	catalog   *catalog.Snapshot
	routable  []suppliers.Routable
	prices    map[catalog.ModelID]billing.Sold
	byokFee   string
	byokRules []routing.ByokRule
	presets   []routing.Preset
	blocked   map[catalog.ModelID]struct{} // the models with a usage field nothing prices
}

// read reads every source of a snapshot once, in the order each one needs the ones before it. A source RouteDeps leaves
// nil reads as its documented default.
func (d *RouteDeps) read(ctx context.Context) (src sources, err error) {
	if src.rates, err = optional(ctx, d.ExchangeRates, nil); err != nil {
		return src, fmt.Errorf("gateway: exchange rates: %w", err)
	}
	if src.catalog, err = d.Catalog.Current(); err != nil {
		return src, fmt.Errorf("gateway: the catalog: %w", err)
	}
	if src.routable, err = d.Endpoints(ctx); err != nil {
		return src, fmt.Errorf("gateway: the endpoints: %w", err)
	}
	if src.prices, err = d.Prices(ctx, src.catalog, src.routable, src.rates); err != nil {
		return src, fmt.Errorf("gateway: the sale prices: %w", err)
	}
	if src.byokFee, err = optional(ctx, d.ByokFee, "0"); err != nil {
		return src, fmt.Errorf("gateway: the BYOK fee: %w", err)
	}
	if src.byokRules, err = optional(ctx, d.ByokRules, nil); err != nil {
		return src, fmt.Errorf("gateway: the provider keys' fallback rules: %w", err)
	}
	if src.presets, err = optional(ctx, d.Presets, presets()); err != nil {
		return src, fmt.Errorf("gateway: the routing presets: %w", err)
	}
	items, err := d.UnknownItems(ctx)
	if err != nil {
		return src, fmt.Errorf("gateway: the unknown billing items: %w", err)
	}
	src.blocked = make(map[catalog.ModelID]struct{}, len(items))
	for _, u := range items {
		src.blocked[u.Model] = struct{}{}
	}
	return src, nil
}

// optional reads a source RouteDeps may leave nil, which reads as fallback.
func optional[T any](ctx context.Context, read func(context.Context) (T, error), fallback T) (T, error) {
	if read == nil {
		return fallback, nil
	}
	return read(ctx)
}

func routeCurrency(e routing.Endpoint) platform.Currency {
	if e.AudiencePrices != nil {
		for _, p := range e.AudiencePrices.Layers() {
			if p.Sold.Version != uuid.Nil() {
				return p.Sold.Schedule.Currency()
			}
		}
	}
	return e.Price.Schedule.Currency()
}

// exchangeBasis is the configured rate between a supplier's currency and the site's, or the bare pair when none is
// configured, so that the missing rate is frozen with the route too. Same currency needs none.
func exchangeBasis(rates []platform.ExchangeRate, from, to platform.Currency) platform.ExchangeRate {
	if from == to {
		return platform.ExchangeRate{}
	}
	for _, rate := range rates {
		if _, ok := rate.Between(from, to); ok {
			return rate
		}
	}
	return platform.ExchangeRate{Base: from.String(), Quote: to.String()}
}

// Publish replaces the router's snapshot with one Build made, publishes the model list built with it, and tells Built
// what it holds. Nothing else takes a snapshot to the router, so what Built hears of is always a snapshot routing already
// plans with, and the list a client reads was built from the same reading of the catalog and the sale prices: the price it
// shows is the price that snapshot routes at.
//
// A build that finished late is refused as a whole, its list with it: the router keeps the newer snapshot it holds
// (Router.Replace refuses the older one), and a list of that older reading standing beside it would quote prices no call is
// charged. Only Publish hands snapshots to the router, so the newest it took is the newest published here.
func (d *RouteDeps) Publish(s *routing.Snapshot, l *openRouterListing, c Count) {
	d.Models.mu.Lock()
	defer d.Models.mu.Unlock()
	if old := d.Models.last; old != nil && !l.built.After(old.built) {
		return
	}
	d.Router.Replace(s)
	d.Models.last = l
	if d.Built != nil {
		d.Built(c)
	}
}

// Run rebuilds the snapshot every Every and replaces the router's with it, until ctx ends. A failed build is logged and
// the previous snapshot stands, which then goes stale on its own after routing's MaxStale: a node that cannot read its
// endpoints stops routing rather than routing to the endpoints it had. The model list stands with it, at the prices of
// that same snapshot, so a build that fails cannot leave the list quoting prices routing does not charge.
func (d *RouteDeps) Run(ctx context.Context) {
	every := d.Every
	if every <= 0 {
		every = rebuildEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		build, cancel := context.WithTimeout(ctx, buildLimit)
		err := d.Rebuild(build)
		cancel()
		if err != nil && ctx.Err() == nil {
			d.Logger.Error("gateway: the routing snapshot was not rebuilt; the last one stands until it is stale", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Rebuild builds a snapshot and publishes it with its model list. Run calls it every Every; the node that committed a
// change routing reads (a sale price, a model's status) calls it once the change is committed, so its next call is
// admitted at the new state rather than at the one Run last read (docs/architecture.md「模块边界规则」, rule 3). A build that fails
// publishes nothing, and the last snapshot stands. It waits for a rebuild in progress, which may have read the sources
// before the change, and gives up, as its reads do, when ctx ends.
func (d *RouteDeps) Rebuild(ctx context.Context) error {
	d.slotOnce.Do(func() { d.slot = make(chan struct{}, 1) })
	select {
	case d.slot <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("gateway: waiting for the rebuild in progress: %w", context.Cause(ctx))
	}
	defer func() { <-d.slot }()
	s, l, n, err := d.Build(ctx)
	if err != nil {
		return err
	}
	d.Publish(s, l, n)
	return nil
}

// leftOut is why some or all of a model's endpoints take no requests, and how many of them are left out.
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

// presets is the system allow-all preset as code builds it, for a RouteDeps without a preset source: the zero ID, which
// a key without a preset of its own is routed by (admit.go routingAdmission), admitting every class and every data
// policy. It is the rules routing's migration writes for the same ID. MaxAttempts is four: a route holds at most
// routing.MaxEntries entries and one request makes at most platform.MaxAttempts attempts, so four is never reached, and
// the router's default is written down here rather than left implicit.
func presets() []routing.Preset {
	return []routing.Preset{{Classes: routing.AllClasses, Policies: routing.AllPolicies, MaxAttempts: 4}}
}
