package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/observability"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// Conn is one attempt's connection to a credential of an endpoint, held from Acquire to Done. *suppliers.Attempt is one.
type Conn interface {
	Send(ctx context.Context, call suppliers.Call) (*http.Response, *suppliers.Failure)
	Policy() suppliers.CallPolicy
	Done() suppliers.AttemptReceipt
	// Sanitize removes supplier identity using the same rules as HTTP rejections. Only suppliers knows the credential
	// and origin; protocol bounds the raw fields before handing them over. nil means no message remains.
	Sanitize(protocol.Rejection) *protocol.Rejection
}

// Upstreams is the call side of suppliers as the attempts use it. Acquire returns suppliers.ErrNoEndpoint or
// suppliers.ErrNoSlot when it has no credential to give; neither is an attempt.
type Upstreams interface {
	Acquire(id suppliers.EndpointID) (Conn, error)
}

type serviceUpstreams struct{ *suppliers.Service }

// NewUpstreams is the suppliers service as Upstreams.
func NewUpstreams(s *suppliers.Service) Upstreams { return serviceUpstreams{s} }

func (u serviceUpstreams) Acquire(id suppliers.EndpointID) (Conn, error) {
	a, err := u.Service.Acquire(id)
	if err != nil {
		return nil, err // not a nil *Attempt inside a non-nil Conn
	}
	return a, nil
}

// Ledger is what a call writes of its money once admitted, *billing.Billing: the extensions of its reservation and
// the settlement that ends it.
type Ledger interface {
	ExtendIn(ctx context.Context, tx pg.Tx, h *billing.Hold, r billing.ExtendRequest) error
	SettleIn(ctx context.Context, tx pg.Tx, h *billing.Hold, o billing.Outcome) (billing.Final, func(), error)
	// SettleBatch settles a completion batch's settlements in one transaction: one balance-row lock, one read of
	// the reservations, the ledger in one pipeline, one set-based reservation end and one request-end insert, instead
	// of those round trips per call.
	SettleBatch(ctx context.Context, scope pg.Scope, items []billing.Settlement) ([]billing.Final, []func(), error)
	// RunBatch commits the works of one completion batch together: one begin, one commit, one connection.
	RunBatch(ctx context.Context, scope pg.Scope, works []func(pg.Tx) error) error
}

// Costs prices an attempt's usage at what its supplier charges us, in billionths of a credit of currency, the one the
// attempt's price is in; *suppliers.Service is one. known is false when that leaves part of the cost out or none of it
// is known: the attempt is then booked at whatever came back, which is the platform's cost and never the user's charge
// (docs/domains/billing.md「供应商成本」). The AttemptCost is the same pricing in the supplier's own unit, which is what
// suppliers stores of the attempt for reconciliation.
type Costs interface {
	Credits(r suppliers.AttemptReceipt, usage []catalog.Usage, currency platform.Currency) (c suppliers.AttemptCost, cost int64, known bool, err error)
}

// ExecDeps are what an Executor needs. Every one is required.
type ExecDeps struct {
	Router    *routing.Router
	Upstreams Upstreams
	Limits    *Backpressure
	Memory    *protocol.Memory // the node's budget for streams
	Logger    *slog.Logger
	Ledger    Ledger
	Costs     Costs
	// Alive is billing.Alive: a call whose holder may be gone starts no attempt.
	Alive func() bool
	// IdentitySecret derives the identifiers sent upstream in place of the client's session and end-user ones; it is
	// the same for every node, or a session's cache would not follow it from one to another.
	IdentitySecret []byte
	// Quality is told how every attempt ended, and measures the endpoints routing ranks by.
	Quality *quality.Monitor
	// Records is the call log, where each call is written once its settlement is. It is optional because the record is
	// observation and a call never depends on it; a node always gives it.
	Records *observability.Recorder
	// Outstanding counts the calls and the settlements they leave in the background, and is what the node drains: the
	// node's Handler shares it, so a request is counted from the gate.
	Outstanding *Outstanding
}

// Executor runs the calls the Handler admits: the attempts down the route, the relay of the answer, and the settlement.
type Executor struct {
	ExecDeps
}

// minSecret is the shortest identity secret accepted.
const minSecret = 16

// NewExecutor checks the dependencies.
func NewExecutor(d ExecDeps) (*Executor, error) {
	if d.Router == nil || d.Upstreams == nil || d.Limits == nil || d.Memory == nil || d.Logger == nil || d.Ledger == nil ||
		d.Costs == nil || d.Alive == nil || d.Quality == nil || d.Outstanding == nil {
		return nil, errors.New("gateway: the executor needs every dependency")
	}
	if len(d.IdentitySecret) < minSecret {
		return nil, errors.New("gateway: the identity secret is too short")
	}
	return &Executor{ExecDeps: d}, nil
}

