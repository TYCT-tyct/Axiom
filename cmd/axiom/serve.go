package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"axiom/internal/observability"
	"axiom/internal/platform/config"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/platform/telemetry"
	"axiom/internal/platform/webui"
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

// serve runs the node until ctx ends, then shuts down: the node reports not ready, its listeners stop accepting,
// and requests in flight and the jobs of the queue get up to AXIOM_SHUTDOWN_TIMEOUT to finish. It refuses to start
// without a master key file it can read (AXIOM_MASTER_KEY_FILE), or against a database whose login escapes row-level
// security, that lacks this program's migrations, or that does not answer within AXIOM_STARTUP_TIMEOUT.
func serve(ctx context.Context, environ []string, logger *slog.Logger) error {
	cfg, err := readSettings(environ)
	if err != nil {
		return err
	}
	shutdownTimeout := cfg.shutdownTimeout
	frontend, err := webui.Embedded()
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
	defer closeRecorder(ctx, recorder, shutdownTimeout, logger)
	queue, err := startSync(ctx, db, cfg.keys, tel, logger)
	if err != nil {
		return err
	}
	// Like the recorder, the queue is stopped on every way out; the normal shutdown stops it earlier, with the shutdown
	// deadline, and a second Stop does nothing.
	defer stopQueue(ctx, queue, shutdownTimeout, logger)
	requests, err := httpserver.NewTelemetry(tel.Traces, tel.Metrics)
	if err != nil {
		return err
	}
	health := httpserver.Health{Metrics: tel.MetricsHandler()}
	servers := node(cfg.publicAddr, publicRoutes(consoleAPI{version: version()}, frontend, logger), cfg.adminAddr, frontend.Admin(), cfg.probesAddr, &health, logger, requests)
	if err := listen(ctx, servers); err != nil {
		return err
	}
	served := startServing(servers, &health, logger)

	var stopped error
	select {
	case err := <-served:
		stopped = fmt.Errorf("a listener stopped: %w", err)
	case <-ctx.Done():
	}
	logger.Info("shutting down", "timeout", shutdownTimeout.String())
	// One deadline for the whole shutdown: the recorder drains after the listeners stop, when no request can enqueue.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	return errors.Join(stopped, shutdown(ctx, shutdownTimeout, &health, servers), queue.Stop(ctx), recorder.Close(ctx))
}

// settings are what serve reads from its environment.
type settings struct {
	publicAddr, adminAddr, probesAddr, databaseURL string
	startupTimeout, shutdownTimeout                time.Duration
	keys                                           *secret.Keyring
}

// readSettings reads every variable of serve and the master key file they name, so that a node with anything wrong
// starts no part of itself, and reports all the wrong variables at once.
func readSettings(environ []string) (settings, error) {
	env := config.New(environ)
	s := settings{
		publicAddr:      env.Addr("AXIOM_PUBLIC_ADDR", ":8080"),         // model calls, the console, payment callbacks
		adminAddr:       env.Addr("AXIOM_ADMIN_ADDR", "127.0.0.1:8082"), // the admin app; loopback until a deployment opens it
		probesAddr:      probesListenAddr(env),
		startupTimeout:  env.Duration("AXIOM_STARTUP_TIMEOUT", defaultStartupTimeout),
		shutdownTimeout: env.Duration("AXIOM_SHUTDOWN_TIMEOUT", 30*time.Second),
		databaseURL:     env.Required("AXIOM_DATABASE_URL"),
	}
	masterKeyFile := env.Required("AXIOM_MASTER_KEY_FILE")
	if err := env.Err(); err != nil {
		return s, fmt.Errorf("configuration:\n%w", err)
	}
	var err error
	if s.keys, err = secret.Load(masterKeyFile); err != nil { // after Err: a missing variable is reported once
		return s, fmt.Errorf("configuration:\n%w", err)
	}
	return s, nil
}

// startSync starts the queue with the syncing of the suppliers on it. It reaches the suppliers from the node's own
// address (egress.Direct).
func startSync(ctx context.Context, db *pg.DB, keys *secret.Keyring, tel *telemetry.Telemetry, logger *slog.Logger) (*pg.Queue, error) {
	exit, err := egress.New(egress.Direct, egress.WithTracerProvider(tel.Traces))
	if err != nil {
		return nil, err
	}
	queue := db.NewQueue(logger)
	if err := suppliers.RegisterSync(suppliers.SyncConfig{
		DB: db, Scope: pg.PlatformScope(), Keys: keys, Egress: exit, Queue: queue, Logger: logger,
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

// startServing serves every listener and reports the node ready. The channel carries the error of the first listener to
// stop.
func startServing(servers []*listening, health *httpserver.Health, logger *slog.Logger) <-chan error {
	served := make(chan error, len(servers))
	for _, s := range servers {
		go func() { served <- s.server.Serve(s.listener) }()
	}
	health.SetReady(true)
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
// it drains while the probes still answer. The admin listener serves the admin app alone (docs/architecture.md
// 「前端交付」); its API and the limits to the admin domain and to restricted sources come with the admin API.
func node(publicAddr string, routes http.Handler, adminAddr string, admin http.Handler, probesAddr string, health *httpserver.Health, logger *slog.Logger, requests httpserver.Telemetry) []*listening {
	return []*listening{
		{"AXIOM_PUBLIC_ADDR", publicAddr, httpserver.New(httpserver.Public(routes, logger, bodyTimeout, httpserver.WithTelemetry(requests)), logger), nil},
		{"AXIOM_ADMIN_ADDR", adminAddr, httpserver.New(httpserver.Public(admin, logger, bodyTimeout, httpserver.WithTelemetry(requests)), logger), nil},
		{"AXIOM_PROBES_ADDR", probesAddr, health.Server(logger), nil},
	}
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
