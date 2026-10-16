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
	"axiom/internal/observability"
	"axiom/internal/platform/config"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/platform/telemetry"
	"axiom/internal/suppliers"
)

// The call-record writer's sizes. The queue holds about what the node's in-flight limit admits (internal/gateway
// README「并发、背压与共享状态」), so a request waits for room only when the database is far behind; batches
// of 500 rows or 200 ms keep the insert rate low (internal/observability README「写入」).
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
func probesListenAddr(env *config.Env) string { return env.Addr("AXIOM_PROBES_ADDR", "127.0.0.1:8081") }

// listening is one of the node's HTTP servers and the address variable it listens on.
type listening struct {
	variable, addr string
	server         *http.Server
	listener       net.Listener
}

// nodeSettings are the node's configuration, all read from the environment before anything starts.
type nodeSettings struct {
	publicAddr, probesAddr string
	// adminAddr is where the admin listener serves; empty, as on a node that is not the core, there is none.
	adminAddr                       string
	startupTimeout, shutdownTimeout time.Duration
	databaseURL                     string
	masterKeyFile, keyHashFile      string
	hashConcurrency                 int
	hashWait                        time.Duration
	trustedProxies                  []netip.Prefix
}

// readSettings reads and checks every variable the node uses, so a node started wrong fails with the whole list.
func readSettings(environ []string) (nodeSettings, error) {
	env := config.New(environ)
	s := nodeSettings{
		publicAddr:      env.Addr("AXIOM_PUBLIC_ADDR", ":8080"), // model calls, the console, payment callbacks
		probesAddr:      probesListenAddr(env),
		adminAddr:       env.Addr("AXIOM_ADMIN_ADDR", ""), // the admin API, only on the core
		startupTimeout:  env.Duration("AXIOM_STARTUP_TIMEOUT", defaultStartupTimeout),
		shutdownTimeout: env.Duration("AXIOM_SHUTDOWN_TIMEOUT", 30*time.Second),
		databaseURL:     env.Required(pg.DatabaseURLVar),
		masterKeyFile:   env.Required("AXIOM_MASTER_KEY_FILE"),
		keyHashFile:     env.Required("AXIOM_KEY_HASH_FILE"),
		// The node's argon2id computations at once, and how long a sign-in waits for a turn (0: the module's defaults,
		// GOMAXPROCS and 2 seconds).
		hashConcurrency: env.Int("AXIOM_HASH_CONCURRENCY", 0, 1),
		hashWait:        env.Duration("AXIOM_HASH_WAIT", 0),
		trustedProxies:  env.Prefixes("AXIOM_TRUSTED_PROXIES"),
	}
	if err := env.Err(); err != nil {
		return nodeSettings{}, fmt.Errorf("configuration:\n%w", err)
	}
	return s, nil
}

// serve runs the node until ctx ends, then shuts down: the node reports not ready, its listeners stop accepting,
// and requests in flight get up to AXIOM_SHUTDOWN_TIMEOUT to finish. It refuses to start against a database whose
// login escapes row-level security, that lacks this program's migrations, or that does not answer within
// AXIOM_STARTUP_TIMEOUT, with key files it cannot read, or with a console contract it cannot enforce.
func serve(ctx context.Context, environ []string, logger *slog.Logger) error {
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
	db, err := openDatabase(ctx, cfg.databaseURL, cfg.startupTimeout, pg.WithTracerProvider(tel.Traces))
	if err != nil {
		return err
	}
	defer db.Close() // after the listeners and the recorder close: requests in flight may still need it

	recorder := startRecorder(ctx, db, tel, logger)
	// Every way out closes the recorder, or its writer would outlive a failed start. The normal shutdown closes it
	// earlier, after the listeners drain and with the shutdown deadline; Close is idempotent, so this one then
	// returns at once. It runs before db.Close, which was deferred first.
	defer closeRecorder(ctx, recorder, cfg.shutdownTimeout, logger)
	requests, err := httpserver.NewTelemetry(tel.Traces, tel.Metrics)
	if err != nil {
		return err
	}
	routes, adminRoutes, err := console.routes(ctx, db, tel, logger)
	if err != nil {
		return err
	}
	health := httpserver.Health{Metrics: tel.MetricsHandler()}
	servers := node(cfg.publicAddr, routes, cfg.adminAddr, adminRoutes, cfg.probesAddr, &health, logger, requests)
	if err := listen(ctx, servers); err != nil {
		return err
	}
	served := make(chan error, len(servers))
	for _, s := range servers {
		go func() { served <- s.server.Serve(s.listener) }()
	}
	health.SetReady(true)
	logger.Info("serving", servedAddrs(servers)...)

	var stopped error
	select {
	case err := <-served:
		stopped = fmt.Errorf("a listener stopped: %w", err)
	case <-ctx.Done():
	}
	logger.Info("shutting down", "timeout", cfg.shutdownTimeout.String())
	// One deadline for the whole shutdown: the recorder drains after the listeners stop, when no request can enqueue.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.shutdownTimeout)
	defer cancel()
	return errors.Join(stopped, shutdown(ctx, cfg.shutdownTimeout, &health, servers), recorder.Close(ctx))
}

