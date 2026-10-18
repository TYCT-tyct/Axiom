package main

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/metric"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/gateway"
	"axiom/internal/observability"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
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
	// drainWait is how long the calls and the settlements they left in the background may take to end after the
	// listeners stop, and abortGrace how long what is left has after its attempts are cancelled
	// (internal/gateway/README.md「优雅退出」). What is still unsettled then is the cleanup's. deploy/compose.yaml's
	// stop_grace_period covers AXIOM_SHUTDOWN_TIMEOUT, these two and the queue's and the recorder's own deadlines.
	drainWait, abortGrace = 15 * time.Second, 5 * time.Second
)

// callDeps is what a node's call path is built from: the modules it orchestrates and the secret it derives the
// identifiers sent upstream from.
type callDeps struct {
	db       *pg.DB
	keys     *secret.Keyring   // unseals suppliers' credentials
	identity []byte            // the identity key file's newest key
	accounts *accounts.Service // the module the calls are admitted through, which took its lookups' slots from limits
	catalog  *catalog.Catalog
	egress   *egress.Client
	limits   *gateway.Backpressure   // the node's in-process limits, which the accounts module was given too
	records  *observability.Recorder // the call log, which the settlements write to
	meter    metric.Meter            // where the ledger's counters go
	logger   *slog.Logger
}

// callNode is a node's call path: the handler the public listener mounts and the executor, whose background work the
// shutdown drains, the signal that routing can route, and the one loop whose end stops the node.
type callNode struct {
	handler  *gateway.Handler
	executor *gateway.Executor
	stop     context.CancelFunc // ends the loops; the node calls it after Drain, so the holder beats while settlements land
	ready    <-chan struct{}    // closed once a published snapshot can route
	reaped   <-chan error       // carries what billing.Run ended with, when it is not a shutdown
}

// startCallPath builds everything a model call needs from admission to the ledger, starts the loops that keep its
// inputs fresh, and returns the handler the public listener serves. The first reading of every input happens here,
// synchronously, so a node whose catalog, endpoints or prices cannot be read fails its start instead of becoming ready
// and answering 503. A node that can route nothing starts too, but stays not ready: a
// fresh deployment gets its first supplier through the admin listener (see announceReady).
func startCallPath(ctx context.Context, d callDeps) (callNode, error) {
	// The window in which a call may start another attempt is the gateway's StartBy, so the two cannot differ: routing
	// stamps it on every plan and the executor reads it from there, not from the configuration.
	router, err := routing.New(routing.Config{AttemptWindow: gateway.DefaultConfig().StartBy})
	if err != nil {
		return callNode{}, err
	}
	upstreams, err := suppliers.New(suppliers.Config{DB: d.db, Scope: pg.PlatformScope(), Keys: d.keys, Egress: d.egress})
	if err != nil {
		return callNode{}, err
	}
	ledger, err := startLedger(ctx, d.db, d.meter, d.logger)
	if err != nil {
		return callNode{}, err
	}
	n, err := newCallNode(d, router, upstreams, ledger)
	if err != nil {
		return callNode{}, err
	}
	// What the snapshot is built from, read once before the node is ready: a failure here is a startup failure.
	if err := upstreams.Reload(ctx); err != nil {
		return callNode{}, err
	}
	if err := d.catalog.Refresh(ctx); err != nil {
		return callNode{}, err
	}
	ready := make(chan struct{})
	routes := routeDeps(d, router, upstreams, ready)
	snap, count, err := routes.Build(ctx)
	if err != nil {
		return callNode{}, err
	}
	routes.Publish(snap, count)
	if !count.Routable() {
		d.logger.Warn("the node is not ready: none of the endpoints suppliers offer can be routed, so every model call "+
			"answers 404 no_endpoint until each model has a spec and a sale price",
			"offered", count.Offered, "in_snapshot", count.Endpoints)
	}
	// The loops outlive the signal that stops the node: the holder must keep beating until the settlements are written.
	loops, stop := context.WithCancel(context.WithoutCancel(ctx))
	n.ready, n.reaped, n.stop = ready, startLoops(loops, d, ledger, upstreams, routes), stop
	return n, nil
}

// startLedger starts billing under a new holder, with the lifetimes the gateway's configuration gives a call.
func startLedger(ctx context.Context, db *pg.DB, meter metric.Meter, logger *slog.Logger) (*billing.Billing, error) {
	gate := gateway.DefaultConfig()
	return billing.Start(ctx, db, pg.PlatformScope(), billing.Config{
		HolderTTL: gate.HolderTTL, Heartbeat: gate.HolderHeartbeat, ReapGrace: gate.ReclaimGrace,
		// A reservation lives as long as a call may take plus the grace its settlement has: the cleanup takes over
		// what no settlement will only once the call cannot still be running.
		Lifetime: gate.MaxCall + gate.SettleGrace, Logger: logger, Meter: meter,
	}, suppliers.CallWrites{})
}

// newCallNode makes the executor that runs admitted calls and the handler that admits them.
func newCallNode(d callDeps, router *routing.Router, upstreams *suppliers.Service, ledger *billing.Billing) (callNode, error) {
	memory := protocol.NewMemory(nodeMemory)
	executor, err := gateway.NewExecutor(gateway.ExecDeps{
		Router: router, Upstreams: gateway.NewUpstreams(upstreams), Limits: d.limits, Memory: memory,
		Logger: d.logger, Ledger: ledger, Costs: upstreams, Alive: ledger.Alive, IdentitySecret: d.identity,
		Records: d.records,
	})
	if err != nil {
		return callNode{}, err
	}
	handler, err := gateway.NewHandler(gateway.Deps{
		Accounts: d.accounts, Catalog: d.catalog, Router: router, Billing: ledger, Limits: d.limits,
		Memory: memory, Run: executor.Run, Logger: d.logger,
	})
	return callNode{handler: handler, executor: executor}, err
}

