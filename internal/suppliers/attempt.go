package suppliers

import (
	"bytes"
	"cmp"
	"context"
	"io"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/cloud"
	"axiom/internal/suppliers/internal/persist"
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
// the format's compaction endpoint instead of its own path, the one call that is not a generation. Count sends it to the
// format's token-count endpoint, which a relay may not have: its failure rests nothing, and a cloud platform's endpoint
// refuses it unsent.
type Call struct {
	Format  protocol.Format
	Compact bool
	Count   bool
	Body    []byte
	Header  http.Header
}

// AttemptReceipt is what an attempt leaves for billing: which credential, endpoint and cost schedule it used, the unit
// value it is costed at and the row that value came from, the exit it left by, and the supplier's own id of the request
// once a response header gave it. Billing passes it back unread; its fields are not for anyone else.
type AttemptReceipt struct {
	exchange                                 platform.ExchangeRate
	supplier, credential, endpoint, schedule uuid.UUID
	unitValueID                              uuid.UUID
	ref                                      string
	status                                   int
	pricedAt                                 time.Time
	currency                                 platform.Currency  // the supplier's settlement currency
	unitValue                                platform.UnitPrice // of a unit of the supplier's balance, in currency
	proxy                                    string             // the credential's exit when the attempt took it
	// ratio is the line's ratio of an attempt at a ceiling plan, which its cost is reckoned from (aggregator.go); nil
	// for every other.
	ratio *big.Rat
}

// CredentialID identifies the selected key for independent health evidence.
func (r AttemptReceipt) CredentialID() uuid.UUID { return r.credential }

// Egress is the exit the attempt's requests left by, as the call log names it: the id of the proxy, its fallback's
// while it was disabled, or "direct".
func (r AttemptReceipt) Egress() string { return cmp.Or(r.proxy, directEgress) }

// WithExchange attaches the reservation's basis, never the supplier snapshot's current rate.
func (r AttemptReceipt) WithExchange(rate platform.ExchangeRate) AttemptReceipt {
	r.exchange = rate
	return r
}

// wire is the path and, for Anthropic's format, the version header every system takes the same way. The key goes in
// Authorization: Bearer, which new-api and sub2api both accept on all three; a provider's own API says otherwise
// (adapter.Wire).
var wire = map[protocol.Format]struct {
	path    string
	version string
}{
	protocol.Chat:        {path: "/v1/chat/completions"},
	protocol.Responses:   {path: "/v1/responses"},
	protocol.Messages:    {path: "/v1/messages", version: "2023-06-01"},
	protocol.Embeddings:  {path: "/v1/embeddings"},
	protocol.Rerank:      {path: "/v1/rerank"},
	protocol.Completions: {path: "/v1/completions"},
}

// relayWire is how every relay takes a call: at the format's path, with the key as a bearer token.
type relayWire struct{}

func (relayWire) Path(_ protocol.Format, path string) string { return path }

func (relayWire) Authorize(h http.Header, _ protocol.Format, key string, _ adapter.Settings) {
	h.Set("Authorization", "Bearer "+key)
}

// compactPaths are the formats' compaction endpoints: the one call the supplier answers whole, whose path is not the
// format's own (docs/domains/gateway.md「转发规则」). Only Responses compacts one.
var compactPaths = map[protocol.Format]string{protocol.Responses: "/v1/responses/compact"}

// countPaths are the formats' token-count endpoints. Only Messages has one (Anthropic's count_tokens).
var countPaths = map[protocol.Format]string{protocol.Messages: "/v1/messages/count_tokens"}

// maxErrorBody is how much of a failed response is read for the adapter to classify.
const maxErrorBody = 64 << 10

// Attempt is one call to one credential of an endpoint, from Acquire to Done.
type Attempt struct {
	s         *Service
	ep        *endpoint
	cred      *credential
	priced    time.Time
	fundsRest uint64 // refusal observed when an explicit probe starts

	mu     sync.Mutex
	ref    string
	status int
	once   sync.Once
}

// Acquire takes a slot on the endpoint's least busy credential that is neither resting nor expired and whose rolling
// minute admits another request, by compare and swap on its in-flight count, in memory. It returns ErrNoEndpoint,
// ErrSupplierResting or ErrNoSlot when there is none, and gateway moves on to its next entry: that is not an attempt.
// The slot is held until Done.
func (s *Service) Acquire(id EndpointID) (*Attempt, error) { return s.acquire(id, false, pick{}) }

// AcquireWithin is Acquire for a request whose native cost is at most bound, in the plan's terms (the gateway's
// reserved cost): it passes over a key that is its own wallet (a type that reads balances) whose last balance read
// cannot pay the request at the type's price, bound ÷ the line's ratio, since the provider charges its own price. When
// no key can, it is ErrNoSlot, as for keys at their limits. Any other credential is picked as by Acquire.
func (s *Service) AcquireWithin(id EndpointID, bound platform.Exact) (*Attempt, error) {
	return s.acquire(id, false, pick{bound: &bound})
}

// pick is what a credential is chosen by beside its load: one to pass over while another can serve, and the request's
// cost bound, which a key that is its own wallet must be able to pay; nil for none.
type pick struct {
	avoid uuid.UUID
	bound *platform.Exact
}

func (s *Service) acquire(id EndpointID, probe bool, p pick) (*Attempt, error) {
	for range 8 { // another caller may take the slot or the window's last request between looking and swapping
		snap := s.snap.Load()
		ep, ok := snap.endpoints[id]
		if !ok {
			return nil, ErrNoEndpoint
		}
		if !probe && ep.funds.resting() {
			return nil, ErrSupplierResting
		}
		var fundsRest uint64
		if probe {
			ep.funds.state.mu.Lock()
			fundsRest = ep.funds.state.version
			ep.funds.state.mu.Unlock()
		}
		now := s.now() // the credential may have rested or expired since the last look
		best, n := leastBusy(ep, now, p)
		if best == nil {
			return nil, ErrNoSlot
		}
		if !best.rt.inflight.CompareAndSwap(n, n+1) { // n is below the limit, and the swap only succeeds if it is still n
			continue
		}

		if !best.rt.requests.takeCurrent(best.perMinute, s, snap) {
			best.rt.inflight.Add(-1) // no request was made: the slot goes back, and another credential is tried
			continue
		}
		return &Attempt{s: s, ep: ep, cred: best, priced: now, fundsRest: fundsRest}, nil
	}
	return nil, ErrNoSlot
}

// Capacity is one endpoint's live credential capacity: whether any of its credentials can take another call now. It
// reads the state Acquire reads before it takes a slot — concurrency, the supplier account's rolling minute, rest and
// expiry — so a plan that finds room can still lose the slot to a concurrent call: Acquire stays the hard bound, and a
// call that loses the race moves on to its next entry (internal/suppliers/README.md「并发与速率」).
type Capacity struct {
	s  *Service
	id EndpointID
}

// Full reports whether no credential of the endpoint can take a call now. The endpoint is read from the current
// snapshot every time, so a Reload and the shares SyncShares applies are seen without rebuilding the routing snapshot;
// an endpoint the snapshot no longer holds is not full, and Acquire answers for it.
func (c *Capacity) Full() bool {
	ep, ok := c.s.snap.Load().endpoints[c.id]
	if !ok {
		return false
	}
	best, _ := leastBusy(ep, c.s.now(), pick{})
	return best == nil
}

// leastBusy is the credential with the lowest share of its limit in use among those that may be picked now, with the
// count of attempts it was found with. Each credential is held to its upstream account's rolling minute: a relay's
// credentials share one, so it is counted once, and a pasted key's is its own.
func leastBusy(ep *endpoint, now time.Time, p pick) (best *credential, bestN int32) {
	var (
		bestLoad float64
		minute   accountMinute
	)
	for _, c := range ep.creds {
		if !c.available(now) || p.bound != nil && c.funds != nil && c.funds.Cmp(*p.bound) < 0 || minute.full(c, now) {
			continue
		}
		n := c.rt.inflight.Load()
		if n >= c.max {
			continue
		}
		load := float64(n) / float64(c.max)
		if c.id == p.avoid {
			load += 2
		} // Every admissible load is below one; an alternative always wins over the previous key.
		if best == nil || load < bestLoad {
			best, bestN, bestLoad = c, n, load
		}
	}
	return best, bestN
}

// accountMinute is the rolling minute of the upstream account last looked at, read once for all of its credentials.
type accountMinute struct {
	account *minuteRuntime
	used    int64
}

// full reports whether the credential's account has used its ceiling this minute; a credential with none never is.
func (m *accountMinute) full(c *credential, now time.Time) bool {
	if c.perMinute == 0 {
		return false
	}
	if c.rt.requests != m.account {
		m.account, m.used = c.rt.requests, c.rt.requests.count(now)
	}
	return m.used >= int64(c.perMinute)
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
		unitValueID: a.ep.unitValueID, ref: a.ref, pricedAt: a.priced, currency: a.ep.currency, unitValue: a.ep.unitValue, status: a.status,
		proxy: a.cred.proxy, ratio: a.ep.ratio,
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
// failed or not, because it is what the charge will be matched by. A cloud platform's adapter makes the request itself
// (adapter.Signer), from the credential's region and the model as it names it, and gives its answer in the format's
// framing; a failure to sign is the call's before anything was sent.
//
// Whether the supplier ran a request that failed is decided in two places. A failure before the request was fully
// written is NotExecuted whatever its cause. After it, the adapter's Classify says: only an answer by which the
// supplier states it refused before running the request is NotExecuted, and a timeout, a reset or a 5xx is not.
// Neither suppliers nor egress retries a POST.
func (a *Attempt) Send(ctx context.Context, call Call) (*http.Response, *Failure) {
	resp, f := a.send(ctx, call)
	if f != nil && !call.Count { // a relay without the count route answers 404, which says nothing of the credential
		a.rest(*f) //nolint:contextcheck // the rejection worker outlives the request; its own timeout bounds the write
	}
	return resp, f
}

// send is Send without resting what a failure says is at fault.
func (a *Attempt) send(ctx context.Context, call Call) (*http.Response, *Failure) {
	signer, cloud := a.ep.quirks.(adapter.Signer)
	var (
		req *http.Request
		f   *Failure
	)
	switch {
	case cloud && call.Count:
		return nil, &Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	case cloud:
		req, f = signer.Sign(ctx, a.cred.exit, adapter.SignedCall{
			Format: call.Format, Origin: a.ep.origin, Region: a.cred.region, Model: a.cred.call, Body: call.Body,
			Header: call.Header, Key: a.cred.key, Settings: a.cred.settings, At: a.s.now(),
		})
	default:
		req, f = a.request(ctx, call)
	}
	if f != nil {
		return nil, f
	}
	var wrote atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(i httptrace.WroteRequestInfo) { wrote.Store(i.Err == nil) }}
	resp, err := a.cred.exit.Do(req.WithContext(httptrace.WithClientTrace(ctx, trace)))
	if err != nil {
		return nil, transportFailure(wrote.Load(), a.cred.proxy != "")
	}
	a.noteResponse(resp.StatusCode, resp.Header)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if cloud {
			resp = signer.Answer(resp)
		}
		return resp, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody)) //nolint:errcheck // a body that fails to read is classified by what was read
	_ = resp.Body.Close()                                          //nolint:errcheck // nothing more to learn
	failure := a.ep.quirks.Classify(resp.StatusCode, resp.Header, body)
	if failure.Scope == adapter.ScopeRequest {
		failure.Rejection = rejectionOf(body, a.ep.origin, a.cred.key.Reveal())
	}
	return nil, &failure
}

