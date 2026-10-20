package suppliers

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// Failure is a failed attempt, as the adapter classified it: who is at fault (Scope), what went wrong (Cause) and
// whether the supplier ran the request (Executed).
type Failure = adapter.Failure

// Scope is what a failure says is at fault, and so what to stop using; the constants are the adapter's, named here for
// gateway, which maps them one by one to routing's.
type Scope = adapter.Scope

// The scopes of a Failure.
const (
	ScopeRequest    = adapter.ScopeRequest
	ScopeEgress     = adapter.ScopeEgress
	ScopeEndpoint   = adapter.ScopeEndpoint
	ScopeCredential = adapter.ScopeCredential
	ScopeGroup      = adapter.ScopeGroup
	ScopeSupplier   = adapter.ScopeSupplier
)

// CallPolicy is how calls to an endpoint behave: whether they stream and whether the supplier stops when we do.
type CallPolicy = adapter.CallPolicy

// Call is what is sent to the supplier: a request body in a call format, already prepared for this endpoint, and the
// request headers that go with it (gateway's whitelist: anthropic-beta and anthropic-version). Header never replaces
// the credential or the content type, and gives the version the client chose over the default. Compact sends the call to
// the format's compaction endpoint instead of its own path, the one call that is not a generation.
type Call struct {
	Format  protocol.Format
	Compact bool
	Body    []byte
	Header  http.Header
}

// AttemptReceipt is what an attempt leaves for billing: which credential, endpoint and cost schedule it used, the unit
// value it is costed at and the row that value came from, and the supplier's own id of the request once a response
// header gave it. Billing passes it back unread; its fields are not for anyone else.
type AttemptReceipt struct {
	exchange                                 platform.ExchangeRate
	supplier, credential, endpoint, schedule uuid.UUID
	unitValueID                              uuid.UUID
	ref                                      string
	pricedAt                                 time.Time
	currency                                 platform.Currency  // the supplier's settlement currency
	unitValue                                platform.UnitPrice // of a unit of the supplier's balance, in currency
}

// WithExchange attaches the reservation's basis, never the supplier snapshot's current rate.
func (r AttemptReceipt) WithExchange(rate platform.ExchangeRate) AttemptReceipt {
	r.exchange = rate
	return r
}

// wire is the path and, for Anthropic's format, the version header every system takes the same way. The key goes in
// Authorization: Bearer, which new-api and sub2api both accept on all three.
var wire = map[protocol.Format]struct {
	path    string
	version string
}{
	protocol.Chat:      {path: "/v1/chat/completions"},
	protocol.Responses: {path: "/v1/responses"},
	protocol.Messages:  {path: "/v1/messages", version: "2023-06-01"},
}

// compactPaths are the formats' compaction endpoints: the one call the supplier answers whole, whose path is not the
// format's own (docs/domains/gateway.md「转发规则」). Only Responses compacts one.
var compactPaths = map[protocol.Format]string{protocol.Responses: "/v1/responses/compact"}

// maxErrorBody is how much of a failed response is read for the adapter to classify.
const maxErrorBody = 64 << 10

// Attempt is one call to one credential of an endpoint, from Acquire to Done.
type Attempt struct {
	s      *Service
	ep     *endpoint
	cred   *credential
	priced time.Time

	mu   sync.Mutex
	ref  string
	once sync.Once
}

// Acquire takes a slot on the endpoint's least busy credential that is neither resting nor expired, by compare and
// swap on its in-flight count, in memory. It returns ErrNoEndpoint or ErrNoSlot when there is none, and gateway moves
// on to its next entry: that is not an attempt. The slot is held until Done.
func (s *Service) Acquire(id EndpointID) (*Attempt, error) {
	ep, ok := s.snap.Load().endpoints[id]
	if !ok {
		return nil, ErrNoEndpoint
	}
	now := s.now()
	for range 8 { // another caller may take the slot between looking and swapping
		best, n := leastBusy(ep, now)
		if best == nil {
			return nil, ErrNoSlot
		}
		if best.rt.inflight.CompareAndSwap(n, n+1) { // n is below the limit, and the swap only succeeds if it is still n
			return &Attempt{s: s, ep: ep, cred: best, priced: now}, nil
		}
	}
	return nil, ErrNoSlot
}

