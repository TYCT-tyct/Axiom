package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// The causes an attempt is cancelled for, which tell what ended its read (context.Cause).
var (
	errIdle       = errors.New("gateway: the upstream sent nothing new in time")
	errHeaderWait = errors.New("gateway: the upstream did not answer in time")
	errClientGone = errors.New("gateway: the client is not reading")
	errShutdown   = errors.New("gateway: the node is going away")
)

// skips is why entries were passed over without an attempt (internal/gateway/README.md「尝试」).
type skips struct {
	noSlot, noEndpoint bool
	unsupported        string               // the first reason an endpoint could not express the request
	failedSuppliers    []routing.SupplierID // stay excluded for this request even after their pause ends
}

// step is what the attempt loop does after an entry.
type step uint8

const (
	nextEntry step = iota // the next entry of the route
	sameEntry             // the entry again, with another credential
	over                  // the call has ended
)

// attempts tries the route's entries in order until one answers, none is left, or something ends the call.
func (s *session) attempts(ctx context.Context) ended {
	maxAttempts := min(s.c.Plan.MaxAttempts, platform.MaxAttempts)
	for i := 0; i < len(s.c.Plan.Entries); {
		if e, stop := s.mayStart(maxAttempts); stop {
			return s.left(e)
		}
		next, e := s.try(ctx, i)
		switch next {
		case over:
			return e
		case nextEntry:
			i++
		case sameEntry:
		}
	}
	return s.left(s.exhausted())
}

// mayStart says whether another attempt may begin, and if not how the call ends. A client that has gone gets nothing,
// a holder that may be gone, or a call at the end of its time, no new attempt (the reservation may no longer cover it),
// and a call past StartBy or on a node going away none either.
func (s *session) mayStart(maxAttempts int) (ended, bool) {
	switch {
	case s.req.Context().Err() != nil:
		return ended{}, true
	case s.x.Outstanding.shutdown.Err() != nil:
		return ended{err: protocol.Error{Type: protocol.Timeout}}, true
	case !s.x.Alive():
		return ended{err: protocol.Error{Type: protocol.Overloaded}, after: 1}, true
	case !time.Now().Before(s.deadline):
		return ended{err: protocol.Error{Type: protocol.Timeout}}, true
	case len(s.ends) >= maxAttempts:
		return s.exhausted(), true
	case time.Now().After(s.c.Plan.StartBy):
		if len(s.ends) == 0 {
			return ended{err: protocol.Error{Type: protocol.Timeout}}, true
		}
		return s.exhausted(), true
	}
	return ended{}, false
}

// exhausted is how a call ends when no entry is left to try. After an attempt it is the upstream's failure whatever
// the last one was (docs/domains/gateway.md「错误」); with none, why every entry was passed over: a credential slot
// that was taken is worth a retry in a second, an endpoint the suppliers do not know is the node's trouble, and what
// is left is a request no endpoint can express.
func (s *session) exhausted() ended {
	switch {
	case len(s.ends) > 0:
		return ended{err: protocol.Error{Type: protocol.UpstreamFailed}}
	case s.skips.noEndpoint:
		return ended{err: protocol.Error{Type: protocol.Overloaded}, after: 1}
	case s.skips.noSlot:
		return ended{err: protocol.Error{Type: protocol.RateLimited}, after: 1}
	}
	return ended{err: protocol.Error{Type: protocol.InvalidRequest, Message: s.skips.unsupported}}
}

