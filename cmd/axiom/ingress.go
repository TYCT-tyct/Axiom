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
	"sync"
	"time"

	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/telemetry"
)

const (
	// ingressHeaderLimit bounds the wait for the core's response headers. The core's own gateway allows 10 minutes
	// for a compact response's headers (CompactHeaderWait) and 60 seconds otherwise (HeaderWait); 15 minutes leaves
	// room for admission and the request's trip without letting a stopped core hold the ingress forever.
	ingressHeaderLimit = 15 * time.Minute
	// ingressIdleLimit bounds a silence in the response body. The core's gateway cuts an idle upstream at 3 minutes
	// (IdleTimeout); 5 minutes lets a live core make that decision first while a stopped one cannot hang the ingress.
	ingressIdleLimit = 5 * time.Minute
	// ingressRequestLimit bounds the whole forwarded request. The core's longest call is 60 minutes (MaxCall); 65
	// minutes lets it finish and settle while a slow client or a wedged stream still ends.
	ingressRequestLimit = 65 * time.Minute
	// ingressWriteLimit bounds one write or flush to a client that has stopped reading. The request's context does not
	// interrupt a blocked socket write, and httpserver.New sets no write timeout because streams may be long; a write
	// deadline is what releases the handler. It matches the core's WriteTimeout (10 seconds).
	ingressWriteLimit = 10 * time.Second
)

// serveWithoutCore runs a node whose roles need no state: an ingress forwards every request to a core over mutual TLS,
// and an ops node serves its probes and, when configured, the MCP bridge, which forwards the agent's tool calls to a
// core's operations listener. Neither touches the database, the key ring or the job queue, so such a machine holds no
// tenant data and its compromise cannot read or write any (docs/architecture.md「部署拓扑」).
func serveWithoutCore(ctx context.Context, cfg nodeSettings, logger *slog.Logger) error {
	servers := listeners(cfg)
	if err := listen(ctx, servers); err != nil {
		return err
	}
	defer closeListeners(servers)
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
	var bridge http.Handler
	if cfg.mcp != nil {
		startCtx, cancel := context.WithTimeout(ctx, cfg.startupTimeout)
		bridge, err = startBridge(startCtx, *cfg.mcp, ingressTransport(cfg.mcp.tls), logger)
		cancel()
		if err != nil {
			return err
		}
	}
	nonCoreServers(servers, cfg, transport, bridge, &health, requests, logger)
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
	logger.Info("shutting down", "timeout", cfg.shutdownTimeout.String(), "edge_delay", drainDelay(cfg.shutdownTimeout).String())
	beginDrain(&health, cfg.shutdownTimeout)
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.shutdownTimeout)
	defer cancel()
	return errors.Join(ended, shutdown(drainCtx, servers))
}

// nonCoreServers attaches the ingress's or the ops node's handlers to the listeners startup claimed. The public proxy
// comes first, then the MCP bridge, then the probes, which keep answering while the others drain. The public proxy and
// the bridge refuse a request that starts during the drain.
func nonCoreServers(servers []*listening, cfg nodeSettings, transport http.RoundTripper, bridge http.Handler, health *httpserver.Health, requests httpserver.Telemetry, logger *slog.Logger) {
	for _, s := range servers {
		switch s.variable {
		case publicAddrVar:
			s.server = httpserver.New(httpserver.Public(health.Gate(ingressHandler(cfg.coreURL, transport, logger)), logger, bodyTimeout,
				httpserver.WithTelemetry(requests)), logger)
		case mcpAddrVar:
			s.server = httpserver.New(httpserver.Public(health.Gate(bridge), logger, bodyTimeout, httpserver.WithTelemetry(requests)), logger)
		case probesAddrVar:
			s.server = health.Server(logger)
		}
	}
}

