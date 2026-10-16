package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/platform/config"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
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

// opsAddr is where the node serves its probes: loopback unless a deployment needs otherwise.
func opsAddr(env *config.Env) string { return env.Addr("AXIOM_OPS_ADDR", "127.0.0.1:8081") }

// listening is one of the node's HTTP servers and the address variable it listens on.
type listening struct {
	variable, addr string
	server         *http.Server
	listener       net.Listener
}

// serve runs the node until ctx ends, then shuts down: the node reports not ready, its listeners stop accepting,
// and requests in flight get up to AXIOM_SHUTDOWN_TIMEOUT to finish. It refuses to start against a database whose
// login escapes row-level security, that lacks this program's migrations, or that does not answer within
// AXIOM_STARTUP_TIMEOUT.
func serve(ctx context.Context, environ []string, logger *slog.Logger) error {
	env := config.New(environ)
	publicAddr := env.Addr("AXIOM_PUBLIC_ADDR", ":8080") // model calls, the console, payment callbacks
	probesAddr := opsAddr(env)
	startupTimeout := env.Duration("AXIOM_STARTUP_TIMEOUT", defaultStartupTimeout)
	shutdownTimeout := env.Duration("AXIOM_SHUTDOWN_TIMEOUT", 30*time.Second)
	databaseURL := env.Required("AXIOM_DATABASE_URL")
	if err := env.Err(); err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	tel, err := telemetry.New()
	if err != nil {
		return err
	}
	defer tel.Shutdown(context.WithoutCancel(ctx)) //nolint:errcheck // nothing exports yet, so there is nothing to lose
	db, err := openNodeDatabase(ctx, databaseURL, startupTimeout, tel)
	if err != nil {
		return err
	}
	defer db.Close() // after the listeners and the recorder close: requests in flight may still need it

	recorder := startRecorder(ctx, db, tel, logger)
	// Every way out closes the recorder, or its writer would outlive a failed start. The normal shutdown closes it
	// earlier, after the listeners drain and with the shutdown deadline; Close is idempotent, so this one then
	// returns at once. It runs before db.Close, which was deferred first.
	defer closeRecorder(ctx, recorder, shutdownTimeout, logger)
	requests, err := httpserver.NewTelemetry(tel.Traces, tel.Metrics)
	if err != nil {
		return err
	}
	health := httpserver.Health{Metrics: tel.MetricsHandler()}
	servers := node(publicAddr, publicRoutes(consoleAPI{version: version()}, logger), probesAddr, &health, logger, requests)
	if err := listen(ctx, servers); err != nil {
		return err
	}
	served := make(chan error, len(servers))
	for _, s := range servers {
		go func() { served <- s.server.Serve(s.listener) }()
	}
	health.SetReady(true)
	logger.Info("serving", "public", servers[0].listener.Addr().String(), "ops", servers[1].listener.Addr().String())

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
	return errors.Join(stopped, shutdown(ctx, shutdownTimeout, &health, servers), recorder.Close(ctx))
}

// openNodeDatabase opens the database for serving, traced, and refuses it when its fallback prices fail billing's check
// (docs/domains/billing.md「价目表缺项」); billing reports its gauges from it.
func openNodeDatabase(ctx context.Context, url string, timeout time.Duration, tel *telemetry.Telemetry) (*pg.DB, error) {
	db, err := openDatabase(ctx, url, timeout, pg.WithTracerProvider(tel.Traces))
	if err != nil {
		return nil, err
	}
	err = db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error { return billing.CheckFallbacks(ctx, tx) })
	if err == nil {
		err = billing.RegisterMetrics(tel.Metrics.Meter("axiom/billing"), db, pg.PlatformScope())
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
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
// it drains while the probes still answer.
func node(publicAddr string, routes http.Handler, probesAddr string, health *httpserver.Health, logger *slog.Logger, requests httpserver.Telemetry) []*listening {
	return []*listening{
		{"AXIOM_PUBLIC_ADDR", publicAddr, httpserver.New(httpserver.Public(routes, logger, bodyTimeout, httpserver.WithTelemetry(requests)), logger), nil},
		{"AXIOM_OPS_ADDR", probesAddr, health.Server(logger), nil},
	}
}

// openDatabase connects to the database and checks it within timeout: a server that accepts connections and never
// answers would otherwise hold the node before its listeners open, where no probe or log reports it.
func openDatabase(ctx context.Context, url string, timeout time.Duration, opts ...pg.Option) (*pg.DB, error) {
	noAnswer := errors.New("database: AXIOM_DATABASE_URL: no answer within AXIOM_STARTUP_TIMEOUT")
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