// try makes the attempt on entry i, or passes the entry over: no credential to give, a request it cannot express, or a
// retry its supplier's budget does not allow. Only an attempt counts.
func (s *session) try(ctx context.Context, i int) (step, ended) {
	entry := s.c.Plan.Entries[i]
	if slices.Contains(s.skips.failedSuppliers, entry.Supplier) || s.x.Router.Blocked(entry) {
		s.skips.noEndpoint = true
		s.exclude(entry, routing.CoolingDown)
		return nextEntry, ended{}
	}
	conn, err := s.x.Upstreams.Acquire(uuid.UUID(entry.Endpoint))
	if err != nil {
		s.skips.noSlot = s.skips.noSlot || errors.Is(err, suppliers.ErrNoSlot)
		s.skips.noEndpoint = s.skips.noEndpoint || !errors.Is(err, suppliers.ErrNoSlot)
		reason := routing.Full
		if errors.Is(err, suppliers.ErrSupplierResting) {
			reason = routing.SupplierBalance
		}
		s.exclude(entry, reason)
		return nextEntry, ended{}
	}
	up, held, err := s.prepare(entry)
	if errors.Is(err, ErrOverloaded) {
		s.done(conn)
		return over, ended{err: protocol.Error{Type: protocol.Overloaded}, after: 1}
	}
	if unsupported, ok := errors.AsType[*protocol.NotSupported](err); ok {
		s.done(conn)
		s.skipUnsupported(entry, unsupported)
		return nextEntry, ended{}
	} else if err != nil {
		s.done(conn)
		return over, s.unprepared(err)
	}
	if len(s.ends) == 0 {
		s.x.Router.Sent(entry.Supplier)
	} else if !s.x.Router.TakeRetry(entry.Supplier) {
		s.done(conn)
		s.x.Memory.Refund(held)
		s.exclude(entry, routing.RetryBudget)
		return nextEntry, ended{}
	}
	return s.attempt(ctx, i, entry, conn, up, held)
}

// done gives an acquired credential slot back and wakes the oldest queued call: a released slot is what the queue waits
// for, and no other event tells it that capacity returned. The order matters: the slot is free before the waiter looks.
func (s *session) done(conn Conn) suppliers.AttemptReceipt {
	receipt := conn.Done()
	s.x.Limits.Queue.Wake()
	return receipt
}

// skipUnsupported passes over an endpoint that cannot express the request. The first reason is what the client is told
// when no entry is left; why, when it is a key the model's spec lacks, goes to the log and no further.
func (s *session) skipUnsupported(entry routing.Entry, unsupported *protocol.NotSupported) {
	if unsupported.Detail != "" {
		s.logger.Warn("an endpoint cannot take the request: its model spec lacks a key", "detail", unsupported.Detail,
			"supplier", entry.Supplier, "endpoint", entry.Endpoint)
	}
	if s.skips.unsupported == "" {
		s.skips.unsupported = unsupported.Reason
	}
	s.exclude(entry, routing.FormatNotServed)
}

// prepare makes the request ready for an endpoint within the node's memory budget. What Prepare may hold at once is
// charged before it allocates (a charge made after would be an allocation already done), and what stays, the capacity
// of the request as this endpoint takes it, which is a copy of the body, is all that remains charged: held, which the
// attempt gives back once the request is sent. A refusal is ErrOverloaded. The compact call is the one request that is
// not a generation: its body is rewritten by the members that must not go upstream as they are (the endpoint's model
// name, the identifiers, the routing parameters) and nothing else (protocol's Target.Compact).
func (s *session) prepare(entry routing.Entry) (up protocol.Upstream, held int64, err error) {
	account := uuid.UUID(s.c.caller.AccountID())
	w := protocol.Rewrites{
		Identity: s.x.identity(account), Device: s.x.deviceID(account), NativeSearch: s.c.nativeSearch,
		ServerFunction: s.serverFunction, ServerLoop: s.c.serverLoop, HasMaxToolCalls: s.c.Request.HasMaxToolCalls,
	}
	target := entry.Target
	target.Compact = s.c.Compact
	up, held, err = protocol.PrepareBudgeted(s.x.Memory, &s.c.Request, s.c.Body, target, w)
	if errors.Is(err, protocol.ErrMemory) {
		return up, 0, ErrOverloaded
	}
	return up, held, err
}

// unprepared is the end of a call whose request cannot be made ready for an endpoint. A request the client got wrong
// is the client's; anything else is ours.
func (s *session) unprepared(err error) ended {
	if errors.Is(err, protocol.ErrInvalid) {
		return ended{err: protocol.Error{Type: protocol.InvalidRequest, Message: strings.TrimPrefix(err.Error(), protocol.ErrInvalid.Error()+": ")}}
	}
	s.logger.Error("a request could not be prepared for an endpoint", "err", err)
	return ended{err: protocol.Error{Type: protocol.Overloaded}, after: 1}
}

// exclude writes an entry that was passed over into the route's record.
func (s *session) exclude(e routing.Entry, why routing.Reason) {
	s.c.Plan.Record.Excluded = append(s.c.Plan.Record.Excluded, routing.Excluded{Endpoint: e.Endpoint, Reason: why})
}

