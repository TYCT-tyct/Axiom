package main

import (
	"context"
	"log/slog"
	"net/http"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/fileparse"
	"axiom/internal/gateway"
	"axiom/internal/observability"
	"axiom/internal/payments"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/telemetry"
	"axiom/internal/suppliers"
	"axiom/internal/websearch"
)

// nodeRoutes are what the node's listeners serve and the call path behind the public one.
type nodeRoutes struct {
	public, admin http.Handler
	calls         callNode
}

// plugins opens the search engines and the file parser the plugins call, each only when the deployment configured
// one: without a backend the plugin keeps the refusal it has always answered.
func (p consoleParts) plugins(ctx context.Context, db *pg.DB, exit *egress.Client) (gateway.SearchBackends, fileparse.Parser, error) {
	search, err := p.search(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	engines, err := websearch.OpenEngines(search.Engines, exit)
	if err != nil {
		return nil, nil, err
	}
	parser, err := p.fileParser(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	var fileParser fileparse.Parser
	if parser.Configured() {
		if fileParser, err = fileparse.Open(parser, exit); err != nil {
			return nil, nil, err
		}
	}
	return engines, fileParser, nil
}

// routes starts the accounts module on this node, which loads the sites, domains and roles and keeps them fresh until
// ctx ends, the call path, and puts the console's API in front of it for the public listener with the model API beside
// it, and the admin's API for the admin listener. Opening an account also opens its balance (openBalance).
func (p consoleParts) routes(ctx context.Context, db *pg.DB, tel *telemetry.Telemetry, logger *slog.Logger,
	recorder *observability.Recorder, exit *egress.Client, cache *kv.Client, recovery *accounts.PasswordRecovery,
	registration *accounts.Registration,
) (nodeRoutes, error) {
	// The accounts module and the call path take their in-process limits from one value: a key lookup that misses the
	// cache uses a database connection like an admission does, so the two must share the counting, or a flood of
	// unknown keys would be bounded by nothing (gateway.Backpressure.KeyLookupSlot, which gateway.NewHandler requires).
	gate := gateway.DefaultConfig()
	gate.PoolSize, gate.PoolReserve = int(db.MaxConns()), db.CompletionConns()
	limits, err := gateway.New(gate, tel.Metrics.Meter("axiom/gateway"))
	if err != nil {
		return nodeRoutes{}, err
	}
	service, err := accounts.NewService(db, accounts.Config{
		Platform: pg.PlatformScope(), Hashes: p.hashes, Keyring: p.master, HashConcurrency: p.cfg.hashConcurrency,
		HashWait: p.cfg.hashWait, LookupSlot: limits.KeyLookupSlot, Meter: tel.Metrics.Meter("axiom/accounts"),
		Recovery: recovery, Registration: registration, OnAccountOpened: openBalance, Cache: cache, LoginKey: p.loginKey,
		Egress: exit,
	})
	if err != nil {
		return nodeRoutes{}, err
	}
	if err := service.Load(ctx, logger); err != nil {
		return nodeRoutes{}, err
	}
	cat, err := catalog.Open(db, pg.PlatformScope(), catalog.Config{Logger: logger})
	if err != nil {
		return nodeRoutes{}, err
	}
	supplierAdmin, err := suppliers.NewAdmin(suppliers.AdminConfig{Keys: p.master, Egress: exit})
	if err != nil {
		return nodeRoutes{}, err
	}
	verify := claimedByCatalog(cat)
	if !p.cfg.modelVerification {
		verify = nil
	}
	engines, fileParser, err := p.plugins(ctx, db, exit)
	if err != nil {
		return nodeRoutes{}, err
	}
	calls, err := startCallPath(ctx, callDeps{
		db: db, keys: p.master, identity: p.identity, accounts: service, catalog: cat, egress: exit, search: engines,
		fileParser: fileParser,
		gate:       gate, limits: limits,
		records: recorder, meter: tel.Metrics.Meter("axiom/billing"), logger: logger, proxies: p.cfg.trustedProxies,
		kv:     cache,
		alerts: p.alerts, healthMode: p.cfg.healthMode, nodeID: p.cfg.valkey.NodeID, verify: verify,
	})
	if err != nil {
		return nodeRoutes{}, err
	}
	return p.listen(calls, service, cat, supplierAdmin, exit, db, tel, logger)
}

// listen opens payments and puts the console, the model calls, the key and payment APIs in front of the call path on
// the public listener, and the admin's API on its own.
func (p consoleParts) listen(calls callNode, service *accounts.Service, cat *catalog.Catalog, supplierAdmin *suppliers.Admin,
	exit *egress.Client, db *pg.DB, tel *telemetry.Telemetry, logger *slog.Logger,
) (nodeRoutes, error) {
	pay, err := payments.Open(payments.Config{
		DB: db, Platform: pg.PlatformScope(), Keyring: p.master, Client: exit, Logger: logger, Plugins: paymentPlugins, Proxies: p.cfg.trustedProxies,
		Alerts: p.alerts, Instance: p.cfg.valkey.NodeID, Signals: p.paymentSignals,
	})
	if err != nil {
		return nodeRoutes{}, err
	}
	public := withModelCalls(
		publicRoutes(consoleAPI{charges: billing.NewChargeReader(logger, tel.Metrics.Meter("axiom/billing")), exit: exit, version: version(), accounts: service, payments: pay, catalog: cat, db: db, models: calls.models, search: calls.search.List()},
			&guard{contract: p.contract, accounts: service, proxies: p.cfg.trustedProxies, logger: logger}, p.frontend, logger),
		calls.handler)
	public = withKeyManagement(public, managementAPI{accounts: service, proxies: p.cfg.trustedProxies, logger: logger})
	public = withPaymentCallbacks(public, pay.Notify())
	adminAPI := newAdminAPI(version(), service, cat, supplierAdmin, calls.upstreams, db, logger)
	adminAPI.keys, adminAPI.exit, adminAPI.smtp, adminAPI.search = p.master, exit, p.cfg.smtp, p.cfg.search
	adminAPI.alerts, adminAPI.instance, adminAPI.paymentSignals = p.alerts, p.cfg.valkey.NodeID, p.paymentSignals
	adminAPI.fileParser = p.cfg.fileParser
	admin := adminRoutes(adminAPI,
		&guard{contract: p.adminContract, accounts: service, proxies: p.cfg.trustedProxies, logger: logger, admin: true}, p.frontend, logger)
	return nodeRoutes{public: public, admin: admin, calls: calls}, nil
}

// search reads the node's engine list, falling back to the old single-backend row and then the environment.
func (p consoleParts) search(ctx context.Context, db *pg.DB) (loaded websearch.Loaded, err error) {
	err = db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		var e error
		loaded, e = websearch.Load(ctx, tx, p.master, p.cfg.search)
		return e
	})
	return loaded, err
}

// fileParser reads the parser backend this node converts files through: the admin's saved configuration when there is
// one, the environment's otherwise.
func (p consoleParts) fileParser(ctx context.Context, db *pg.DB) (fileparse.Config, error) {
	cfg := p.cfg.fileParser
	err := db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		stored, found, err := fileparse.StoredConfig(ctx, tx, p.master)
		if found {
			cfg = stored
		}
		return err
	})
	return cfg, err
}
