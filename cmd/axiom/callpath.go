package main

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync/atomic"
	"time"
	"uuid"

	"axiom/internal/platform"

	"go.opentelemetry.io/otel/metric"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/fileparse"
	"axiom/internal/gateway"
	"axiom/internal/observability"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

const (
	// nodeMemory is the node's budget for request bodies, conversions, relay buffers and aggregated replies, which
	// protocol charges before it allocates: the 64 KiB each relay holds plus this (internal/gateway/README.md「内存」).
	nodeMemory = 2 << 30
	// reloadEvery is how often the suppliers' snapshot is rebuilt, which is the catalog's polling period: routing
	// merges the two every 5 seconds, so a faster reload would only be read later.
	reloadEvery = 5 * time.Second
	shareEvery  = time.Second
	// readinessEvery is how often readiness is compared with the snapshot's freshness: well inside routing's MaxStale,
	// so a node is out of rotation within a second of its calls starting to fail.
	readinessEvery = time.Second
	// abortGrace is how long what the calls left in the background has after its attempts are cancelled
	// (internal/gateway/README.md「优雅退出」); it comes out of the shutdown deadline the listeners drain under, so the
	// calls and their settlements share that one deadline. What is still unsettled then is the cleanup's.
	// deploy/compose.yaml's stop_grace_period covers the edge delay, AXIOM_SHUTDOWN_TIMEOUT and the queue's and the
	// recorder's own deadlines.
	abortGrace = 5 * time.Second
)

// callDeps is what a node's call path is built from: the modules it orchestrates, the secret it derives the
// identifiers sent upstream from, and the reverse proxies the deployment trusts (AXIOM_TRUSTED_PROXIES).
type callDeps struct {
	nodeID        string
	healthMode    quality.Mode
	db            *pg.DB
	keys          *secret.Keyring   // unseals suppliers' credentials
	identity      []byte            // the identity key file's newest key
	accounts      *accounts.Service // the module the calls are admitted through, which took its lookups' slots from limits
	catalog       *catalog.Catalog
	egress        *egress.Client
	search        gateway.SearchBackends
	fileParser    fileparse.Parser
	gate          gateway.Config          // the settings limits was made from, which the lifetimes and windows below follow
	limits        *gateway.Backpressure   // the node's in-process limits, which the accounts module was given too
	records       *observability.Recorder // the call log, which the settlements write to
	meter         metric.Meter            // where the call path's counters go
	logger        *slog.Logger
	proxies       []netip.Prefix // whose X-Forwarded-For names the client, which the IP limit and the keys' allow-lists judge
	kv            *kv.Client     // the shared cache the precheck counts in, nil in single-core mode
	responseCache bool           // whether the platform permits the response cache to store reply content
	alerts        alert.Sink
	// verify answers what an endpoint claims to serve, for model verification. Production sets it unless
	// AXIOM_MODEL_VERIFICATION=off; a test whose fake upstream counts every generation leaves it nil, and the
	// endpoint then stays unverified.
	verify func(catalog.ModelID) (quality.Claimed, bool)
}

// callNode is a node's call path: the handler the public listener mounts and the executor, whose background work the
// shutdown drains, the signal that routing can route, and the one loop whose end stops the node.
type callNode struct {
	handler     *gateway.Handler
	executor    *gateway.Executor
	outstanding *gateway.Outstanding // what the handler and the executor count on, which Drain waits for
	// upstreams is the path's suppliers, which the admin also tests an endpoint through: a test must go through the
	// snapshot and the credentials the calls use, not a second view of the database.
	upstreams *suppliers.Service
	stop      context.CancelFunc // ends the loops; the node calls it after Drain, so the holder beats while settlements land
	ready     func() bool        // whether the node can route now: its latest snapshot can route and is fresh
	reaped    <-chan error       // carries what billing.Run ended with, when it is not a shutdown
	// models is the list the route builder publishes with each snapshot, also read by the public models and prices page.
	models *gateway.ModelList
	// search is the node's enabled engines, also read by the public model pages to show what a search costs.
	search gateway.SearchBackends
	cache  *gateway.ResponseCache // the response cache's writer and expiry loop, started with the other loops
	// health is the node's endpoint evaluation; the console reads the source classes it routes by from it.
	health *quality.Monitor
}

