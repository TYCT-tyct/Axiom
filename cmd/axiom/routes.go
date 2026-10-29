package main

import (
	"context"
	"log/slog"
	"net/http"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/gateway"
	"axiom/internal/observability"
	"axiom/internal/payments"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/telemetry"
	"axiom/internal/suppliers"
)

// nodeRoutes are what the node's listeners serve and the call path behind the public one.
type nodeRoutes struct {
	public, admin http.Handler
	calls         callNode
}

// routes starts the accounts module on this node, which loads the sites, domains and roles and keeps them fresh until
// ctx ends, the call path, and puts the console's API in front of it for the public listener with the model API beside
// it, and the admin's API for the admin listener. Opening an account also opens its balance (openBalance).
func (p consoleParts) routes(ctx context.Context, db *pg.DB, tel *telemetry.Telemetry, logger *slog.Logger,
	recorder *observability.Recorder, exit *egress.Client, cache *kv.Client, recovery *accounts.PasswordRecovery,
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
		Recovery: recovery, OnAccountOpened: openBalance, Cache: cache, LoginKey: p.loginKey,
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
	calls, err := startCallPath(ctx, callDeps{
		db: db, keys: p.master, identity: p.identity, accounts: service, catalog: cat, egress: exit, gate: gate, limits: limits,
		records: recorder, meter: tel.Metrics.Meter("axiom/billing"), logger: logger, proxies: p.cfg.trustedProxies,
		kv:     cache,
		alerts: p.alerts, healthMode: p.cfg.healthMode,
	})
	if err != nil {
		return nodeRoutes{}, err
	}
	pay, err := payments.Open(payments.Config{
		DB: db, Platform: pg.PlatformScope(), Keyring: p.master, Client: exit, Logger: logger, Plugins: paymentPlugins, Proxies: p.cfg.trustedProxies,
	})
	if err != nil {
		return nodeRoutes{}, err
	}
	public := withModelCalls(
		publicRoutes(consoleAPI{version: version(), accounts: service, payments: pay, db: db},
			&guard{contract: p.contract, accounts: service, proxies: p.cfg.trustedProxies, logger: logger}, p.frontend, logger),
		calls.handler)
	public = withPaymentCallbacks(public, pay.Notify())
	adminAPI := newAdminAPI(version(), service, cat, supplierAdmin, calls.upstreams, db, logger)
	adminAPI.keys, adminAPI.exit = p.master, exit
	admin := adminRoutes(adminAPI,
		&guard{contract: p.adminContract, accounts: service, proxies: p.cfg.trustedProxies, logger: logger, admin: true}, p.frontend, logger)
	return nodeRoutes{public: public, admin: admin, calls: calls}, nil
}
