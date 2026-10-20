package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/gateway"
	"axiom/internal/observability"
	"axiom/internal/platform/config"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/platform/telemetry"
	"axiom/internal/platform/webui"
	"axiom/internal/suppliers"
)

// The call-record writer's sizes. The queue holds about what the node's in-flight limit admits (internal/gateway
// README「并发、背压与共享状态」); when it is full the recorder drops the new record rather than slow a call, so the
// queue only bounds memory and loss. Batches of 500 records or 200 ms keep the insert rate low (internal/observability
// README「写入」).
var callRecords = observability.Config{
	QueueSize: 5000, BatchSize: 500, FlushInterval: 200 * time.Millisecond, RetryBase: 100 * time.Millisecond,
	RetryMax:     5 * time.Second,
	WriteTimeout: 10 * time.Second,
}

// bodyTimeout bounds how long a client may take to send a request body. Generous for large uploads over slow
// links; the gateway sets size limits and per-route deadlines on top.
const bodyTimeout = 2 * time.Minute

// defaultStartupTimeout bounds how long a command waits for the database before it starts.
const defaultStartupTimeout = 30 * time.Second

// probesListenAddr is where the node serves its probes: loopback unless a deployment needs otherwise.
func probesListenAddr(env *config.Env) string { return env.Addr(probesAddrVar, "127.0.0.1:8081") }

// listening is one of the node's HTTP servers and the address variable it listens on.
type listening struct {
	variable, addr string
	server         *http.Server
	listener       net.Listener
}

// nodeSettings are the node's configuration, all read from the environment before anything starts.
type nodeSettings struct {
	publicAddr, adminAddr, probesAddr string
	startupTimeout, shutdownTimeout   time.Duration
	databaseURL                       string
	masterKeyFile, keyHashFile        string
	identityKeyFile                   string
	hashConcurrency                   int
	hashWait                          time.Duration
	trustedProxies                    []netip.Prefix
	smtp                              mail.Config
}

// readSettings reads and checks every variable the node uses, so a node started wrong fails with the whole list.
func readSettings(environ []string) (nodeSettings, error) {
	env := config.New(environ)
	s := nodeSettings{
		publicAddr:      env.Addr(publicAddrVar, ":8080"),         // model calls, the console, payment callbacks
		adminAddr:       env.Addr(adminAddrVar, "127.0.0.1:8082"), // the admin app; loopback until a deployment opens it
		probesAddr:      probesListenAddr(env),
		startupTimeout:  env.Duration(startupTimeoutVar, defaultStartupTimeout),
		shutdownTimeout: env.Duration(shutdownTimeoutVar, 30*time.Second),
		databaseURL:     env.Required(pg.DatabaseURLVar),
		masterKeyFile:   env.Required(masterKeyFileVar),
		keyHashFile:     env.Required(keyHashFileVar),
		// The identity key stands in for the client's session and end-user identifiers upstream, so rotating the key
		// hashes must not rotate it: the upstream's prompt caches are keyed by them (#205).
		identityKeyFile: env.Required(identityKeyFileVar),
		// The node's argon2id computations at once, and how long a sign-in waits for a turn (0: the module's defaults,
		// GOMAXPROCS and 2 seconds).
		hashConcurrency: env.Int(hashConcurrencyVar, 0, 1),
		hashWait:        env.Duration(hashWaitVar, 0),
		trustedProxies:  env.Prefixes(trustedProxiesVar),
	}
	// Email is optional: none of its variables set means the node starts with email disabled, and a partly set
	// configuration joins the problems above, so the node still reports everything wrong at once.
	smtp, mailErr := mail.ReadConfig(env)
	s.smtp = smtp
	if err := errors.Join(env.Err(), mailErr); err != nil {
		return nodeSettings{}, fmt.Errorf("configuration:\n%w", err)
	}
	return s, nil
}

// serve runs the node until ctx ends, then shuts down: the node reports not ready, its listeners stop accepting,
// and requests in flight and the jobs of the queue get up to AXIOM_SHUTDOWN_TIMEOUT to finish. It refuses to start against a database whose
// login escapes row-level security, that lacks this program's migrations, or that does not answer within
// AXIOM_STARTUP_TIMEOUT, with key files it cannot read, or with a console contract it cannot enforce.
func serve(ctx context.Context, environ []string, logger *slog.Logger) error {
	return serveVia(ctx, environ, logger, directEgress)
}

