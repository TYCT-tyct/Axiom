package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"axiom/internal/platform/egress"
	"axiom/internal/suppliers/internal/adapter"
)

const (
	// maxBody bounds a response. A page of 100 charge records is about 100 KiB; the limit is far above any answer the
	// management side reads and far below what could hurt the node.
	maxBody = 8 << 20
	// requestTimeout is the deadline egress requires on every request.
	requestTimeout = 30 * time.Second
	// defaultBlock is how long a 429 without a usable Retry-After closes the buckets.
	defaultBlock = time.Minute
)

// ErrTooLarge is the error of a response longer than maxBody.
var ErrTooLarge = errors.New("client: the response is larger than the limit")

// Client is an adapter.Client for one supplier: its origin, the exit it is reached through and its rate limits. The
// address of every request is the origin plus the adapter's path, so credentials go only to the host the supplier was
// connected with.
type Client struct {
	origin  string
	http    *egress.Client
	limiter *Limiter
	now     func() time.Time
}

var _ adapter.Client = (*Client)(nil)

// New makes the client of the supplier at origin, which is scheme://host[:port] and nothing more.
func New(origin string, exit *egress.Client, limiter *Limiter, now func() time.Time) (*Client, error) {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "" || u.RawQuery != "" ||
		u.Fragment != "" || u.User != nil {
		return nil, errors.New("client: the origin must be scheme://host[:port] and nothing more")
	}
	return &Client{origin: u.Scheme + "://" + u.Host, http: exit, limiter: limiter, now: now}, nil
}

// Do implements adapter.Client. A 429 closes every bucket the request named until its Retry-After, because new-api
// answers it with an empty body that does not say which of them was exceeded, and returns Blocked: the caller tries
// again later, and no request goes out meanwhile.
func (c *Client) Do(ctx context.Context, r adapter.Request) (adapter.Response, error) {
	if !strings.HasPrefix(r.Path, "/") || strings.HasPrefix(r.Path, "//") {
		return adapter.Response{}, fmt.Errorf("client: path %q must start with a single slash", r.Path)
	}
	if err := c.limiter.Take(r.Buckets); err != nil {
		return adapter.Response{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, c.origin+r.Path, bytes.NewReader(r.Body))
	if err != nil {
		return adapter.Response{}, fmt.Errorf("client: build the request: %w", err)
	}
	req.Header = r.Header.Clone()
	resp, err := c.http.Do(req)
	if err != nil {
		return adapter.Response{}, fmt.Errorf("client: send the request: %w", err)
	}
	defer resp.Body.Close()                            //nolint:errcheck // the body was read or abandoned; nothing more to learn from closing it
	if resp.StatusCode == http.StatusTooManyRequests { // before the body: what it says is not needed, and it may not come
		until := c.now().Add(retryAfter(resp.Header.Get("Retry-After")))
		c.limiter.Block(r.Buckets, until)
		return adapter.Response{}, adapter.Blocked{Until: until}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return adapter.Response{}, fmt.Errorf("client: read the response: %w", err)
	}
	if len(body) > maxBody {
		return adapter.Response{}, ErrTooLarge
	}
	return adapter.Response{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

// retryAfter reads the seconds form of Retry-After; anything else, and anything not positive, is defaultBlock.
func retryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return defaultBlock
	}
	return time.Duration(n) * time.Second
}