// record writes how an attempt went into the route's record.
func (s *session) record(a *attempt, failed bool, scope routing.Scope, next routing.Action) {
	s.c.Plan.Record.Attempts = append(s.c.Plan.Record.Attempts, routing.Attempt{
		Endpoint: a.entry.Endpoint, Failed: failed, Scope: scope, Action: next,
	})
}

// attempt is one try at an endpoint, from the moment its identity is fixed. The goroutine that runs the call owns it.
// A model-side loop runs inside it, one supplier request per sampling: each is an attempt of its own in s.ends, with its
// own number, slot, receipt and cost, while the stream, its meter and its timers stay with this one.
type attempt struct {
	s     *session
	first int // index in s.ends of the attempt's first supplier request
	i     int // index in s.ends of the supplier request now running, or the last one
	entry routing.Entry
	conn  Conn
	meter *billing.Meter // bound to the entry that was priced

	// toolCallTokens is what each client tool call's framework adds to the output in this entry's family, the same
	// figure settlement uses, so the meter's bound and the estimate agree.
	toolCallTokens int64

	cancel  context.CancelCauseFunc
	release context.CancelFunc
	unhook  func() bool // of the node's shutdown, which cancels the attempt

	content     bool              // the upstream sent content: the commit point came
	capture     *cacheCapture     // the upstream body's bounded copy, nil when this call does not cache
	state       string            // the turn state the upstream set, passed on with the answer (headers.go)
	usage       protocol.Usage    // what the stream reported, once it is over
	seen        protocol.Progress // what the relay had seen last
	hops        []billing.Segment // a loop sampling's usage, estimated where the upstream did not report it
	hopRecorded bool              // whether this sampling's usage is in hops
	nextBody    io.ReadCloser
	nextUsage   protocol.UsageContext

	asked   billing.ExtendRequest
	landing chan error // the outcome of the extension asked for, nil when none is in flight

	obs quality.Observation // how it ended, as quality is told when it closes
}

// bind fixes the attempt's identity before anything is sent: its number, and the entry whose price the reservation
// holds, which Bind refuses to hand to an entry the reservation dropped (I1). From here the attempt counts, whatever
// becomes of it.
func (s *session) bind(ctx context.Context, i int, entry routing.Entry, conn Conn) (*attempt, context.Context) {
	kept := s.c.Hold.Kept[i]
	a := &attempt{
		s: s, entry: entry, conn: conn, meter: s.c.Hold.Bind(kept),
		toolCallTokens: billing.ToolCallTokens(billing.TokenizerOf(entry.Target.Model)),
	}
	a.obs.Endpoint, a.obs.Sent = uuid.UUID(entry.Endpoint), time.Now()
	a.obs.Source = uuid.UUID(s.c.Admission.OwnerID())
	a.obs.Request = s.c.ID
	a.obs.Config = quality.EndpointConfig{Model: uuid.UUID(entry.Model), Upstream: entry.Target.Model, Formats: uint16(entry.Formats)}
	a.first = s.begin(entry, kept, false)
	a.i = a.first
	s.currency = entry.Price.Schedule.Currency()
	s.last = a
	policy := conn.Policy()
	ctx, release := AttemptContext(ctx, s.deadline, policy.Cancels)
	ctx, a.cancel = context.WithCancelCause(ctx)
	a.release = release
	a.unhook = context.AfterFunc(s.x.Outstanding.shutdown, func() { a.cancel(errShutdown) })
	s.x.Router.Begin(entry.Endpoint)
	s.setCancel(a.cancel, policy.Cancels)
	return a, ctx
}

// begin numbers a supplier request: its place in the settlement's attempts and the call log's, in step. continues says
// it is a further sampling of the previous request's loop. A fallback is only begun while fewer than
// platform.MaxAttempts attempts were made, a loop's samplings counted (mayStart), so every attempt that does not
// continue a loop is numbered within platform.MaxAttempts, which every release's correction worker reads; a loop adds
// at most one attempt a step, platform.MaxServerToolSteps for the call, so the number stays within MaxAttemptSeq.
func (s *session) begin(entry routing.Entry, kept int, continues bool) int {
	s.ends = append(s.ends, billing.AttemptResult{Seq: len(s.ends) + 1, Entry: kept, Result: billing.Unknown, Continues: continues})
	s.tries = append(s.tries, try{entry: entry, started: time.Now()})
	return len(s.ends) - 1
}