// directEgress is the client a node reaches its suppliers through in production.
func directEgress(tel *telemetry.Telemetry) (*egress.Client, error) {
	return egress.New(egress.Direct, egress.WithTracerProvider(tel.Traces))
}

// serveVia is serve with the client the node reaches its suppliers through: one pool of connections for the call path,
// the console's admin and the suppliers' sync. A test gives one that may reach a supplier on loopback.
func serveVia(ctx context.Context, environ []string, logger *slog.Logger, outbound func(*telemetry.Telemetry) (*egress.Client, error)) error {
	return serveWith(ctx, environ, logger, outbound, startRecorder)
}

// serveWith is serveVia with the constructor of the call-record writer, which a test wraps to get the recorder this
// call created.
func serveWith(ctx context.Context, environ []string, logger *slog.Logger, outbound func(*telemetry.Telemetry) (*egress.Client, error),
	record func(context.Context, *pg.DB, *telemetry.Telemetry, *slog.Logger) *observability.Recorder,
) error {
	cfg, err := readSettings(environ)
	if err != nil {
		return err
	}
	console, err := loadConsole(cfg)
	if err != nil {
		return err
	}
	tel, err := telemetry.New()
	if err != nil {
		return err
	}
	defer tel.Shutdown(context.WithoutCancel(ctx)) //nolint:errcheck // nothing exports yet, so there is nothing to lose
	db, err := openNodeDatabase(ctx, cfg.databaseURL, cfg.startupTimeout, tel)
	if err != nil {
		return err
	}
	defer db.Close() // after the listeners and the recorder close: requests in flight may still need it

	exit, err := outbound(tel)
	if err != nil {
		return err
	}
	recorder := record(ctx, db, tel, logger)
	// Every way out closes the recorder, or its writer would outlive a failed start. The normal shutdown closes it
	// earlier, after the listeners drain and with the shutdown deadline; Close is idempotent, so this one then
	// returns at once. It runs before db.Close, which was deferred first.
	defer closeRecorder(ctx, recorder, cfg.shutdownTimeout, logger)
	queue, err := startSync(ctx, db, cfg.smtp, console.master, exit, tel, logger)
	if err != nil {
		return err
	}
	// Like the recorder, the queue is stopped on every way out; the normal shutdown stops it earlier, with the shutdown
	// deadline, and a second Stop does nothing.
	defer stopQueue(ctx, queue, cfg.shutdownTimeout, logger)
	requests, err := httpserver.NewTelemetry(tel.Traces, tel.Metrics)
	if err != nil {
		return err
	}
	routes, err := console.routes(ctx, db, tel, logger, recorder, exit)
	if err != nil {
		return err
	}
	defer routes.calls.stop() // every way out ends the loops; the normal shutdown does it earlier, after the drain
	health := httpserver.Health{Metrics: tel.MetricsHandler()}
	servers := node(cfg.publicAddr, routes.public, cfg.adminAddr, routes.admin, cfg.probesAddr, &health, logger, requests)
	if err := listen(ctx, servers); err != nil {
		return err
	}
	served := startServing(servers, logger)
	// Not ready until a routing snapshot can route, which startCallPath has already published or will on its next
	// rebuild.
	stopAnnouncing := announceReady(ctx, routes.calls.ready, readinessEvery, &health, logger)
	stopped := awaitEnd(ctx, served, routes.calls.reaped)
	stopAnnouncing()
	logger.Info("shutting down", "timeout", cfg.shutdownTimeout.String())
	return errors.Join(stopped, stopNode(context.WithoutCancel(ctx), cfg.shutdownTimeout, &health, servers, routes.calls, queue, recorder))
}

// stopNode stops what serve started, in an order that loses nothing it need not: the listeners first (the probes keep
// answering, not ready), then the calls and their settlements, then the loops that keep the holder alive for them, then
// the queue and the recorder, which the calls may still have fed, the two together: a slow job then cannot use up the
// recorder's time. Each stage has a deadline of its own, so a long stream that uses the listeners' up does not take the
// recorder's with it. The worst case is timeout + drainWait + abortGrace + timeout (deploy/compose.yaml's
// stop_grace_period covers it).
func stopNode(ctx context.Context, timeout time.Duration, health *httpserver.Health, servers []*listening,
	calls callNode, queue *pg.Queue, recorder *observability.Recorder,
) error {
	err := shutdown(ctx, timeout, health, servers)
	calls.Drain(ctx)
	calls.stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var queueErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		queueErr = queue.Stop(ctx)
	}()
	recorderErr := recorder.Close(ctx)
	<-done
	return errors.Join(err, queueErr, recorderErr)
}

