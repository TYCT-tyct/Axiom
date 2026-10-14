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

// opsAddr is where the node serves its probes: loopback unless a deployment needs otherwise.
func opsAddr(env *config.Env) string { return env.Addr("AXIOM_OPS_ADDR", "127.0.0.1:8081") }

// serve runs the node until ctx ends, then shuts down: the node reports not ready, its listeners stop accepting,
// and requests in flight get up to AXIOM_SHUTDOWN_TIMEOUT to finish. It refuses to start against a database whose
// login escapes row-level security or that lacks this program's migrations.
func serve(ctx context.Context, environ []string, logger *slog.Logger) error {
	env := config.New(environ)
	addr := opsAddr(env)
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
	ops := health.Server(logger)
	// Listening before reporting ready turns a taken address into a startup failure.
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
	if err != nil {
		if opErr := (*net.OpError)(nil); errors.As(err, &opErr) {
			err = opErr.Err // the cause, such as "bind: address already in use", without the address
		}
		return fmt.Errorf("AXIOM_OPS_ADDR: %w", err)
	}
	served := make(chan error, 1)
	go func() { served <- ops.Serve(listener) }()
	health.SetReady(true)
	logger.Info("serving", "ops", listener.Addr().String())

	select {
	case err := <-served:
		return fmt.Errorf("ops listener: %w", err)
	case <-ctx.Done():
	}
	health.SetReady(false)
	logger.Info("shutting down", "timeout", shutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := ops.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down the ops listener: %w", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("ops listener: %w", err)
	}
	return nil
}