// leastBusy is the credential with the lowest share of its limit in use among those that may be picked now, with the
// count of attempts it was found with.
func leastBusy(ep *endpoint, now time.Time) (best *credential, bestN int32) {
	var bestLoad float64
	for _, c := range ep.creds {
		if (c.expires != nil && !now.Before(*c.expires)) || now.UnixNano() < c.rt.restUnix.Load() {
			continue
		}
		n := c.rt.inflight.Load()
		if n >= c.max {
			continue
		}
		if load := float64(n) / float64(c.max); best == nil || load < bestLoad {
			best, bestN, bestLoad = c, n, load
		}
	}
	return best, bestN
}

// NotExecuted is the Executed of a failure after which the supplier is known not to have run the request; any other
// value is unknown, and the cost is then estimated (README.md「调用面」).
const NotExecuted = adapter.NotExecuted

// The causes of a Failure. gateway and quality cannot name the adapter's own types, so they are named here: gateway
// weighs an endpoint down for RateLimited, and quality judges an endpoint's health by every cause
// (docs/domains/quality.md「什么算一次失败」). Each is the adapter's type of the same name.
type (
	// RateLimited is adapter.RateLimited.
	RateLimited = adapter.RateLimited
	// Overloaded is adapter.Overloaded.
	Overloaded = adapter.Overloaded
	// UpstreamError is adapter.UpstreamError.
	UpstreamError = adapter.UpstreamError
	// SupplierBalanceLow is adapter.SupplierBalanceLow.
	SupplierBalanceLow = adapter.SupplierBalanceLow
	// CredentialQuotaExhausted is adapter.CredentialQuotaExhausted.
	CredentialQuotaExhausted = adapter.CredentialQuotaExhausted
	// CredentialRejected is adapter.CredentialRejected.
	CredentialRejected = adapter.CredentialRejected
	// NotPermitted is adapter.NotPermitted.
	NotPermitted = adapter.NotPermitted
	// ContextTooLong is adapter.ContextTooLong.
	ContextTooLong = adapter.ContextTooLong
	// BadRequest is adapter.BadRequest.
	BadRequest = adapter.BadRequest
	// ContentRejected is adapter.ContentRejected.
	ContentRejected = adapter.ContentRejected
	// ModelNotFound is adapter.ModelNotFound.
	ModelNotFound = adapter.ModelNotFound
	// Unknown is adapter.Unknown.
	Unknown = adapter.Unknown
	// Transport is adapter.Transport.
	Transport = adapter.Transport
)

// Policy is how this attempt's endpoint behaves: the adapter's default, over which quality's measurement of
// cancellation stands.
func (a *Attempt) Policy() CallPolicy { return a.s.policy(a.ep) }

// receipt is what the attempt has so far; Done gives it out once the attempt is over.
func (a *Attempt) receipt() AttemptReceipt {
	a.mu.Lock()
	defer a.mu.Unlock()
	return AttemptReceipt{
		supplier: a.ep.supplier, credential: a.cred.id, endpoint: a.ep.id, schedule: a.ep.schedule,
		unitValueID: a.ep.unitValueID, ref: a.ref, pricedAt: a.priced, currency: a.ep.currency, unitValue: a.ep.unitValue,
	}
}

// Done ends the attempt: it frees the slot, once however often and from wherever it is called, and returns the
// receipt, with the supplier's request id if a response gave one. It takes no arguments so that cancelling a
// request and finishing it can both call it.
func (a *Attempt) Done() AttemptReceipt {
	a.once.Do(func() { a.cred.rt.inflight.Add(-1) })
	return a.receipt()
}