// request is the call as a relay or a provider's own API takes it: at the format's path under the origin, with the key
// in the header the format takes there.
func (a *Attempt) request(ctx context.Context, call Call) (*http.Request, *Failure) {
	w, ok := wire[call.Format]
	if !ok {
		return nil, &Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	}
	path := w.path
	switch {
	case call.Compact:
		path, ok = compactPaths[call.Format]
	case call.Count:
		path, ok = countPaths[call.Format]
	}
	if !ok {
		return nil, &Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	}
	wire, own := a.ep.quirks.(adapter.Wire)
	if !own {
		wire = relayWire{}
	}
	body, err := a.namedBody(call.Body)
	if err != nil {
		return nil, &Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.ep.origin+wire.Path(call.Format, path), bytes.NewReader(body))
	if err != nil {
		return nil, &Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	}
	maps.Copy(req.Header, call.Header)
	wire.Authorize(req.Header, call.Format, a.cred.key.Reveal(), a.cred.settings)
	req.Header.Set("Content-Type", "application/json")
	if w.version != "" && req.Header.Get("Anthropic-Version") == "" {
		req.Header.Set("Anthropic-Version", w.version)
	}
	return req, nil
}

// namedBody is the body with the model as the credential's calls name it: the gateway writes the endpoint's name, and a
// key that lists the model under a name of its own (call_names) may not know that one. Only then is the body rewritten.
func (a *Attempt) namedBody(body []byte) ([]byte, error) {
	if a.cred.call == a.ep.upstream {
		return body, nil
	}
	model, err := jsonx.MarshalCanonical(a.cred.call)
	if err != nil {
		return nil, err
	}
	out, _, err := cloud.Rewrite(body, nil, cloud.Member{Name: "model", Value: model})
	return out, err
}

