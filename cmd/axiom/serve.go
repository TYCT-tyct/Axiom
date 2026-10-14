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
)

// serve runs the node until ctx ends, then shuts down: the node reports not ready, its listeners stop accepting,
// and requests in flight get up to AXIOM_SHUTDOWN_TIMEOUT to finish.
func serve(ctx context.Context, environ []string, logger *slog.Logger) error {
	env := config.New(environ)
	opsAddr := env.String("AXIOM_OPS_ADDR", "127.0.0.1:8081") // probes and metrics; loopback unless deployed otherwise
	shutdownTimeout := env.Duration("AXIOM_SHUTDOWN_TIMEOUT", 30*time.Second)
	if err := env.Err(); err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}

	var health httpserver.Health
	ops := httpserver.New(opsAddr, health.Handler(), logger)
	// Listening before reporting ready turns a taken or malformed address into a startup failure.
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", opsAddr)
	if err != nil {
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