// startCallPath builds everything a model call needs from admission to the ledger, starts the loops that keep its
// inputs fresh, and returns the handler the public listener serves. The first reading of every input happens here,
// synchronously, so a node whose catalog, endpoints or prices cannot be read fails its start instead of becoming ready
// and answering 503. A node that can route nothing starts too, but stays not ready: a
// fresh deployment gets its first supplier through the admin listener (see announceReady).
func startCallPath(ctx context.Context, d callDeps) (callNode, error) {
	if d.records == nil {
		return callNode{}, errors.New("the call path needs the call log recorder")
	}
	billing.WarmTokenizers()
	var nodes suppliers.ShareRegistry
	if d.kv != nil {
		nodes = d.kv
	}
	upstreams, err := suppliers.New(suppliers.Config{DB: d.db, Scope: pg.PlatformScope(), Keys: d.keys, Egress: d.egress, Meter: d.meter, Nodes: nodes})
	if err != nil {
		return callNode{}, err
	}
	// The ledger reads the segment settings first, then routing takes the same live policy: an entry is never marked
	// segmentable with no sizes for billing to reserve it by (#229).
	ledger, err := startLedger(ctx, d.db, d.gate, d.meter, d.logger, d.alerts)
	if err != nil {
		return callNode{}, err
	}
	// The window in which a call may start another attempt is the gateway's StartBy, so the two cannot differ: routing
	// stamps it on every plan and the executor reads it from there, not from the configuration.
	router, err := routing.New(routingConfig(d.gate, ledger.Segments()))
	if err != nil {
		return callNode{}, err
	}
	health, healthLoop, err := quality.Start(ctx, quality.Config{
		Node: d.nodeID, DB: d.db, Scope: pg.PlatformScope(), Mode: d.healthMode,
		Source: upstreams, Pauses: router, Alerts: d.alerts, Logger: d.logger, Meter: d.meter,
		Identity: d.verify,
	})
	if err != nil {
		return callNode{}, err
	}
	// The list in OpenRouter's shape is built by the route builder with every snapshot it publishes, from the sale prices
	// that snapshot was priced with, so a price a client reads is a price a call is charged at.
	models := gateway.NewModelList()
	n, err := newCallNode(d, router, upstreams, ledger, health, models)
	if err != nil {
		return callNode{}, err
	}
	// What the snapshot is built from, read once before the node is ready: a failure here is a startup failure.
	if err := loadCallInputs(ctx, upstreams, d.catalog); err != nil {
		return callNode{}, err
	}
	var routable atomic.Bool
	routes := routeDeps(d, router, upstreams, health, &routable, salePrices(d.db, d.accounts), models)
	snap, list, count, err := routes.Build(ctx)
	if err != nil {
		return callNode{}, err
	}
	routes.Publish(snap, list, count)
	if !count.Routable() {
		d.logger.Warn("the node is not ready: none of the endpoints suppliers offer can be routed, so every model call "+
			"answers 404 no_endpoint until each model has a spec and a sale price",
			"offered", count.Offered, "in_snapshot", count.Endpoints)
	}
	// The loops outlive the signal that stops the node: the holder must keep beating until the settlements are written.
	loops, stop := context.WithCancel(context.WithoutCancel(ctx))
	healthDone := make(chan struct{})
	go func() { defer close(healthDone); healthLoop.Run(loops) }()
	n.ready, n.reaped, n.stop = func() bool { return routable.Load() && router.Fresh() }, startLoops(loops, d, n.cache, ledger, upstreams, routes), func() { stop(); <-healthDone }
	return n, nil
}