// awaitEnd waits until the node is asked to stop (nil), a listener stops, or the billing loop ends because this node's
// holder was reaped, and says which of the last two it was.
func awaitEnd(ctx context.Context, served <-chan error, reaped <-chan error) error {
	select {
	case err := <-served:
		return fmt.Errorf("a listener stopped: %w", err)
	case err := <-reaped:
		return fmt.Errorf("this node's holder is gone: %w", err)
	case <-ctx.Done():
		return nil
	}
}

// consoleParts are what the node builds from its configuration before it touches the database: the secrets read from
// protected files, whose errors name the variable and never the path or the contents, and the contract the guard
// enforces.
type consoleParts struct {
	cfg           nodeSettings
	master        *secret.Keyring // seals users' TOTP secrets and suppliers' credentials
	hashes        *secret.MACKeys // keys API keys are hashed with
	identity      []byte          // derives what replaces the client's identifiers upstream
	contract      *contract
	adminContract *contract
	frontend      *webui.Frontend // the pages of the three apps
}

func loadConsole(cfg nodeSettings) (consoleParts, error) {
	master, err := secret.Load(cfg.masterKeyFile)
	if err != nil {
		return consoleParts{}, err
	}
	hashes, err := secret.LoadMAC(cfg.keyHashFile)
	if err != nil {
		return consoleParts{}, err
	}
	identity, err := secret.LoadSecret(identityKeyFileVar, cfg.identityKeyFile)
	if err != nil {
		return consoleParts{}, err
	}
	console, err := consoleContract()
	if err != nil {
		return consoleParts{}, err
	}
	admin, err := adminContract()
	if err != nil {
		return consoleParts{}, err
	}
	frontend, err := webui.Embedded()
	return consoleParts{cfg, master, hashes, identity, console, admin, frontend}, err
}

// nodeRoutes are what the node's listeners serve and the call path behind the public one.
type nodeRoutes struct {
	public, admin http.Handler
	calls         callNode
}

// routes starts the accounts module on this node, which loads the sites, domains and roles and keeps them fresh until
// ctx ends, the call path, and puts the console's API in front of it for the public listener with the model API beside
// it, and the admin's API for the admin listener. Opening an account also opens its balance (openBalance).
func (p consoleParts) routes(ctx context.Context, db *pg.DB, tel *telemetry.Telemetry, logger *slog.Logger,
	recorder *observability.Recorder, exit *egress.Client,
) (nodeRoutes, error) {
	// The accounts module and the call path take their in-process limits from one value: a key lookup that misses the
	// cache uses a database connection like an admission does, so the two must share the counting, or a flood of
	// unknown keys would be bounded by nothing (gateway.Backpressure.KeyLookupSlot, which gateway.NewHandler requires).
	gate := gateway.DefaultConfig()
	limits, err := gateway.New(gate, tel.Metrics.Meter("axiom/gateway"))
	if err != nil {
		return nodeRoutes{}, err
	}
	service, err := accounts.NewService(db, accounts.Config{
		Platform: pg.PlatformScope(), Hashes: p.hashes, Keyring: p.master, HashConcurrency: p.cfg.hashConcurrency,
		HashWait: p.cfg.hashWait, LookupSlot: limits.KeyLookupSlot, Meter: tel.Metrics.Meter("axiom/accounts"),
		OnAccountOpened: openBalance,
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
	})
	if err != nil {
		return nodeRoutes{}, err
	}
	public := withModelCalls(
		publicRoutes(consoleAPI{version: version(), accounts: service, db: db},
			&guard{contract: p.contract, accounts: service, proxies: p.cfg.trustedProxies, logger: logger}, p.frontend, logger),
		calls.handler)
	admin := adminRoutes(newAdminAPI(version(), service, cat, supplierAdmin, db, logger),
		&guard{contract: p.adminContract, accounts: service, proxies: p.cfg.trustedProxies, logger: logger, admin: true}, p.frontend, logger)
	return nodeRoutes{public: public, admin: admin, calls: calls}, nil
}

