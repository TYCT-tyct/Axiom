package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"axiom/internal/platform/httpserver"
)

// startServing serves every listener the node's roles opened. The channel carries the error of the first listener to
// stop. Readiness is not reported here: a core waits for the routing snapshot (announceReady), and an ingress waits for
// its core (watchCore), so a node that cannot do its job never says it can.
func startServing(roles nodeRoles, servers []*listening, logger *slog.Logger) <-chan error {
	served := make(chan error, len(servers))
	attrs := []any{"roles", roles.String()}
	for _, s := range servers {
		go func() {
			if s.tls {
				served <- s.server.ServeTLS(s.listener, "", "") // the certificates are in TLSConfig
				return
			}
			served <- s.server.Serve(s.listener)
		}()
		attrs = append(attrs, listenerKey(s.variable), s.listener.Addr().String())
	}
	logger.Info("serving", attrs...)
	return served
}

// listenerKey is the log field each listener has always had: the tests and the deployment's checks read them by name.
func listenerKey(variable string) string {
	switch variable {
	case publicAddrVar:
		return "public"
	case adminAddrVar:
		return "admin"
	case nodeAddrVar:
		return "node"
	default:
		return "ops"
	}
}

// node returns a core node's servers in the order shutdown stops them. The public listener comes first, so at shutdown
// it drains while the probes still answer. The admin listener serves the admin app and the admin API, behind the source
// restriction; it exists only on a core node (#171). The node listener is the same call path over mutual TLS, for the
// ingress nodes' calls, and also answers the peers' readiness probes so an ingress knows whether the core can route.
// Every listener but the probes refuses a request that starts during the drain (httpserver.Health.Gate).
func node(cfg nodeSettings, routes http.Handler, admin http.Handler, health *httpserver.Health, logger *slog.Logger, requests httpserver.Telemetry) []*listening {
	servers := []*listening{
		{
			publicAddrVar, cfg.publicAddr,
			httpserver.New(httpserver.Public(health.Gate(routes), logger, bodyTimeout, httpserver.WithTelemetry(requests)), logger), nil, false,
		},
		{
			adminAddrVar, cfg.adminAddr,
			httpserver.New(httpserver.Public(health.Gate(restrictSources(admin, cfg.adminSources, cfg.trustedProxies)), logger, bodyTimeout,
				httpserver.WithTelemetry(requests)), logger), nil, false,
		},
	}
	if cfg.nodeTLS != nil {
		server := httpserver.New(httpserver.Public(withHealth(routes, health), logger, bodyTimeout,
			httpserver.WithTelemetry(requests)), logger)
		server.TLSConfig = cfg.nodeTLS
		servers = append(servers, &listening{nodeAddrVar, cfg.nodeAddr, server, nil, true})
	}
	return append(servers, &listening{probesAddrVar, cfg.probesAddr, health.Server(logger), nil, false})
}

// withHealth puts the two probes on the node listener in front of the call path, so an ingress can ask the core over
// mutual TLS whether it can route. Metrics stay on the probes listener, which is not exposed to peers. The gate is
// under the probes: /livez must keep answering while the node drains, and /readyz is what reports the drain.
func withHealth(routes http.Handler, health *httpserver.Health) http.Handler {
	mux := http.NewServeMux()
	probes := health.Handler()
	mux.Handle("/livez", probes)
	mux.Handle("/readyz", probes)
	mux.Handle("/", health.Gate(routes))
	return mux
}

// beginDrain stops taking new work and reports not ready, then waits for the load balancers in front to stop routing
// here before the next step closes the listeners. A node that was never ready has no traffic to wait for; one that was
// ready waits drainDelay.
func beginDrain(health *httpserver.Health, timeout time.Duration) {
	ready := health.Ready()
	health.BeginDrain()
	if ready {
		time.Sleep(drainDelay(timeout))
	}
}

// drainDelay is how long the edge has to notice that a ready node stopped accepting new work: a third of the shutdown
// deadline, at most 10 seconds, so the wait stays bounded even when the deadline is long.
func drainDelay(timeout time.Duration) time.Duration {
	return min(timeout/3, 10*time.Second)
}

// shutdown stops the servers in order, each finishing its requests in flight, all within the one deadline ctx carries.
// The public listener comes first, so the probes keep answering while it drains. Past the deadline a server's
// remaining connections are closed, which ends their requests' contexts: Shutdown alone would return and leave them
// running, and closing the database pool would then wait for every connection they hold.
func shutdown(ctx context.Context, servers []*listening) error {
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
