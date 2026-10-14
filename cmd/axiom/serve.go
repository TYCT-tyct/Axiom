package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"axiom/internal/platform/config"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
)

// bodyTimeout bounds how long a client may take to send a request body. Generous for large uploads over slow
// links; the gateway sets size limits and per-route deadlines on top.
const bodyTimeout = 2 * time.Minute

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
// login escapes row-level security or that lacks this program's migrations.
func serve(ctx context.Context, environ []string, logger *slog.Logger) error {
	env := config.New(environ)
	publicAddr := env.Addr("AXIOM_PUBLIC_ADDR", ":8080") // model calls, the console, payment callbacks
	probesAddr := opsAddr(env)
	shutdownTimeout := env.Duration("AXIOM_SHUTDOWN_TIMEOUT", 30*time.Second)
	databaseURL := env.Required("AXIOM_DATABASE_URL")
	if err := env.Err(); err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	db, err := pg.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer db.Close() // after the listeners close: requests in flight may still need it
	if err := db.Check(ctx); err != nil {
		return fmt.Errorf("database: %w", err)
	}

	var health httpserver.Health
	// The public listener comes first, so at shutdown it drains while the probes still answer.
	servers := []*listening{
		{"AXIOM_PUBLIC_ADDR", publicAddr, httpserver.New(httpserver.Public(publicRoutes(logger), logger, bodyTimeout), logger), nil},
		{"AXIOM_OPS_ADDR", probesAddr, health.Server(logger), nil},
	}
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
	return errors.Join(stopped, shutdown(context.WithoutCancel(ctx), shutdownTimeout, &health, servers))
}

// shutdown reports not ready, then stops the servers in order, each finishing its requests in flight, all within
// timeout. The public listener comes first, so the probes keep answering, not ready, while it drains.
func shutdown(ctx context.Context, timeout time.Duration, health *httpserver.Health, servers []*listening) error {
	health.SetReady(false)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var errs []error
	for _, s := range servers {
		if err := s.server.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shut down %s: %w", s.variable, err))
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