// routingConfig is what the router gets from the node's settings and the live segment policy the ledger loaded. The
// policy is the one ledgerConfig hands billing, so routing never marks an entry segmentable with no sizes for billing
// to reserve it by (#229).
func routingConfig(gate gateway.Config, segments *billing.SegmentPolicy) routing.Config {
	return routing.Config{AttemptWindow: gate.StartBy, Segments: segments}
}

// ledgerConfig is what billing gets from the node's settings; it holds the same live policy routingConfig reads, so an
// entry one of them segments is one the other can reserve.
func ledgerConfig(gate gateway.Config, segments *billing.SegmentPolicy, logger *slog.Logger, meter metric.Meter, signals alert.Sink) billing.Config {
	return billing.Config{
		HolderTTL: gate.HolderTTL, Heartbeat: gate.HolderHeartbeat, ReapGrace: gate.ReclaimGrace,
		// A reservation lives as long as a call may take plus the grace its settlement has: the cleanup takes over
		// what no settlement will only once the call cannot still be running.
		Lifetime: gate.MaxCall + gate.SettleGrace, Segments: segments, Logger: logger, Meter: meter, Alerts: signals,
	}
}

// startLedger starts billing under a new holder, with the lifetimes the gateway's configuration gives a call and the
// segment settings the administrator saved. The policy is shared with routing through Billing.Segments.
func startLedger(ctx context.Context, db *pg.DB, gate gateway.Config, meter metric.Meter, logger *slog.Logger, signals alert.Sink) (*billing.Billing, error) {
	segments, err := billing.LoadSegmentPolicy(ctx, db, pg.PlatformScope(), gate.ExtendWait, logger)
	if err != nil {
		return nil, err
	}
	return billing.Start(ctx, db, pg.PlatformScope(), ledgerConfig(gate, segments, logger, meter, signals), suppliers.CallWrites{})
}

// newCallNode makes the executor that runs admitted calls and the handler that admits them.
func newCallNode(d callDeps, router *routing.Router, upstreams *suppliers.Service, ledger *billing.Billing, health *quality.Monitor,
	models *gateway.ModelList,
) (callNode, error) {
	memory, outstanding := protocol.NewMemory(nodeMemory), gateway.NewOutstanding()
	var (
		cache *gateway.ResponseCache
		err   error
	)
	if d.responseCache {
		cache, err = gateway.NewResponseCache(d.db, d.keys, pg.PlatformScope(), d.logger, d.meter)
		if err != nil {
			return callNode{}, err
		}
	}
	executor, err := gateway.NewExecutor(gateway.ExecDeps{
		Router: router, Upstreams: gateway.NewUpstreams(upstreams), Limits: d.limits, Memory: memory,
		Logger: d.logger, Ledger: ledger, Costs: upstreams, Alive: ledger.Alive, IdentitySecret: d.identity,
		Records: d.records, Quality: health, Outstanding: outstanding, Search: d.search, Cache: cache,
	})
	if err != nil {
		return callNode{}, err
	}
	handler, err := gateway.NewHandler(gateway.Deps{
		Accounts: d.accounts, Catalog: d.catalog, Router: router, Billing: ledger, Limits: d.limits,
		Memory: memory, Run: executor.Run, Logger: d.logger, Meter: d.meter, Models: models, Proxies: d.proxies, KV: d.kv,
		Outstanding: outstanding, Search: d.search, Parser: d.fileParser, Records: d.records,
	})
	return callNode{handler: handler, executor: executor, outstanding: outstanding, upstreams: upstreams, models: models, search: d.search, cache: cache, health: health}, err
}

