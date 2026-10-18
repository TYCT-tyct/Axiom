package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/gateway"
	"axiom/internal/observability"
	"axiom/internal/platform"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/platform/telemetry"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// callPath is billing.Suppliers as the suppliers module implements it: the writes billing makes in the transaction of
// its own settlement and cleanup, which is how a call's supplier side commits exactly when the ledger's cost does
// (docs/architecture.md 规则 5). billing names suppliers' types, so this is all that joins the two: cmd/ is the only
// package that may hold both, and neither module knows the other's wiring.
type callPath struct{}

var _ billing.Suppliers = callPath{}

func (callPath) RecordAttempts(ctx context.Context, tx pg.Tx, attempts []suppliers.AttemptRecord) error {
	return suppliers.RecordAttempts(ctx, tx, attempts)
}

func (callPath) RecordLostRequest(ctx context.Context, tx pg.Tx, request, site, account uuid.UUID, route []uuid.UUID) error {
	return suppliers.RecordLostRequest(ctx, tx, platform.RequestID(request), site, account, route)
}

func (callPath) ResolveLostRequest(ctx context.Context, tx pg.Tx, request uuid.UUID) error {
	return suppliers.ResolveLostRequest(ctx, tx, platform.RequestID(request))
}

func (callPath) EndRequest(ctx context.Context, tx pg.Tx, request, site, account uuid.UUID, at time.Time) error {
	return suppliers.EndRequest(ctx, tx, platform.RequestID(request), site, account, at)
}

const (
	// nodeMemory is the node's budget for request bodies, conversions, relay buffers and aggregated replies, which
	// protocol charges before it allocates: the 64 KiB each relay holds plus this (internal/gateway/README.md「内存」).
	nodeMemory = 2 << 30
	// reloadEvery is how often the suppliers' snapshot is rebuilt, which is the catalog's polling period: routing
	// merges the two every 5 seconds, so a faster reload would only be read later.
	reloadEvery = 5 * time.Second
	// settleGrace is how long the settlements of finished calls may keep retrying after the listeners stop
	// (internal/gateway/README.md「优雅退出」). What is still unsettled then is the cleanup's.
	settleGrace = 30 * time.Second
)

// callDeps is what a node's call path is built from: the modules it orchestrates and the secret it derives the
// identifiers sent upstream from.
type callDeps struct {
	db       *pg.DB
	keys     *secret.Keyring         // unseals suppliers' credentials
	identity []byte                  // the identity key file's newest key
	accounts *accounts.Service       // the module the calls are admitted through, which took its lookups' slots from limits
	catalog  *catalog.Catalog        //
	egress   *egress.Client          //
	limits   *gateway.Backpressure   // the node's in-process limits, which the accounts module was given too
	records  *observability.Recorder // the call log, which the settlements write to
	tel      *telemetry.Telemetry    //
	logger   *slog.Logger            //
}

// callNode is a node's call path: the handler the public listener mounts and the executor, whose background work the
// shutdown drains, the signal that routing can route, and the one loop whose end stops the node.
type callNode struct {
	handler  *gateway.Handler
	executor *gateway.Executor
	ready    <-chan struct{} // closed once a published snapshot can route
	reaped   <-chan error    // carries what billing.Run ended with, when it is not a shutdown
}

// startCallPath builds everything a model call needs from admission to the ledger, starts the loops that keep its
// inputs fresh, and returns the handler the public listener serves. The first reading of every input happens here,
// synchronously, so a node whose catalog, endpoints or prices cannot be read fails its start instead of becoming ready
// and answering 503. A node that can route nothing starts too, but stays not ready: a
// fresh deployment gets its first supplier through the admin listener (see announceReady).
func startCallPath(ctx context.Context, d callDeps) (callNode, error) {
	gate := gateway.DefaultConfig()
	limits := d.limits
	router, err := routing.New(routing.Config{})
	if err != nil {
		return callNode{}, err
	}
	upstreams, err := suppliers.New(suppliers.Config{
		DB: d.db, Scope: pg.PlatformScope(), Keys: d.keys, Egress: d.egress,
	})
	if err != nil {
		return callNode{}, err
	}
	memory := protocol.NewMemory(nodeMemory)
	ledger, err := billing.Start(ctx, d.db, pg.PlatformScope(), billing.Config{
		HolderTTL: gate.HolderTTL, Heartbeat: gate.HolderHeartbeat, ReapGrace: gate.ReclaimGrace,
		// A reservation lives as long as a call may take plus the grace its settlement has: the cleanup takes over
		// what no settlement will only once the call cannot still be running.
		Lifetime: gate.MaxCall + gate.SettleGrace, Logger: d.logger,
	}, callPath{})
	if err != nil {
		return callNode{}, err
	}
	executor, err := gateway.NewExecutor(gateway.ExecDeps{
		Router: router, Upstreams: gateway.NewUpstreams(upstreams), Limits: limits, Memory: memory,
		Logger: d.logger, Ledger: ledger, Costs: upstreams, Alive: ledger.Alive, IdentitySecret: d.identity,
		Records: d.records,
	})
	if err != nil {
		return callNode{}, err
	}
	handler, err := gateway.NewHandler(gateway.Deps{
		Accounts: d.accounts, Catalog: d.catalog, Router: router, Billing: ledger, Limits: limits,
		Memory: memory, Run: executor.Run, Logger: d.logger,
	})
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
	var ready = make(chan struct{})
	var once sync.Once
	routes := gateway.RouteDeps{
		Endpoints:    func(context.Context) ([]suppliers.Routable, error) { return upstreams.Routable(), nil },
		Catalog:      d.catalog,
		Prices:       salePrices(d.db, d.accounts, d.catalog),
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

	// The loops. The holder's is the only one that can end the node: once it has been reaped nothing this process
	// writes is fenced in, so it must be restarted with a new one (#205).
	reaped := make(chan error, 1)
	go func() {
		if err := ledger.Run(ctx); err != nil {
			reaped <- err
		}
	}()
	go func() {
		_ = d.catalog.Run(ctx) // returns only when ctx ends; a failed refresh is logged there and keeps the snapshot
	}()
	go reloadEndpoints(ctx, upstreams, d.logger)
	go routes.Run(ctx)
	return callNode{handler: handler, executor: executor, ready: ready, reaped: reaped}, nil
}

// announceReady reports the node ready once its routing snapshot can route, and stops listening when the node ends.
// Readiness is a promise about routing, so it is made only after a snapshot is published, never at the moment the
// listeners open: the mutation that moves it before the first snapshot is what this waits out.
func announceReady(ctx context.Context, ready <-chan struct{}, health *httpserver.Health, logger *slog.Logger) {
	select {
	case <-ready:
		health.SetReady(true)
		logger.Info("ready", "routing", "the snapshot has an endpoint")
	case <-ctx.Done():
	}
}

// Drain gives the calls in flight and the settlements they left in the background up to settleGrace to end, after the
// listeners have stopped admitting: what still has not is the cleanup's (internal/gateway/README.md「故障矩阵」).
func (c callNode) Drain(ctx context.Context) {
	c.handler.Drain(ctx) // the reservations of admissions that were lost
	c.executor.Drain(ctx, settleGrace)
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
// currency and rules is G8's.
func salePrices(db *pg.DB, sites *accounts.Service, cat *catalog.Catalog) func(context.Context) (map[catalog.ModelID]billing.Sold, error) {
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
		return billing.SalePrices(rules, snap, site.Currency)
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