// ingressTransport is the ingress's one connection pool to its core: HTTP/2 over mutual TLS, no proxy from the
// environment (node-to-node traffic is never routed through a third party), and a finite wait for response headers, so
// a core that stops answering costs this request its answer instead of holding the client forever.
func ingressTransport(tlsConfig *tls.Config) *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       tlsConfig,
		ForceAttemptHTTP2:     true,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: ingressHeaderLimit,
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
	return ingressHandlerWith(core, transport, logger, ingressIdleLimit, ingressRequestLimit, -1)
}

// ingressHandlerWith is ingressHandler with the streaming limits and flush interval a test can shorten or remove. The
// header limit lives on the transport, and the whole-request limit covers a client that stops reading as well as a core
// that keeps sending. A test uses a zero flush interval so ReverseProxy's flush timer cannot contend with a writer the
// test deliberately blocks.
func ingressHandlerWith(core *url.URL, transport http.RoundTripper, logger *slog.Logger, idle, request, flush time.Duration) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(core)
	if idle > 0 {
		proxy.Transport = idleTransport{next: transport, idle: idle}
	} else {
		proxy.Transport = transport
	}
	proxy.FlushInterval = flush
	proxy.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Warn("forwarding to the core node", "request_id", httpserver.RequestID(r.Context()), "err", err)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusBadGateway)
		if _, err := io.WriteString(w, `{"error":{"message":"the core node did not answer","type":"upstream_error"}}`); err != nil {
			logger.Warn("writing the forward error", "request_id", httpserver.RequestID(r.Context()), "err", err)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), request)
		defer cancel()
		out := &clientWriteDeadline{ResponseWriter: w, timeout: ingressWriteLimit}
		defer out.clear()
		proxy.ServeHTTP(out, r.WithContext(ctx))
	})
}

// clientWriteDeadline arms a downstream write deadline before each write and flush, and clears it when the request
// ends. A client that stops reading blocks net/http inside Write, where the request's context cannot reach it; the
// deadline turns that into an error the handler can return from. It unwraps so ResponseController can still find the
// server's writer.
type clientWriteDeadline struct {
	http.ResponseWriter
	timeout time.Duration
}

func (w *clientWriteDeadline) Write(p []byte) (int, error) {
	w.arm()
	return w.ResponseWriter.Write(p)
}

func (w *clientWriteDeadline) FlushError() error {
	w.arm()
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *clientWriteDeadline) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *clientWriteDeadline) arm() {
	// A writer without deadline support (an in-process recorder, for example) still has the whole-request limit; not
	// supporting a write deadline must not turn every write into an error.
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(w.timeout)) //nolint:errcheck // see above
}

func (w *clientWriteDeadline) clear() {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Time{}) //nolint:errcheck // the response is already written; there is no error to report
}

// idleTransport fails a response body that sends nothing for idle. A core that is stopped (SIGSTOP) keeps its sockets
// open, and neither ResponseHeaderTimeout nor the connection's idle timeout covers a body already being read.
type idleTransport struct {
	next http.RoundTripper
	idle time.Duration
}

func (t idleTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = newIdleBody(resp.Body, t.idle)
	return resp, nil
}

// idleBody closes the response body when a Read waits longer than idle. Closing it wakes the blocked Read; the reverse
// proxy then stops copying and closes the client's side rather than waiting for a byte the core will never send.
type idleBody struct {
	body   io.ReadCloser
	idle   time.Duration
	timer  *time.Timer
	mu     sync.Mutex
	closed bool
}

func newIdleBody(body io.ReadCloser, idle time.Duration) io.ReadCloser {
	b := &idleBody{body: body, idle: idle}
	b.timer = time.AfterFunc(idle, b.expire)
	return b
}

func (b *idleBody) expire() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.mu.Unlock()
	// The read this close wakes already fails; there is no second error to report.
	_ = b.body.Close() //nolint:errcheck // see above
}

func (b *idleBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if !b.closed {
		b.timer.Reset(b.idle)
	}
	b.mu.Unlock()
	n, err := b.body.Read(p)
	if err != nil {
		b.timer.Stop()
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	return b.body.Close()
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