// close ends the attempt: its upstream is cancelled if it still runs, the credential's slot and the endpoint's count
// are given back, what billing takes of its last supplier request is made from its usage and the receipt, and quality
// is told how it ended. ctx is the attempt's, by then cancelled, which only quality's counters read.
func (a *attempt) close(ctx context.Context) {
	a.s.setCancel(nil, false)
	a.unhook()
	a.cancel(nil)
	a.release()
	a.s.x.Router.End(a.entry.Endpoint) // before the wake: a waiter must find both the endpoint's count and the slot free
	if a.conn != nil {
		a.endRequest()
	}
	a.observe(ctx)
}

// endRequest ends the supplier request now held: its slot is given back, and its own record is priced with its own
// receipt, which has the supplier's request id. A loop ends one per sampling, so each charge reconciles alone.
func (a *attempt) endRequest() {
	receipt := a.s.done(a.conn)
	a.conn = nil
	a.obs.Credential = receipt.CredentialID()
	a.result(receipt)
}

// observe tells quality how the attempt ended, once, as it closes, with the input it was estimated at and the output
// it is charged for (docs/domains/quality.md「什么算一次失败」).
func (a *attempt) observe(ctx context.Context) {
	s, o := a.s, &a.obs
	o.Gone = s.isGone()
	if o.Ended.IsZero() { // cut by us before the upstream said it was done
		o.Ended = time.Now()
	}
	r := &s.c.Request
	o.InputTokens = billing.InputEstimate(billing.Demand{TextBytes: r.TextBytes, Images: r.Images, Documents: r.Documents}, a.entry.Limits)
	o.ToolChecks, o.ToolErrors = a.seen.ToolChecks, a.seen.ToolErrors // the answering reply's, the last the relay read
	for _, rec := range s.ends[a.first : a.i+1] {
		for _, seg := range rec.Segments {
			o.OutputTokens += seg.Usage.OutputTotal()
		}
	}
	// The fingerprint's usage evidence comes from the same response as its headers, the first supplier request, and
	// only from what that upstream reported: an estimate the gateway filled in is not something the supplier said.
	for _, seg := range s.ends[a.first].Segments {
		for it := range catalog.NumItems {
			if seg.Reported.Has(it) && seg.Usage.Count(it) > 0 {
				o.UsageItems.Add(it)
			}
		}
	}
	s.x.Quality.Observe(ctx, *o)
}

// cutOf is why the gateway cancelled an attempt, read from its context: not at all, one of the two waits quality
// judges, or anything else, which is the call's own end (its deadline, the node's shutdown, an extension refused, the
// client leaving, which quality tells by Gone).
func cutOf(ctx context.Context) quality.Cut {
	cause := context.Cause(ctx)
	switch {
	case ctx.Err() == nil:
		return quality.NotCut
	case errors.Is(cause, errHeaderWait):
		return quality.HeaderWait
	case errors.Is(cause, errIdle):
		return quality.Idle
	}
	return quality.Ours
}

// attempt runs one try: send, and either a failure to decide on or an answer to read, a stream for a client relayed, a
// whole body for one that is passed through.
func (s *session) attempt(ctx context.Context, i int, entry routing.Entry, conn Conn, up protocol.Upstream, held int64) (step, ended) {
	s.let() // another attempt answers the call: the reply kept before it is not part of it
	if s.c.serverLoop && s.loop == nil {
		s.loop = newServerLoop(s.c)
	}
	a, ctx := s.bind(ctx, i, entry, conn)
	defer a.close(ctx)
	resp, fail := a.send(ctx, up, held)
	if fail != nil {
		a.obs.Failure, a.obs.Cut, a.obs.Ended = fail, cutOf(ctx), time.Now()
		return s.failed(a, fail)
	}
	defer resp.Body.Close()     //nolint:errcheck // the stream is over or abandoned; nothing to learn from the close
	a.obs.Headers = resp.Header // the fingerprint evidence source classes are recognized by
	a.state = strings.TrimSpace(resp.Header.Get(stateHeader))
	if s.c.Compact || s.c.Format.Direct() {
		return a.pass(ctx, resp)
	}
	return a.stream(ctx, resp.Body, up.Usage)
}

