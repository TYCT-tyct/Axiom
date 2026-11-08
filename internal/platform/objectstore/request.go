package objectstore

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxAttempts   = 4 // one try and three retries: a few seconds of a failing bucket, not the whole deadline
	retryBase     = 200 * time.Millisecond
	retryMax      = 5 * time.Second
	retryAfterCap = 30 * time.Second
	maxErrorBody  = 4 << 10
	maxListBody   = 8 << 20

	// S3's limits: one PUT carries at most 5 GiB, a multipart object at most 5 TiB, and its parts between 5 MiB and
	// 5 GiB. A 16 MiB part keeps a 5 TiB object under the 10,000-part limit once the size grows past 160 GiB.
	maxSinglePut       = 5 << 30
	maxObjectSize      = 5 << 40
	defaultPartSize    = 16 << 20
	maxPartSize        = 5 << 30
	maxParts           = 10000
	multipartThreshold = defaultPartSize
)

// hexDigits writes the upper-case escapes SigV4 and S3 URLs use.
const hexDigits = "0123456789ABCDEF"

// request is one S3 operation before it is signed. Every field but body and contentType describes the HTTP request
// that SigV4 signs, so a retry re-creates exactly the same request.
type request struct {
	method      string
	key         string // empty for a bucket operation
	query       url.Values
	body        io.ReadSeeker
	size        int64
	contentType string
	payloadHash string
	ifNoneMatch bool // If-None-Match: *, signed like the content type
}

// do sends the request, retrying only what may succeed later: a transport failure, 408, 429 and 5xx. A retryable
// status on the last attempt is returned as a response, so the caller turns the server's own explanation into an
// Error instead of replacing it with "too many attempts".
func (c *Client) do(ctx context.Context, r request) (*http.Response, error) {
	if err := requireDeadline(ctx); err != nil {
		return nil, err
	}
	var last error
	for attempt := 1; ; attempt++ {
		resp, err := c.roundTrip(ctx, r)
		if err == nil && !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		last = attemptError(resp, err)
		if ctx.Err() != nil {
			drain(resp)
			return nil, errors.Join(last, ctx.Err())
		}
		if attempt >= c.attempts {
			return lastAttempt(resp, last)
		}
		if err := c.wait(ctx, attempt, resp); err != nil {
			return nil, errors.Join(last, err)
		}
	}
}

// attemptError is what a failed attempt leaves behind: the transport's error, or the status when the server answered.
func attemptError(resp *http.Response, err error) error {
	if err != nil {
		return err
	}
	return statusError(resp)
}

// lastAttempt hands back the final response so its own error document wins over "too many attempts"; a transport
// failure has no response to hand back.
func lastAttempt(resp *http.Response, last error) (*http.Response, error) {
	if resp != nil {
		return resp, nil
	}
	return nil, last
}

// wait is the order a retry needs: compute the wait, drain the body that will not be read, then pause. A canceled
// context stops at the pause.
func (c *Client) wait(ctx context.Context, attempt int, resp *http.Response) error {
	wait := c.backoff(attempt, retryAfter(resp, c.now()))
	drain(resp)
	return pause(ctx, wait)
}

// roundTrip sends one attempt and rewinds the body first. A protocol upgrade is refused and closed like egress does:
// after 101 the body is the raw connection, which the request deadline no longer closes.
func (c *Client) roundTrip(ctx context.Context, r request) (*http.Response, error) {
	req, err := c.request(ctx, r)
	if err != nil {
		return nil, err
	}
	resp, err := c.transport.RoundTrip(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("objectstore %s: %w", r.method, ctx.Err())
		}
		return nil, fmt.Errorf("objectstore %s: %w", r.method, err)
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return nil, errors.Join(errors.New("objectstore: protocol upgrades are not supported"), resp.Body.Close())
	}
	return resp, nil
}

// request builds and signs one attempt. The body is rewound here as well as after hashing, because an earlier attempt
// consumed it.
func (c *Client) request(ctx context.Context, r request) (*http.Request, error) {
	path, rawPath := c.objectPath(r.key)
	u := *c.endpoint
	u.Path = path
	u.RawPath = rawPath
	u.RawQuery = canonicalQuery(r.query)

	var body io.ReadCloser
	if r.body != nil {
		if _, err := r.body.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("objectstore: rewind request body: %w", err)
		}
		body = io.NopCloser(r.body)
	}
	req := &http.Request{
		Method:        r.method,
		URL:           &u,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          body,
		ContentLength: r.size,
		Host:          u.Host,
	}
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", "Axiom")
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	if r.ifNoneMatch {
		req.Header.Set("If-None-Match", "*")
	}
	c.sign(req, r.payloadHash, c.now())
	return req, nil
}

// objectPath returns the decoded path and its S3 encoding for one request. Key segments are escaped with RFC 3986's
// unreserved set plus "/", which is what S3 signs and what every implementation answers to.
func (c *Client) objectPath(key string) (path, rawPath string) {
	base := strings.TrimSuffix(c.endpoint.Path, "/")
	var decoded, encoded strings.Builder
	decoded.WriteString(base)
	encoded.WriteString(escape(base, true))
	if c.pathStyle {
		decoded.WriteByte('/')
		encoded.WriteByte('/')
		decoded.WriteString(c.bucket)
		encoded.WriteString(escape(c.bucket, true))
	}
	if key != "" {
		decoded.WriteByte('/')
		encoded.WriteByte('/')
		decoded.WriteString(key)
		encoded.WriteString(escape(key, true))
	}
	if decoded.Len() == 0 {
		decoded.WriteByte('/')
		encoded.WriteByte('/')
	}
	return decoded.String(), encoded.String()
}