func (a *Attempt) noteResponse(status int, h http.Header) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status = status
	if ref, ok := a.ep.quirks.ChargeRef(h); ok {
		a.ref = string(ref)
	}
}

// transportFailure is a failure with no usable response. Before the request was written nothing reached the supplier,
// and the fault is in the route to it; after, it may have run, and the fault is the endpoint's, or the route's when it
// went through a proxy, which stands between the two and can drop either side.
func transportFailure(wrote, proxied bool) *Failure {
	switch {
	case !wrote:
		return &Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeEgress, Cause: adapter.Transport{}}
	case proxied:
		return &Failure{Scope: adapter.ScopeEgress, Cause: adapter.Transport{}}
	}
	return &Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Transport{}}
}

// reportRejected asks the worker to mark a key the provider refused. A credential has at most one report in flight: the
// call path stays free of the database, and a revoked key found by a thousand requests writes its row once. The mark is
// cleared when the write ends, so a write that failed is tried again and a key that a test restored can be marked again.
func (s *Service) reportRejected(supplier uuid.UUID, cred *credential) {
	if s.cfg.DB == nil || !cred.rt.reporting.CompareAndSwap(false, true) {
		return
	}
	s.rejectOnce.Do(func() { go s.markRejected() })
	select {
	case s.rejected <- rejection{supplier: supplier, credential: cred.id, runtime: cred.rt}:
	default:
		cred.rt.reporting.Store(false) // the queue is full; a later rejection tries again
	}
}

// markRejected writes the rejected status, and the suppliers' queue with it, until the queue of reports is empty. An
// error is logged and not retried: the credential is resting in this node anyway, and the next rejection after a
// restart tries again.
func (s *Service) markRejected() {
	for r := range s.rejected {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.cfg.DB.Tx(ctx, s.cfg.Scope, func(tx pg.Tx) error {
			return persist.RejectCredential(ctx, tx, r.supplier, r.credential, s.now())
		})
		if err != nil {
			s.cfg.Logger.Error("marking a refused key", "supplier", r.supplier.String(), "err", err)
		}
		r.runtime.reporting.Store(false)
		cancel()
	}
}