// send posts the request, within the wait for the response headers, which is when the supplier's request id comes. The
// prepared body, held in the node's budget, is given back as soon as it has been sent. The compact endpoint is the one
// whose reply is not a stream: it gets a longer wait for its headers, and its own path (docs/domains/gateway.md
// 「路径与格式」).
func (a *attempt) send(ctx context.Context, up protocol.Upstream, held int64) (*http.Response, *suppliers.Failure) {
	s := a.s
	defer s.x.Memory.Refund(held)
	waitFor := s.x.Limits.cfg.HeaderWait
	call := suppliers.Call{Format: a.entry.Target.Format, Body: up.Body, Header: a.upstreamHeader()}
	if s.c.Compact {
		waitFor, call.Compact = s.x.Limits.cfg.CompactHeaderWait, true
	}
	wait := time.AfterFunc(waitFor, func() { a.cancel(errHeaderWait) })
	resp, fail := a.conn.Send(ctx, call)
	wait.Stop() // at once: a late firing would cancel the stream the headers began
	return resp, fail
}

// failed decides what to do after an attempt the supplier answered with a failure, or did not answer: whether the
// request ran is the adapter's word (NotExecuted) or unknown, and routing says whether to try another credential, the
// next entry, or give the client the error.
//
//nolint:cyclop // one branch for the search no-retry rule
func (s *session) failed(a *attempt, f *suppliers.Failure) (step, ended) {
	s.ends[a.i].Result = billing.Unknown
	if s.isGone() { // the client left while the request was out: the failure is the cancel's, and cancelling is never free
		s.ends[a.i].Finish = billing.FinishCancelled
	}
	if f.Executed == suppliers.NotExecuted {
		s.ends[a.i].Result = billing.NotExecuted
	}
	scope := routingScope(f.Scope)
	limit, limited := f.Cause.(suppliers.RateLimited)
	_, outOfFunds := f.Cause.(suppliers.SupplierBalanceLow)
	if limited || scope == routing.ScopeSupplier && !outOfFunds {
		s.x.Router.Block(a.entry, scope, limit.RetryAfter)
	}
	if limited {
		s.x.Router.Throttled(a.entry.Endpoint)
	}
	if scope == routing.ScopeSupplier {
		s.skips.failedSuppliers = append(s.skips.failedSuppliers, a.entry.Supplier)
	}
	next := routing.Decide(routing.Failure{Scope: scope})
	searchEnded := s.searched && (next == routing.NextEntry || next == routing.OtherCredential)
	if searchEnded {
		// A search already ran on this call's account: another entry or credential would have to run it again, and the
		// searches already run stay charged. A request-scoped refusal keeps its meaning; anything else is the upstream
		// failing mid-loop, which the client sees as 502 rather than a 400 made from an empty rejection.
		next = routing.ReturnToClient
	}
	s.record(a, true, scope, next)
	switch next {
	case routing.ReturnToClient: // the request itself is refused: the client gets what the supplier said of it, cleaned of its name
		if searchEnded && scope != routing.ScopeRequest {
			return over, ended{err: protocol.Error{Type: protocol.UpstreamFailed}}
		}
		return over, ended{err: requestFailure(f)}
	case routing.OtherCredential:
		return sameEntry, ended{}
	case routing.NextEntry:
	}
	return nextEntry, ended{}
}

// routingScope is the scope routing decides by for the scope an adapter classified a failure with. The two types list
// the same scopes, but each in its own module's order, so they are matched by name rather than by number.
func routingScope(s suppliers.Scope) routing.Scope {
	switch s {
	case suppliers.ScopeRequest:
		return routing.ScopeRequest
	case suppliers.ScopeEgress:
		return routing.ScopeEgress
	case suppliers.ScopeEndpoint:
		return routing.ScopeEndpoint
	case suppliers.ScopeCredential:
		return routing.ScopeCredential
	case suppliers.ScopeGroup:
		return routing.ScopeGroup
	case suppliers.ScopeSupplier:
		return routing.ScopeSupplier
	}
	panic("gateway: a failure scope routing does not have")
}