// routeDeps are the sources of the routing snapshot. routable holds whether the latest published snapshot can route, and
// models collects the model list published with it. Apart from the main site's currency, every source is read through a
// closure, so a test that holds only the suppliers service builds the wiring (callpath_test.go).
func routeDeps(d callDeps, router *routing.Router, upstreams *suppliers.Service, health *quality.Monitor, routable *atomic.Bool,
	prices func(context.Context, *catalog.Snapshot, []suppliers.Routable, []platform.ExchangeRate) (map[catalog.ModelID]billing.Sold, error), models *gateway.ModelList,
) *gateway.RouteDeps {
	// The currency only denominates BYOK prices; no other routing source needs the accounts service, which a caller
	// may not hold.
	var siteCurrency platform.Currency
	if d.accounts != nil {
		if site, ok := d.accounts.MainSite(); ok {
			siteCurrency = site.Currency
		}
	}
	return &gateway.RouteDeps{
		ExchangeRates: exchangeRates(d),
		Endpoints:     func(context.Context) ([]suppliers.Routable, error) { return upstreams.Routable() },
		Catalog:       d.catalog,
		Prices:        prices,
		ByokFee:       byokFee(d),
		ByokRules:     byokRules(d),
		SiteCurrency:  siteCurrency,
		Models:        models,
		UnknownItems:  unknownItems(d.db),
		Presets:       routePresets(d.db, d.logger, router),
		Quality:       health,
		Router:        router,
		Logger:        d.logger,
		// Readiness is decided here, once the snapshot is the router's: a node whose suppliers offer no endpoint yet is
		// ready, because there is nothing to route and the console is how its first supplier is set up; a node whose
		// suppliers offer endpoints that the catalog or the price list cannot describe is not, because every model call
		// would answer 404 no_endpoint and the log is the only place that says why.
		Built: func(c gateway.Count) { routable.Store(c.Routable()) },
	}
}

// startLoops starts the loops that keep the call path's inputs fresh. The holder's is the only one that can end the
// node: once it has been reaped nothing this process writes is fenced in, so it must be restarted with a new one
// (#205). Its end is what the channel carries.
func startLoops(ctx context.Context, d callDeps, cache *gateway.ResponseCache, ledger *billing.Billing, upstreams *suppliers.Service, routes *gateway.RouteDeps) <-chan error {
	reaped := make(chan error, 1)
	go func() {
		if err := ledger.Run(ctx); err != nil {
			reaped <- err
		}
	}()
	go func() {
		if err := d.catalog.Run(ctx); err != nil { // returns only when ctx ends; a failed refresh is logged there
			d.logger.Error("the catalog's refresh loop ended", "err", err)
		}
	}()
	go reloadEndpoints(ctx, upstreams, d.logger)
	go routes.Run(ctx)
	if cache != nil {
		go cache.Run(ctx)
	}
	return reaped
}