// consoleParts are what the console's API needs that the node can check before it touches the database: the secrets
// read from protected files, whose errors name the variable and never the path or the contents, and the contract
// the guard enforces. The admin API, when the node serves it, stands on the same accounts module and the same files.
type consoleParts struct {
	cfg           nodeSettings
	master        *secret.Keyring // seals users' TOTP secrets
	hashes        *secret.MACKeys // keys API keys are hashed with
	contract      *contract
	adminContract *contract
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
	console, err := consoleContract()
	if err != nil {
		return consoleParts{}, err
	}
	admin, err := adminContract()
	return consoleParts{cfg, master, hashes, console, admin}, err
}

// routes starts the accounts module on this node, which loads the sites, domains and roles and keeps them fresh until
// ctx ends, and puts the console's API in front of it, and the admin API, which is nil unless the node serves one.
// Opening an account also opens its balance (openBalance).
func (p consoleParts) routes(ctx context.Context, db *pg.DB, tel *telemetry.Telemetry, logger *slog.Logger) (public, admin http.Handler, err error) {
	service, err := accounts.NewService(db, accounts.Config{
		Platform: pg.PlatformScope(), Hashes: p.hashes, Keyring: p.master, HashConcurrency: p.cfg.hashConcurrency,
		HashWait: p.cfg.hashWait, Meter: tel.Metrics.Meter("axiom/accounts"), OnAccountOpened: openBalance,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := service.Load(ctx, logger); err != nil {
		return nil, nil, err
	}
	public = publicRoutes(consoleAPI{version: version(), accounts: service, db: db},
		&guard{contract: p.contract, accounts: service, proxies: p.cfg.trustedProxies, logger: logger}, logger)
	if p.cfg.adminAddr != "" {
		admin = adminRoutes(adminAPI{version: version(), accounts: service, db: db},
			&guard{contract: p.adminContract, accounts: service, proxies: p.cfg.trustedProxies, logger: logger, admin: true}, logger)
	}
	return public, admin, nil
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

// node returns the node's servers in the order shutdown stops them. The public and admin listeners come first, so at
// shutdown they drain while the probes still answer. The admin listener is there when adminRoutes is.
func node(publicAddr string, routes http.Handler, adminAddr string, adminRoutes http.Handler, probesAddr string, health *httpserver.Health, logger *slog.Logger, requests httpserver.Telemetry) []*listening {
	server := func(routes http.Handler) *http.Server {
		return httpserver.New(httpserver.Public(routes, logger, bodyTimeout, httpserver.WithTelemetry(requests)), logger)
	}
	servers := []*listening{{"AXIOM_PUBLIC_ADDR", publicAddr, server(routes), nil}}
	if adminRoutes != nil {
		servers = append(servers, &listening{"AXIOM_ADMIN_ADDR", adminAddr, server(adminRoutes), nil})
	}
	return append(servers, &listening{"AXIOM_PROBES_ADDR", probesAddr, health.Server(logger), nil})
}

// servedAddrs are the addresses the node listens on, named for the log line that reports them: public, admin when
// the node serves one, and probes.
func servedAddrs(servers []*listening) []any {
	names := map[string]string{"AXIOM_PUBLIC_ADDR": "public", "AXIOM_ADMIN_ADDR": "admin", "AXIOM_PROBES_ADDR": "probes"}
	var attrs []any
	for _, s := range servers {
		attrs = append(attrs, names[s.variable], s.listener.Addr().String())
	}
	return attrs
}

// openDatabase connects to the database and checks it within timeout: a server that accepts connections and never
// answers would otherwise hold the node before its listeners open, where no probe or log reports it.
func openDatabase(ctx context.Context, url string, timeout time.Duration, opts ...pg.Option) (*pg.DB, error) {
	noAnswer := errors.New("database: " + pg.DatabaseURLVar + ": no answer within AXIOM_STARTUP_TIMEOUT")
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, noAnswer)
	defer cancel()
	db, err := pg.Open(ctx, url, opts...)
	if err != nil {
		return nil, err
	}
	if err := db.Check(ctx, modules()...); err != nil {
		db.Close()
		if errors.Is(context.Cause(ctx), noAnswer) {
			return nil, noAnswer // pgx reports only that the context ended
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