// escape encodes every byte except an unreserved character, and "/" when the value is a path. It iterates bytes so a
// UTF-8 key becomes its bytes' escapes, which is what S3 stores.
func escape(value string, keepSlash bool) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.', ch == '~':
			out.WriteByte(ch)
		case ch == '/' && keepSlash:
			out.WriteByte(ch)
		default:
			out.WriteByte('%')
			out.WriteByte(hexDigits[ch>>4])
			out.WriteByte(hexDigits[ch&0xf])
		}
	}
	return out.String()
}

// canonicalQuery encodes and sorts the query exactly as it is signed: by encoded name, then encoded value, with
// spaces as %20 rather than url.Values' "+".
func canonicalQuery(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		names := values[key]
		sort.Strings(names)
		for _, value := range names {
			if out.Len() > 0 {
				out.WriteByte('&')
			}
			out.WriteString(escape(key, false))
			out.WriteByte('=')
			out.WriteString(escape(value, false))
		}
	}
	return out.String()
}

// remoteError turns a failed response into an Error carrying the server's code, and always closes the body.
func remoteError(resp *http.Response) error {
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	closeErr := resp.Body.Close()
	remote := errorDocument(resp.StatusCode, data)
	if remote == nil {
		// An HTML error page from a proxy is still an Error with its status; a body that is not XML adds nothing.
		remote = &Error{Status: resp.StatusCode}
	}
	remote.cause = errors.Join(readErr, closeErr)
	return remote
}

// errorDocument parses an S3 Error document a caller already read, and returns nil when the body names no code.
func errorDocument(status int, data []byte) *Error {
	var remote struct {
		Code      string `xml:"Code"`
		Message   string `xml:"Message"`
		RequestID string `xml:"RequestId"`
	}
	if xml.Unmarshal(data, &remote) == nil && remote.Code != "" {
		return &Error{Status: status, Code: remote.Code, Message: remote.Message, RequestID: remote.RequestID}
	}
	// An HTML error page from a proxy, or XML that names no code, adds nothing beyond the status.
	return nil
}

// statusError describes a retryable status before the response body is parsed; the last attempt is parsed instead.
func statusError(resp *http.Response) error {
	return fmt.Errorf("objectstore: HTTP %d", resp.StatusCode)
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// retryAfter reads the server's requested wait, in either form RFC 9110 allows, capped so a long one cannot push the
// next attempt past a deadline that the caller set for the whole operation.
func retryAfter(resp *http.Response, now time.Time) time.Duration {
	if resp == nil {
		return 0
	}
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > int(retryAfterCap/time.Second) {
			return retryAfterCap
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return min(max(at.Sub(now), 0), retryAfterCap)
	}
	return 0
}

// backoff doubles from retryBase to retryMax and adds up to half a wait of jitter, so a bucket's outage does not make
// every node retry in the same instant.
func (c *Client) backoff(attempt int, asked time.Duration) time.Duration {
	if asked > 0 {
		return asked
	}
	wait := c.retryBase
	for i := 1; i < attempt && wait < c.retryMax; i++ {
		wait *= 2
	}
	wait = min(wait, c.retryMax)
	return wait/2 + time.Duration(rand.Int64N(int64(wait/2)+1)) //nolint:gosec // jitter spreads retries; it guards nothing
}

func pause(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// drain reads a little of a response that will be retried and closes it, so the connection can be reused. A
// transport failure has no response to drain.
func drain(resp *http.Response) {
	if resp == nil {
		return
	}
	// The response is about to be thrown away; its read and close errors cannot change the retry decision.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) //nolint:errcheck // discarded response
	_ = resp.Body.Close()                                         //nolint:errcheck // discarded response
}

// newTransport is the object store's own transport. It keeps egress's timeouts, header limit and connection reuse, but
// not its public-address policy: the endpoint is deployment configuration, and a self-hosted SeaweedFS is normally on a
// private network. A user-controlled destination never reaches this client.
func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:       nil, // ignore HTTP_PROXY: the bucket's address is the deployment's, not the environment's
		DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		// A custom DialContext turns HTTP/2 off unless it is requested explicitly.
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		MaxResponseHeaderBytes: 1 << 20,
		IdleConnTimeout:        90 * time.Second,
		MaxIdleConns:           100,
		MaxIdleConnsPerHost:    10,
		DisableCompression:     true, // object bytes are already compressed; a gzip layer would only cost CPU
	}
}

// Error is a refusal from the bucket, with the server's own code when it sent one.
type Error struct {
	Status    int
	Code      string
	Message   string
	RequestID string
	cause     error
}

func (e *Error) Error() string {
	parts := []string{"HTTP " + strconv.Itoa(e.Status)}
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if message := truncate(e.Message, 256); message != "" {
		parts = append(parts, message)
	}
	if e.RequestID != "" {
		parts = append(parts, "request "+e.RequestID)
	}
	return strings.Join(parts, ": ")
}

// Unwrap exposes a body read or close failure beside the server's answer.
func (e *Error) Unwrap() error { return e.cause }

// Is lets a caller test errors.Is(err, ErrNotFound) without knowing which implementation answered 404, and
// errors.Is(err, ErrExists) for the 412 of a conditional PUT.
func (e *Error) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound ||
		target == ErrExists && e.Status == http.StatusPreconditionFailed
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