// announceReady keeps the node's readiness equal to ready(): that the latest published snapshot can route and is fresh.
// Readiness is a promise about routing, so it is made only after such a snapshot is published, never at the moment the
// listeners open; it is withdrawn when every call would fail, with 503 while the snapshot is stale or 404 once every
// endpoint is excluded, so a load balancer stops sending traffic here, and restored when a rebuild succeeds. The
// returned function ends the loop and returns once it has: after it, nothing sets the node ready again, so a shutdown's
// not-ready stays.
func announceReady(ctx context.Context, ready func() bool, every time.Duration, health *httpserver.Health, logger *slog.Logger) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(every)
		defer tick.Stop()
		serving := false
		for {
			if now := ready(); now != serving {
				serving = now
				health.SetReady(now)
				if now {
					logger.Info("ready", "routing", "the snapshot has an endpoint")
				} else {
					logger.Error("not ready: the routing snapshot is stale or has no routable endpoint, so model calls fail until a rebuild fixes it")
				}
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() { cancel(); <-done }
}

// Drain gives the requests in flight, the calls and the settlements they left in the background until the shutdown
// deadline, after the listeners have stopped admitting. Attempts are cancelled abortGrace before that deadline, and
// what is still not settled when it ends is the cleanup's (internal/gateway/README.md「故障矩阵」).
func (c callNode) Drain(ctx context.Context) {
	if c.outstanding != nil {
		c.outstanding.Drain(ctx, abortGrace)
	}
}

// reloadEndpoints rebuilds the suppliers' snapshot every reloadEvery until ctx ends, so that a change made in the admin
// reaches routing within a period. A failed reload is logged and the previous snapshot stays, as the catalog's does,
// until it is older than the suppliers' MaxStale: then Routable refuses it and the node stops routing rather than
// calling endpoints it can no longer confirm.
func reloadEndpoints(ctx context.Context, s *suppliers.Service, logger *slog.Logger) {
	tick := time.NewTicker(reloadEvery)
	shares := time.NewTicker(shareEvery)
	defer tick.Stop()
	defer shares.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-shares.C:
			if err := s.SyncShares(ctx); err != nil && ctx.Err() == nil {
				logger.Error("suppliers: credential shares could not be synchronized; admission stops when the lease expires", "err", err)
			}
			if err := s.RefreshBalances(ctx); err != nil && ctx.Err() == nil {
				logger.Error("suppliers: a requested balance sync could not be scheduled", "err", err)
			}
		case <-tick.C:
			if err := s.Reload(ctx); err != nil && ctx.Err() == nil {
				logger.Error("suppliers: the reload failed; the endpoints of the last one stay until they are stale", "err", err)
			}
		}
	}
}

// salePrices only reads published heads. Local cost checks may withhold an endpoint, but never change shared publication
// state: nodes with different adapters or key versions must not publish different evidence.
func salePrices(db *pg.DB, sites *accounts.Service) func(context.Context, *catalog.Snapshot, []suppliers.Routable, []platform.ExchangeRate) (map[catalog.ModelID]billing.Sold, error) {
	return func(ctx context.Context, snap *catalog.Snapshot, endpoints []suppliers.Routable, rates []platform.ExchangeRate) (sold map[catalog.ModelID]billing.Sold, err error) {
		site, ok := sites.MainSite()
		if !ok {
			return nil, nil
		}
		err = db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) (err error) {
			sold, err = billing.PublishedPrices(ctx, tx, uuid.UUID(site.ID))
			return err
		})
		if err != nil {
			return nil, err
		}
		costs := priceCosts(endpoints, rates, site.Currency)
		byModel := map[catalog.ModelID][]billing.PriceCost{}
		for _, c := range costs {
			byModel[c.Model] = append(byModel[c.Model], c)
		}
		filterPriceCosts(sold, snap, byModel)
		return sold, nil
	}
}

func filterPriceCosts(sold map[catalog.ModelID]billing.Sold, snap *catalog.Snapshot, byModel map[catalog.ModelID][]billing.PriceCost) {
	for id, price := range sold {
		model, ok := snap.Model(id)
		if !ok {
			delete(sold, id)
			continue
		}
		price = price.WithCosts(model, byModel[id])
		if !price.Available() {
			delete(sold, id)
		} else {
			sold[id] = price
		}
	}
}

func priceCosts(endpoints []suppliers.Routable, rates []platform.ExchangeRate, currency platform.Currency) []billing.PriceCost {
	costs := make([]billing.PriceCost, 0, len(endpoints))
	for _, e := range endpoints {
		if e.Byok {
			// A key's zero cost is the account's, not the platform's: it must not set the floor of a shared sale price.
			continue
		}
		var rate platform.ExchangeRate
		if e.Currency != currency {
			for _, r := range rates {
				if _, ok := r.Between(e.Currency, currency); ok {
					rate = r
					break
				}
			}
		}
		costs = append(costs, billing.PriceCost{Endpoint: e.ID, Model: catalog.ModelID(e.Model), Schedule: e.Schedule, UnitValue: e.UnitValue.String(), UnitValueID: e.UnitValueID, Currency: e.Currency, CurrencyCode: e.Currency.String(), Exchange: rate})
	}
	return costs
}

