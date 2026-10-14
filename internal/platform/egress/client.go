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
type Client struct{ transport *http.Transport }

// New keeps one connection pool per selected exit; callers should reuse it across requests.
func New(exit Exit) (*Client, error) {
	if exit != Direct {
		return nil, errors.New("egress: unsupported exit")
	}
	return newClient(egresstest.Policy{}, net.DefaultResolver.LookupNetIP), nil
}

// NewForTesting accepts only a capability minted by egresstest, never an arbitrary address predicate.
func NewForTesting(policy egresstest.Policy) *Client {
	return newClient(policy, net.DefaultResolver.LookupNetIP)
}

// newClient takes the resolver so tests can name hosts without replacing the production dial and TLS wiring.
// The wait for response headers has no separate bound: Do requires a request deadline, and a non-streaming model
// call sends its headers only after the whole generation, which can take minutes.
func newClient(policy egresstest.Policy, lookup func(context.Context, string, string) ([]netip.Addr, error)) *Client {
	d := &dialer{
		lookup:  lookup,
		connect: (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		policy:  policy,
	}
	return &Client{transport: &http.Transport{
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
}

const maxResponseHeaderBytes = 1 << 20

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
	resp, err := c.transport.RoundTrip(out)
	if err != nil {
		return nil, fmt.Errorf("egress request: %w", err)
	}
	// After a 101 the body is the raw connection, which the request context no longer closes, so a silent upstream
	// could hold a reader past its deadline. Axiom never asks for an upgrade; closing the body closes the connection.
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return nil, errors.Join(errProtocolSwitch, resp.Body.Close())
	}
	return resp, nil
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