// startSync starts the queue with supplier syncing, charge reconciliation, the ledger's daily check and the expiry of
// credit. It reaches the suppliers through exit, the client the whole node shares.
func startSync(ctx context.Context, db *pg.DB, smtp mail.Config, keys *secret.Keyring, exit *egress.Client, tel *telemetry.Telemetry, logger *slog.Logger) (*pg.Queue, error) {
	queue := db.NewQueue(logger, pg.PlatformScope(), tel.Metrics.Meter("axiom/pg"))
	// Outbound email rides the queue's default queue. Registering here also reports at startup whether this node can
	// send mail at all (docs/domains/accounts.md「给用户的邮件」).
	if _, err := mail.Register(ctx, db, queue, smtp, logger); err != nil {
		return nil, err
	}
	if err := suppliers.RegisterSync(suppliers.SyncConfig{
		DB: db, Scope: pg.PlatformScope(), Keys: keys, Egress: exit, Queue: queue, Logger: logger,
	}); err != nil {
		return nil, err
	}
	// The ledger checks itself once a day, on whichever node leads the queue (the check needs only the database, so it
	// is registered here and not with the call path, which starts after the queue).
	if err := billing.RegisterReconcile(billing.ReconcileConfig{
		DB: db, Scope: pg.PlatformScope(), Queue: queue, Logger: logger, Meter: tel.Metrics.Meter("axiom/billing"),
	}); err != nil {
		return nil, err
	}
	// Credit past its expiry is written off every minute by whichever node leads the queue (docs/domains/billing.md「额度批次」).
	if err := billing.RegisterExpire(billing.ExpireConfig{
		DB: db, Scope: pg.PlatformScope(), Queue: queue, Logger: logger, Meter: tel.Metrics.Meter("axiom/billing"),
	}); err != nil {
		return nil, err
	}
	if err := suppliers.RegisterReconcile(suppliers.ReconcileJobConfig{
		DB: db, Scope: pg.PlatformScope(), Queue: queue, Logger: logger,
	}); err != nil {
		return nil, err
	}
	if err := queue.Start(ctx); err != nil {
		return nil, err
	}
	return queue, nil
}

// stopQueue stops the queue within timeout, on a context that outlives the one that ended the node.
func stopQueue(ctx context.Context, queue *pg.Queue, timeout time.Duration, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := queue.Stop(ctx); err != nil {
		logger.Error("stopping the job queue", "err", err)
	}
}