// unknownItems is what billing met in a usage report that nothing prices. Its model takes no new requests until an
// administrator prices the field (docs/domains/billing.md「价目表缺项」), so routing is told of it at every rebuild.
func unknownItems(db *pg.DB) func(context.Context) ([]billing.UnknownItem, error) {
	return func(ctx context.Context) (items []billing.UnknownItem, err error) {
		err = db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
			items, err = billing.UnknownItems(ctx, tx)
			return err
		})
		return items, err
	}
}

// routePresets reads the routing presets for each build, keeping the versions it compiled, and tells router whether the
// platform has suspended them as soon as it reads that (routing.Presets).
func routePresets(db *pg.DB, logger *slog.Logger, router *routing.Router) func(context.Context) ([]routing.Preset, error) {
	source := &routing.Presets{Logger: logger, Router: router}
	return func(ctx context.Context) (presets []routing.Preset, err error) {
		err = db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
			presets, err = source.Load(ctx, tx)
			return err
		})
		return presets, err
	}
}

func loadCallInputs(ctx context.Context, upstreams *suppliers.Service, cat *catalog.Catalog) error {
	if err := upstreams.Reload(ctx); err != nil {
		return err
	}
	if err := upstreams.SyncShares(ctx); err != nil {
		return err
	}
	return cat.Refresh(ctx)
}

// claimedByCatalog answers what an endpoint claims to serve from the catalog's current snapshot: the catalog model's
// slug and, when it has one, the vendor-native name a verification answer should report; the author's display name,
// which it must carry; and every other author's display name, which instead contradicts the claim. The snapshot is
// read when a check is due, in the health loop, never on the call path (docs/domains/quality.md「真伪检测」).
func claimedByCatalog(cat *catalog.Catalog) func(catalog.ModelID) (quality.Claimed, bool) {
	return func(id catalog.ModelID) (quality.Claimed, bool) {
		snap, err := cat.Current()
		if err != nil {
			return quality.Claimed{}, false
		}
		m, ok := snap.Model(id)
		if !ok || m.AuthorName == "" {
			return quality.Claimed{}, false
		}
		name, _ := snap.OfficialName(id)
		others := make([]string, 0, 8)
		seen := make(map[string]struct{}, 8)
		for other := range snap.Models() {
			name := other.AuthorName
			if name == "" || name == m.AuthorName {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			others = append(others, name)
		}
		return quality.Claimed{Author: m.AuthorName, Others: others, Model: m.Slug, Name: name}, true
	}
}

// byokRules reads the accounts' no-fallback choices on every build. They come from the key rows, not from the routable
// endpoints, so a key the provider refused keeps its rule (docs/domains/routing.md「自带的供应商 key」).
func byokRules(d callDeps) func(context.Context) ([]routing.ByokRule, error) {
	return func(ctx context.Context) ([]routing.ByokRule, error) {
		rules, err := suppliers.ByokRules(ctx, d.db, pg.PlatformScope())
		if err != nil {
			return nil, err
		}
		out := make([]routing.ByokRule, 0, len(rules))
		for _, r := range rules {
			out = append(out, routing.ByokRule{Account: routing.AccountID(r.Account), Model: catalog.ModelID(r.Model)})
		}
		return out, nil
	}
}

// byokFee reads the main site's BYOK rate on every build. The route build is the main site's, so the rate is its: a
// secondary site's own rate governs calls made against that site, which this node does not serve.
func byokFee(d callDeps) func(context.Context) (string, error) {
	return func(ctx context.Context) (rate string, err error) {
		site, ok := d.accounts.MainSite()
		if !ok {
			return "0", nil
		}
		err = d.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) (err error) {
			rate, err = billing.ByokFee(ctx, tx, uuid.UUID(site.ID))
			return err
		})
		return rate, err
	}
}