// Send posts the call to the supplier. A response in the 2xx range is returned for the caller to read and close.
// Anything else comes back as a Failure. The supplier's request id is taken from the response headers of any answer,
// failed or not, because it is what the charge will be matched by.
//
// Whether the supplier ran a request that failed is decided in two places. A failure before the request was fully
// written is NotExecuted whatever its cause. After it, the adapter's Classify says: only an answer by which the
// supplier states it refused before running the request is NotExecuted, and a timeout, a reset or a 5xx is not.
// Neither suppliers nor egress retries a POST.
func (a *Attempt) Send(ctx context.Context, call Call) (*http.Response, *Failure) {
	w, ok := wire[call.Format]
	if !ok {
		return nil, &Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	}
	var wrote atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(i httptrace.WroteRequestInfo) { wrote.Store(i.Err == nil) }}
	path := w.path
	if call.Compact {
		if path, ok = compactPaths[call.Format]; !ok {
			return nil, &Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
		}
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, a.ep.origin+path, bytes.NewReader(call.Body))
	if err != nil {
		return nil, &Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	}
	maps.Copy(req.Header, call.Header)
	req.Header.Set("Authorization", "Bearer "+a.cred.key.Reveal())
	req.Header.Set("Content-Type", "application/json")
	if w.version != "" && req.Header.Get("Anthropic-Version") == "" {
		req.Header.Set("Anthropic-Version", w.version)
	}
	resp, err := a.s.cfg.Egress.Do(req)
	if err != nil {
		return nil, transportFailure(wrote.Load())
	}
	a.noteRef(resp.Header)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody)) //nolint:errcheck // a body that fails to read is classified by what was read
	_ = resp.Body.Close()                                          //nolint:errcheck // nothing more to learn
	f := a.ep.quirks.Classify(resp.StatusCode, resp.Header, body)
	if f.Scope == adapter.ScopeRequest {
		f.Rejection = rejectionOf(body, a.ep.origin, a.cred.key.Reveal())
	}
	a.rest(f)
	return nil, &f
}

func (a *Attempt) noteRef(h http.Header) {
	if ref, ok := a.ep.quirks.ChargeRef(h); ok {
		a.mu.Lock()
		a.ref = string(ref)
		a.mu.Unlock()
	}
}

// transportFailure is a failure with no usable response. Before the request was written nothing reached the supplier,
// and the fault is in the route to it; after, it may have run.
func transportFailure(wrote bool) *Failure {
	if !wrote {
		return &Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeEgress, Cause: adapter.Transport{}}
	}
	return &Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Transport{}}
}

// rest keeps a credential the supplier refused from being picked for a while: until the time the supplier named, or
// for the configured rest when it named none. Faults of other scopes are for routing and quality to act on.
func (a *Attempt) rest(f Failure) {
	if f.Scope != adapter.ScopeCredential {
		return
	}
	now := a.s.now()
	until := now.Add(a.s.cfg.CredentialCooldown)
	switch c := f.Cause.(type) {
	case adapter.CredentialQuotaExhausted:
		until = now.Add(a.s.cfg.QuotaCooldown)
		if c.ResetAt.After(now) {
			until = c.ResetAt
		}
	case adapter.RateLimited:
		until = now.Add(max(c.RetryAfter, time.Second))
	case adapter.CredentialRejected, adapter.Overloaded, adapter.UpstreamError, adapter.SupplierBalanceLow, adapter.NotPermitted,
		adapter.ContextTooLong, adapter.BadRequest, adapter.ContentRejected, adapter.ModelNotFound, adapter.Unknown, adapter.Transport:
	}
	for {
		cur := a.cred.rt.restUnix.Load()
		if until.UnixNano() <= cur || a.cred.rt.restUnix.CompareAndSwap(cur, until.UnixNano()) {
			return
		}
	}
}