// identity maps a value the client sent to the opaque one that goes upstream: the same value of the same account
// always gives the same identifier, another account's never does, and it is short enough for Prepare.
func (x *Executor) identity(account uuid.UUID) func(string) string {
	return func(v string) string {
		mac := hmac.New(sha256.New, x.IdentitySecret)
		mac.Write(account[:])
		mac.Write([]byte(v))
		return hex.EncodeToString(mac.Sum(nil)[:16])
	}
}

// ended is how the attempts ended the call. err answers it when nothing was written to the client yet; relay is the
// stream the call ends in, nil when no attempt got as far as one that the call ends with; done says the upstream ended
// normally. reply is the whole body of a call the upstream answered at once, the compact one, whose status passes
// through with it and whose memory is the session's replyHeld.
type ended struct {
	err    protocol.Error
	after  int // Retry-After of the answer, in seconds
	relay  *protocol.Relay
	done   bool
	reply  []byte
	status int
}

// Run is Deps.Run: it takes the call from admission to the end of its answer, and settles it once however it ends.
// The call's slots go when its settlement is written, or once it is the cleanup's (README.md「结束与结算」), which may
// be after Run returns.
func (x *Executor) Run(w http.ResponseWriter, r *http.Request, c *Call) {
	defer x.Outstanding.add()() // nested in the handler's count on a node; the count of a call run on its own
	s := newSession(x, w, r, c)
	defer func() {
		if !s.handedOff {
			c.Done()
		}
	}()
	defer s.finish(nil) // whatever the way out, the keep-alive stops before the handler returns
	defer s.release()   // the relays no answer released, as a panic leaves them, are let go
	ctx := r.Context()
	defer s.settleOnPanic(ctx)
	s.conclude(ctx, s.attempts(ctx))
}

// session is the run of one call. The goroutine that runs it owns it, but for the keep-alive timer and the cancelling
// of an attempt, which share the write lock's state.
type session struct {
	x        *Executor
	w        http.ResponseWriter
	rc       *http.ResponseController
	c        *Call
	req      *http.Request
	start    time.Time
	deadline time.Time // the longest the call may run
	logger   *slog.Logger

	ends       []billing.AttemptResult // the attempts made, in order; the last is the one charged
	tries      []try                   // the same attempts as the call log needs them: their entry and when they began
	currency   platform.Currency       // of the last attempt's price, the site's until one is made
	skips      skips
	last       *attempt                                // the attempt in progress, or the last one made
	textCounts [billing.NumTokenizers - 1]textEstimate // one count (or failure) per family with a count, for this session

	aggregate bool            // the client wants one body: its stream is aggregated, and nothing is written before it is whole
	part      *protocol.Relay // the aggregate reply of an attempt that generated content, kept for the answer
	// state is the turn state the upstream of the attempt whose content answers the call set, which the client gets
	// back with it: the upstream's own, which no attempt the call gave up on can put there (headers.go).
	state string
	// live is the relay the call must release however it ends, panic included: the one being read, then the one the
	// call ends with. It is the backstop for a way out that skips the release; drop clears it once a relay is let go,
	// so that a discarded relay's buffers are not kept reachable (and the budget they were charged against reused)
	// while the next attempt waits on its upstream. A kept part is always the live one too.
	live      *protocol.Relay
	replyHeld int64 // of the node's budget, held by the compact reply the call ends with, until Run returns

	concluded bool
	handedOff bool // the call's slots are the background's to give back

	wmu       sync.Mutex // guards what follows and every write to the client
	started   bool       // the stream began: nothing is retried from here (I4)
	gone      bool       // a write failed: the client is not reading
	closed    bool       // the ending is being written: no more keep-alives
	keepAlive *time.Timer
	writeFor  time.Duration           // the deadline of one write
	cancel    context.CancelCauseFunc // of the attempt in progress, nil between attempts
	follows   bool                    // whether cancelling the client cancels that attempt
}

func newSession(x *Executor, w http.ResponseWriter, r *http.Request, c *Call) *session {
	now := time.Now()
	return &session{
		x: x, w: w, rc: http.NewResponseController(w), c: c, req: r, start: now, currency: c.site.Currency,
		deadline: now.Add(x.Limits.cfg.MaxCall), logger: x.Logger.With("request_id", c.ID.String()),
		writeFor: x.Limits.cfg.WriteTimeout, aggregate: !c.Request.Stream && !c.Compact,
	}
}

