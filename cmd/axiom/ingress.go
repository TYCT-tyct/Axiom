package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/telemetry"
)

// serveWithoutCore runs a node whose roles need no state: an ingress forwards every request to a core over mutual TLS,
// and an ops node serves its probes while the operations interface is still to be built. Neither touches the database,
// the key ring or the job queue, so an ingress machine holds no tenant data and its compromise cannot read or write
// any (docs/architecture.md「部署拓扑」).
func serveWithoutCore(ctx context.Context, cfg nodeSettings, logger *slog.Logger) error {
	tel, err := telemetry.New()
	if err != nil {
		return err
	}
	defer tel.Shutdown(context.WithoutCancel(ctx)) //nolint:errcheck // nothing exports yet, so there is nothing to lose
	requests, err := requestTelemetry(tel)
	if err != nil {
		return err
	}
	health := httpserver.Health{Metrics: tel.MetricsHandler()}
	var transport *http.Transport
	if cfg.roles.has(roleIngress) {
		transport = ingressTransport(cfg.clientTLS)
	}
	servers := nonCoreServers(cfg, transport, &health, requests, logger)
	if err := listen(ctx, servers); err != nil {
		return err
	}
	if transport == nil { // an ops node has nothing to wait for: it is ready as soon as its probes answer
		health.SetReady(true)
	}
	served := startServing(cfg.roles, servers, logger)
	var stopWatching func()
	if transport != nil {
		stopWatching = watchCore(ctx, cfg.coreURL, ingressClient(transport), readinessEvery, &health, logger)
	}
	ended := awaitEnd(ctx, served, nil, nil)
	if stopWatching != nil {
		stopWatching()
	}
	logger.Info("shutting down", "timeout", cfg.shutdownTimeout.String())
	return errors.Join(ended, shutdown(context.WithoutCancel(ctx), cfg.shutdownTimeout, &health, servers))
}

// nonCoreServers is the ingress's or the ops node's listeners in the order shutdown stops them: the public proxy first,
// then the probes, which keep answering while it drains.
func nonCoreServers(cfg nodeSettings, transport http.RoundTripper, health *httpserver.Health, requests httpserver.Telemetry, logger *slog.Logger) []*listening {
	var servers []*listening
	if cfg.roles.has(roleIngress) {
		servers = append(servers, &listening{
			publicAddrVar, cfg.publicAddr,
			httpserver.New(httpserver.Public(ingressHandler(cfg.coreURL, transport, logger), logger, bodyTimeout,
				httpserver.WithTelemetry(requests)), logger), nil, false,
		})
	}
	return append(servers, &listening{probesAddrVar, cfg.probesAddr, health.Server(logger), nil, false})
}

// ingressTransport is the ingress's one connection pool to its core: HTTP/2 over mutual TLS, no proxy from the
// environment (node-to-node traffic is never routed through a third party), and no response-header timeout, because a
// model's first token can take as long as the core allows and the core enforces its own deadlines.
func ingressTransport(tlsConfig *tls.Config) *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       tlsConfig,
		ForceAttemptHTTP2:     true,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		IdleConnTimeout:       2 * time.Minute,
		ExpectContinueTimeout: time.Second,
		MaxIdleConnsPerHost:   64,
	}
}

// ingressHandler forwards the public listener's requests to the core. It streams: a flush after every write keeps an
// event stream arriving token by token, and a client's disconnect cancels the core's request through the request
// context, as a stop needs. The client's Host is passed through, because the core resolves the site and the surface by
// it; X-Forwarded-For is appended by the reverse proxy, so the core can still read the client's address when it trusts
// this node.
func ingressHandler(core *url.URL, transport http.RoundTripper, logger *slog.Logger) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(core)
	proxy.Transport = transport
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Warn("forwarding to the core node", "request_id", httpserver.RequestID(r.Context()), "err", err)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusBadGateway)
		if _, err := io.WriteString(w, `{"error":{"message":"the core node did not answer","type":"upstream_error"}}`); err != nil {
			logger.Warn("writing the forward error", "request_id", httpserver.RequestID(r.Context()), "err", err)
		}
	}
	return proxy
}

// ingressClient checks the core's readiness. It never follows a redirect: only the configured address may answer for
// the core.
func ingressClient(transport *http.Transport) *http.Client {
	return &http.Client{
		Transport:     transport,
		Timeout:       probeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// watchCore keeps this ingress's readiness equal to the core's: a node that cannot reach a core that can route is not
// ready, so the load balancer in front stops sending it traffic. One mutual-TLS request per period; only a change is
// logged, so a long outage does not fill the log.
func watchCore(ctx context.Context, core *url.URL, client *http.Client, every time.Duration, health *httpserver.Health, logger *slog.Logger) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	ready := false
	checked := false
	check := func() {
		ok, err := coreReady(ctx, client, core.JoinPath("readyz"))
		if checked && ok == ready {
			return
		}
		checked = true
		ready = ok
		health.SetReady(ok)
		if ok {
			logger.Info("the core node is ready")
			return
		}
		logger.Warn("the core node is not ready", "err", err)
	}
	go func() {
		defer close(done)
		check()
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check()
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// coreReady asks the core's node listener for /readyz and reports whether it answered 200.
func coreReady(ctx context.Context, client *http.Client, target *url.URL) (bool, error) {
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close() //nolint:errcheck // a probe's body has nothing worth reading.
	return resp.StatusCode == http.StatusOK, nil
}