// openNodeDatabase opens the database for serving, traced, and refuses it when its fallback prices fail billing's check
// (docs/domains/billing.md「价目表缺项」). One deadline covers the connection and the check, so a database that stops
// answering, or a lock held on the fallbacks, fails the start instead of holding it. The other commands open the
// database without the check: save-fallbacks is how a refused row is repaired.
func openNodeDatabase(ctx context.Context, url string, timeout time.Duration, tel *telemetry.Telemetry) (*pg.DB, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errNoAnswer)
	defer cancel()
	db, err := openDatabase(ctx, url, timeout, pg.WithTracerProvider(tel.Traces))
	if err != nil {
		return nil, err
	}
	err = db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error { return billing.CheckFallbacks(ctx, tx) })
	if err != nil && errors.Is(context.Cause(ctx), errNoAnswer) {
		err = errNoAnswer
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// startServing serves every listener. The channel carries the error of the first listener to stop. Readiness is not
// reported here: it waits for the routing snapshot (announceReady), so a node that cannot route never says it can.
func startServing(servers []*listening, logger *slog.Logger) <-chan error {
	served := make(chan error, len(servers))
	for _, s := range servers {
		go func() { served <- s.server.Serve(s.listener) }()
	}
	logger.Info("serving", "public", servers[0].listener.Addr().String(), "admin", servers[1].listener.Addr().String(), "ops", servers[2].listener.Addr().String())
	return served
}

// startRecorder starts the call-record writer, reporting to the node's metrics.
func startRecorder(ctx context.Context, db *pg.DB, tel *telemetry.Telemetry, logger *slog.Logger) *observability.Recorder {
	cfg := callRecords
	cfg.Meter = tel.Metrics.Meter("axiom/observability")
	return observability.NewRecorder(ctx, db, pg.PlatformScope(), cfg, logger)
}

// closeRecorder drains the recorder within timeout, on a context that outlives the one that ended the node.
func closeRecorder(ctx context.Context, recorder *observability.Recorder, timeout time.Duration, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := recorder.Close(ctx); err != nil {
		logger.Error("closing the call-record writer", "err", err)
	}
}

// node returns the node's servers in the order shutdown stops them. The public listener comes first, so at shutdown
// it drains while the probes still answer. The admin listener serves the admin app and the admin API (docs/architecture.md
// 「前端交付」); the limit to restricted sources is not built (#171).
func node(publicAddr string, routes http.Handler, adminAddr string, admin http.Handler, probesAddr string, health *httpserver.Health, logger *slog.Logger, requests httpserver.Telemetry) []*listening {
	return []*listening{
		{publicAddrVar, publicAddr, httpserver.New(httpserver.Public(routes, logger, bodyTimeout, httpserver.WithTelemetry(requests)), logger), nil},
		{adminAddrVar, adminAddr, httpserver.New(httpserver.Public(admin, logger, bodyTimeout, httpserver.WithTelemetry(requests)), logger), nil},
		{probesAddrVar, probesAddr, health.Server(logger), nil},
	}
}

// checkPool refuses a pool smaller than the permits the gateway hands out for database work. Its last
// Config.PoolReserve permits are kept for completions, and that reserve means nothing if the pool cannot answer it;
// a larger pool is left to the deployment. It runs in openDatabase, so every command that opens the database holds to
// the one pool configuration; the gateway's settings are its defaults until the call path is wired, so the check reads
// them there.
func checkPool(db *pg.DB) error {
	gate := gateway.DefaultConfig()
	if have := int(db.MaxConns()); have < gate.PoolSize {
		return fmt.Errorf("%s: pool_max_conns %d is below the gateway's %d database connections (PoolSize): raise it, or lower PoolSize",
			pg.DatabaseURLVar, have, gate.PoolSize)
	}
	return nil
}

var errNoAnswer = errors.New("database: " + pg.DatabaseURLVar + ": no answer within AXIOM_STARTUP_TIMEOUT")

// openDatabase connects to the database and checks it within timeout: a server that accepts connections and never
// answers would otherwise hold the node before its listeners open, where no probe or log reports it.
func openDatabase(ctx context.Context, url string, timeout time.Duration, opts ...pg.Option) (*pg.DB, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errNoAnswer)
	defer cancel()
	db, err := pg.Open(ctx, url, opts...)
	if err != nil {
		return nil, err
	}
	if err := checkPool(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.Check(ctx, modules()...); err != nil {
		db.Close()
		if errors.Is(context.Cause(ctx), errNoAnswer) {
			return nil, errNoAnswer // pgx reports only that the context ended
		}
		return nil, fmt.Errorf("database: %w", err)
	}
	if err := suppliers.CheckAdapters(ctx, db, pg.PlatformScope()); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// shutdown reports not ready, then stops the servers in order, each finishing its requests in flight, all within
// timeout. The public listener comes first, so the probes keep answering, not ready, while it drains. Past timeout
// a server's remaining connections are closed, which ends their requests' contexts: Shutdown alone would return and
// leave them running, and closing the database pool would then wait for every connection they hold.
func shutdown(ctx context.Context, timeout time.Duration, health *httpserver.Health, servers []*listening) error {
	health.SetReady(false)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var errs []error
	for _, s := range servers {
		if err := s.server.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shut down %s: %w", s.variable, err), s.server.Close())
		}
	}
	return errors.Join(errs...)
}

// listen binds every server before any serves, so a taken address fails startup with nothing half started. A
// failure names the variable and the cause, such as "bind: address already in use", but not the address.
func listen(ctx context.Context, servers []*listening) error {
	for i, s := range servers {
		listener, err := new(net.ListenConfig).Listen(ctx, "tcp", s.addr)
		if err != nil {
			if opErr := (*net.OpError)(nil); errors.As(err, &opErr) {
				err = opErr.Err
			}
			errs := []error{fmt.Errorf("%s: %w", s.variable, err)}
			for _, bound := range servers[:i] {
				errs = append(errs, bound.listener.Close())
			}
			return errors.Join(errs...)
		}
		s.listener = listener
	}
	return nil
}