// conclude settles the call and writes what is left to tell the client. The order is the one that keeps the money
// right: the attempt is over (its upstream cancelled, its read returned) before Settle, and Settle is committed before
// the ending that reports the cost is written (I3). A settlement not written in time goes on in the background once
// the ending is out.
func (s *session) conclude(ctx context.Context, e ended) {
	s.concluded = true
	s.stick() //nolint:contextcheck // the queued write outlives the call: Later never waits, and the request's context is often over by now
	o := billing.Outcome{Attempts: s.ends}
	f, err := s.x.settle(ctx, s.c, o, time.Now().Add(s.x.Limits.cfg.SettleRetry))
	var fin *protocol.Final
	if err == nil && len(o.Attempts) > 0 {
		fin = s.final(f)
	}
	switch {
	case e.reply != nil:
		s.respond(e.status, e.reply) // as it came, after the settlement (docs/domains/gateway.md「转发规则」)
	case e.relay == nil:
		if fin != nil && fin.Cost.Cmp(platform.Exact{}) == 0 {
			fin = nil // an error that cost nothing says nothing of the charge
		}
		s.answer(ctx, e, fin)
	case s.aggregate:
		s.answerAggregate(e, fin, err)
	case e.done:
		final := protocol.Final{Pending: err != nil}
		if fin != nil {
			final = *fin
		}
		s.finish(e.relay.Finish(final))
	default:
		s.finish(e.relay.Fail(e.err, fin))
	}
	s.handedOff = s.x.settled(ctx, s, o, err)
	if err == nil {
		s.x.log(ctx, s.trail(), o, f, time.Now())
	}
}

// stick moves the session to the endpoint of the charged attempt when that attempt delivered content and the session
// was not already there (docs/domains/routing.md「会话粘性」). A call that delivered nothing writes nothing and forgets
// nothing: the entry stays until its time is up or a later call succeeds elsewhere. It runs before the settlement and the
// ending. The process table is updated immediately; the shared cache is offered a bounded background write, so the
// next request may arrive before that binding is visible (README.md「并发、背压与共享状态」).
func (s *session) stick() {
	if len(s.ends) == 0 {
		return
	}
	last := len(s.ends) - 1
	if r := s.ends[last].Result; r != billing.Committed && r != billing.Aggregated {
		return
	}
	if ep := s.tries[last].entry.Endpoint; ep != s.c.sticky {
		if cache := s.c.stickyCache; cache != nil {
			cache.Later(uuid.UUID(s.c.caller.AccountID()), s.x.Router.StickyWrite(s.c.stickyKey, ep, s.c.Plan.StickyTTL))
		} else {
			s.x.Router.Stick(s.c.Session, ep, s.c.Plan.StickyTTL)
		}
	}
}

// answerAggregate writes the whole body of a call that did not stream, after its settlement (I3): the reply the relay
// aggregated, or the failure, which carries what was generated and the error for a Chat or Responses client and is the
// error alone for a Messages one (docs/domains/gateway.md「错误」). Finish and Fail return the body and the status of a
// relay that has not ended yet, which is always the case here. The body is the relay's buffers, so its share of the
// node's budget stays charged until the call is over (session.live), as the compact reply's is (replyHeld).
func (s *session) answerAggregate(e ended, fin *protocol.Final, err error) {
	final := protocol.Final{Pending: err != nil}
	if fin != nil {
		final = *fin
	}
	var body []byte
	if e.done {
		body = e.relay.Finish(final)
	} else {
		if fin != nil && fin.Cost.Cmp(platform.Exact{}) == 0 {
			fin = nil // an error that cost nothing says nothing of the charge
		}
		body = e.relay.Fail(e.err, fin)
	}
	status := e.relay.Status()
	s.respond(status, body)
}

// let drops the aggregate reply kept for the answer, if there is one: the call is over, or another attempt replaced it.
func (s *session) let() {
	if s.part != nil {
		s.drop(s.part)
		s.part = nil
	}
}

// release gives back what the call still holds of the node's budget: the kept reply, the live relay and the compact
// reply.
func (s *session) release() {
	s.let()
	s.dropReply()
	if s.live != nil {
		s.live.Release()
		s.live = nil
	}
}

// dropReply gives back the budget of a compact reply that is not going to be written, or that the call is over with.
func (s *session) dropReply() {
	s.x.Memory.Refund(s.replyHeld)
	s.replyHeld = 0
}

// final is what the ending reports of a settlement.
func (s *session) final(f billing.Final) *protocol.Final {
	fin := &protocol.Final{
		Cost: f.Charged, Currency: s.currency, Estimated: f.Estimated,
		ServerToolCost: f.ServerToolCost, CacheDiscount: f.CacheDiscount,
	}
	for _, seg := range f.Segments {
		fin.Usage.Segments = append(fin.Usage.Segments, protocol.Segment{Usage: seg.Usage, Reported: seg.Reported})
	}
	return fin
}

// settleOnPanic settles a call whose run panicked, with the attempts it had made, and lets the panic go on: the
// reservation must not wait for the cleanup because of a bug.
func (s *session) settleOnPanic(ctx context.Context) {
	p := recover()
	if p == nil {
		return
	}
	if !s.concluded {
		o := billing.Outcome{Attempts: s.ends}
		f, err := s.x.settle(ctx, s.c, o, time.Now().Add(s.x.Limits.cfg.SettleRetry))
		s.handedOff = s.x.settled(ctx, s, o, err)
		if err == nil {
			s.x.log(ctx, s.trail(), o, f, time.Now())
		}
	}
	panic(p)
}
