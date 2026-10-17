// Package egress keeps outbound HTTP behind one DNS, address and timeout policy.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"axiom/internal/platform/egresstest"
)

// Exit selects an outbound route without exposing the transport's security controls.
type Exit string

// Direct is the only implemented exit; unknown exits must never silently fall back to it.
const Direct Exit = "direct"

// ErrDeadlineRequired prevents a caller from accidentally keeping an upstream stream open forever.
var ErrDeadlineRequired = errors.New("egress: request context requires a deadline")

// ErrInvalidURL rejects URLs whose user information would become a Basic Authorization header or leak into errors.
var ErrInvalidURL = errors.New("egress: expected an HTTP(S) URL with a host and without user information or opaque data")

var errProtocolSwitch = errors.New("egress: upstream answered 101 Switching Protocols; protocol upgrades are not supported")

// Client owns its transport so callers cannot replace address validation or enable proxies.
type Client struct {
	transport *http.Transport
	dialer    *dialer
	tracer    trace.Tracer
}

// Option adjusts a Client.
type Option func(*Client)

// WithTracerProvider makes every Do a span, a child of the span in the request's context, carrying the target host
// and the response status: the host is set by a site owner or ourselves, never by a client; the path and query, which
// may hold identifiers, are left out.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *Client) { c.tracer = tp.Tracer("axiom/egress") }
}

// New keeps one connection pool per selected exit; callers should reuse it across requests.
func New(exit Exit, opts ...Option) (*Client, error) {
	if exit != Direct {
		return nil, errors.New("egress: unsupported exit")
	}
	return newClient(egresstest.Policy{}, net.DefaultResolver.LookupNetIP, opts...), nil
}

// NewForTesting accepts only a capability minted by egresstest, never an arbitrary address predicate.
func NewForTesting(policy egresstest.Policy, opts ...Option) *Client {
	return newClient(policy, net.DefaultResolver.LookupNetIP, opts...)
}

// newClient takes the resolver so tests can name hosts without replacing the production dial and TLS wiring.
// The wait for response headers has no separate bound: Do requires a request deadline, and a non-streaming model
// call sends its headers only after the whole generation, which can take minutes.
func newClient(policy egresstest.Policy, lookup func(context.Context, string, string) ([]netip.Addr, error), opts ...Option) *Client {
	d := &dialer{
		lookup:  lookup,
		connect: (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		policy:  policy,
	}
	c := &Client{tracer: noop.NewTracerProvider().Tracer("axiom/egress"), dialer: d, transport: &http.Transport{
		DialContext: d.dialContext,
		// A custom DialContext turns HTTP/2 off unless it is requested explicitly.
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		// Upstream URLs can be set by site owners; the default 10 MB would let one buffer that much per request.
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
		IdleConnTimeout:        90 * time.Second,
		// A fresh Transport keeps unlimited idle connections in total and 2 per host; a gateway sends many
		// concurrent requests to few upstream hosts, so 2 would reopen connections under load.
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
	}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

const maxResponseHeaderBytes = 1 << 20

// Check says whether a connection to host would be let through, resolving it and applying the address policy as a
// dial does, without connecting. It is how a host is vetted before it is stored: a refused host is ErrAddressDenied.
// The answer holds for the moment it is given; every dial checks again, so a name that later resolves elsewhere is
// still refused.
func (c *Client) Check(ctx context.Context, host string) error {
	_, err := c.dialer.resolve(ctx, host)
	return err
}

// Do clones headers so concurrent callers retain ownership of their request metadata.
// RoundTrip deliberately leaves redirects, including malformed Location values, untouched.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if err := validateRequest(req); err != nil {
		if req != nil && req.Body != nil {
			err = errors.Join(err, req.Body.Close())
		}
		return nil, err
	}
	out := req.Clone(req.Context())
	out.Host = out.URL.Host
	if out.Header == nil {
		out.Header = make(http.Header)
	}
	for key := range out.Header {
		if strings.EqualFold(key, "User-Agent") {
			delete(out.Header, key)
		}
	}
	out.Header.Set("User-Agent", "Axiom")
	ctx, span := c.startSpan(out.Context(), out.URL.Hostname())
	defer span.End()           // ends when the response headers arrive; reading the body is the caller's
	out = out.WithContext(ctx) //nolint:contextcheck // ctx comes from out.Context() through the tracer
	resp, err := c.transport.RoundTrip(out)
	if err != nil {
		span.SetStatus(codes.Error, "request failed") // the error text can quote the address
		return nil, fmt.Errorf("egress request: %w", err)
	}
	recordStatus(span, resp.StatusCode)
	// After a 101 the body is the raw connection, which the request context no longer closes, so a silent upstream
	// could hold a reader past its deadline. Axiom never asks for an upgrade; closing the body closes the connection.
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return nil, errors.Join(errProtocolSwitch, resp.Body.Close())
	}
	return resp, nil
}

// startSpan opens the outbound request's span, a child of ctx's. The host is set by a site owner or ourselves, never
// by a client; the path and query, which may hold identifiers, are left out. The attribute is set only when the span
// records: an unsampled span drops attributes, but Go builds their arguments first, and with no exporter nothing is
// sampled (internal/platform/README.md「遥测」).
func (c *Client) startSpan(ctx context.Context, host string) (context.Context, trace.Span) {
	ctx, span := c.tracer.Start(ctx, "egress.request", trace.WithSpanKind(trace.SpanKindClient))
	if span.IsRecording() {
		span.SetAttributes(attribute.String("server.address", host))
	}
	return ctx, span
}

// recordStatus records the response status on a span that records; an unsampled span keeps nothing, so the attribute
// is built only for one that does.
func recordStatus(span trace.Span, status int) {
	if span.IsRecording() {
		span.SetAttributes(attribute.Int("http.response.status_code", status))
	}
}

func validateRequest(req *http.Request) error {
	if req == nil || req.URL == nil {
		return ErrInvalidURL
	}
	if _, ok := req.Context().Deadline(); !ok {
		return ErrDeadlineRequired
	}
	if req.URL.User != nil || req.URL.Opaque != "" || req.URL.Hostname() == "" ||
		(req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.RequestURI != "" {
		return ErrInvalidURL
	}
	return nil
}

// CloseIdleConnections releases pooled sockets without interrupting active streams.
func (c *Client) CloseIdleConnections() { c.transport.CloseIdleConnections() }