// routeDeps are the sources of the routing snapshot. ready is closed by the first published snapshot that can route.
func routeDeps(d callDeps, router *routing.Router, upstreams *suppliers.Service, ready chan struct{}) *gateway.RouteDeps {
	var once sync.Once
	return &gateway.RouteDeps{
		Endpoints:    func(context.Context) ([]suppliers.Routable, error) { return upstreams.Routable(), nil },
		Catalog:      d.catalog,
		Prices:       salePrices(d.db, d.accounts, d.catalog, d.logger),
		UnknownItems: unknownItems(d.db),
		Router:       router,
		Logger:       d.logger,
		// Readiness is decided here, once the snapshot is the router's: a node whose suppliers offer no endpoint yet is
		// ready, because there is nothing to route and the console is how its first supplier is set up; a node whose
		// suppliers offer endpoints that the catalog or the price list cannot describe is not, because every model call
		// would answer 404 no_endpoint and the log is the only place that says why.
		Built: func(c gateway.Count) {
			if c.Routable() {
				once.Do(func() { close(ready) })
			}
		},
	}
}

// startLoops starts the loops that keep the call path's inputs fresh. The holder's is the only one that can end the
// node: once it has been reaped nothing this process writes is fenced in, so it must be restarted with a new one
// (#205). Its end is what the channel carries.
func startLoops(ctx context.Context, d callDeps, ledger *billing.Billing, upstreams *suppliers.Service, routes *gateway.RouteDeps) <-chan error {
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
	return reaped
}

// announceReady reports the node ready once its routing snapshot can route. Readiness is a promise about routing, so it
// is made only after a snapshot is published, never at the moment the listeners open. The returned function ends the
// wait and returns once it has: after it, the node can no longer be reported ready, so a shutdown's not-ready stays.
func announceReady(ctx context.Context, ready <-chan struct{}, health *httpserver.Health, logger *slog.Logger) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ready:
			health.SetReady(true)
			logger.Info("ready", "routing", "the snapshot has an endpoint")
		case <-ctx.Done():
		}
	}()
	return func() { cancel(); <-done }
}

// Drain gives the calls in flight and the settlements they left in the background up to drainWait to end, after the
// listeners have stopped admitting, and abortGrace more once their attempts are cancelled: what still has not ended is
// the cleanup's (internal/gateway/README.md「故障矩阵」).
func (c callNode) Drain(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, drainWait)
	defer cancel()
	c.handler.Drain(ctx) // the reservations of admissions that were lost
	c.executor.Drain(ctx, abortGrace)
}

// reloadEndpoints rebuilds the suppliers' snapshot every reloadEvery until ctx ends, so that a change made in the admin
// reaches routing within a period. A failed reload is logged and the previous snapshot stays, as the catalog's does:
// the endpoints it could not read are the ones this node keeps calling.
func reloadEndpoints(ctx context.Context, s *suppliers.Service, logger *slog.Logger) {
	tick := time.NewTicker(reloadEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := s.Reload(ctx); err != nil && ctx.Err() == nil {
				logger.Error("suppliers: the reload failed; the endpoints of the last one stay", "err", err)
			}
		}
	}
}

// salePrices is each model's sale price in the main site's currency: the site's pricing rules applied to the catalog's
// reference prices (billing.SalePrices). M1 prices the node's models at the main site's, because the currency a call is
// charged in is the site's and a routing snapshot is one per node, not one per site; a secondary site with its own
// currency and rules is G8's. Only the rules that price everyone count, for the same reason: a rule for one account or
// tier cannot price a model for the whole node.
func salePrices(db *pg.DB, sites *accounts.Service, cat *catalog.Catalog, logger *slog.Logger) func(context.Context) (map[catalog.ModelID]billing.Sold, error) {
	return func(ctx context.Context) (map[catalog.ModelID]billing.Sold, error) {
		// A node deployed before its site exists prices nothing, so no model is routable: that is a normal state of a
		// fresh deployment, and the site is set up through `axiom site` on a node that is already serving.
		site, ok := sites.MainSite()
		if !ok {
			return nil, nil
		}
		snap, err := cat.Current()
		if err != nil {
			return nil, err
		}
		var rules []billing.Rule
		err = db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) (err error) {
			rules, err = billing.Rules(ctx, tx, uuid.UUID(site.ID))
			return err
		})
		if err != nil {
			return nil, err
		}
		rules = slices.DeleteFunc(rules, func(r billing.Rule) bool { return !r.Audience.Prices(accounts.AccountID{}, accounts.TierID{}) })
		listed := make([]catalog.ModelID, 0)
		for m := range snap.Models() {
			listed = append(listed, m.ID)
		}
		sold, unpriced := billing.SalePrices(rules, snap, listed, site.Currency)
		for _, err := range unpriced {
			// A rule that cannot price the model it was set for leaves it unsold rather than emptying the site's prices.
			logger.Warn("a sale price the catalog cannot hold; the model is not sold", "err", err)
		}
		return sold, nil
	}
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
