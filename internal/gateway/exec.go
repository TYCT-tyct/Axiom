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
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// Conn is one attempt's connection to a credential of an endpoint, held from Acquire to Done. *suppliers.Attempt is one.
type Conn interface {
	Send(ctx context.Context, call suppliers.Call) (*http.Response, *suppliers.Failure)
	Policy() suppliers.CallPolicy
	Receipt() suppliers.AttemptReceipt
	Done() suppliers.AttemptReceipt
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
	Extend(ctx context.Context, h *billing.Hold, r billing.ExtendRequest) error
	Settle(ctx context.Context, h *billing.Hold, o billing.Outcome) (billing.Final, error)
}

// Costs prices an attempt's usage at what its supplier charges us, in billionths of a credit of currency, the one the
// attempt's price is in; *suppliers.Service is one. known is false when that leaves part of the cost out or none of it
// is known, and Cost without a currency is the native amount the same schedule gives, which is what suppliers stores of
// the attempt for reconciliation (docs/domains/billing.md「免单规则」「供应商成本」).
type Costs interface {
	Credits(r suppliers.AttemptReceipt, usage []catalog.Usage, currency platform.Currency) (cost int64, known bool, err error)
	Cost(r suppliers.AttemptReceipt, usage []catalog.Usage) (suppliers.AttemptCost, error)
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
}

// Executor runs the calls the Handler admits: the attempts down the route, the relay of the answer, and the settlement.
type Executor struct {
	ExecDeps
	bg       *background        // the calls running and the settlements that outlive them
	shutdown context.Context    //nolint:containedctx // not a request's: the executor's own, which every attempt's context.AfterFunc watches; done once Drain gives up waiting
	abort    context.CancelFunc // ends shutdown
}

// minSecret is the shortest identity secret accepted.
const minSecret = 16

// NewExecutor checks the dependencies.
func NewExecutor(d ExecDeps) (*Executor, error) {
	if d.Router == nil || d.Upstreams == nil || d.Limits == nil || d.Memory == nil || d.Logger == nil || d.Ledger == nil ||
		d.Costs == nil || d.Alive == nil {
		return nil, errors.New("gateway: the executor needs every dependency")
	}
	if len(d.IdentitySecret) < minSecret {
		return nil, errors.New("gateway: the identity secret is too short")
	}
	shutdown, abort := context.WithCancel(context.Background())
	return &Executor{ExecDeps: d, bg: newBackground(), shutdown: shutdown, abort: abort}, nil
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
// through with it and whose memory is given back once it is written.
type ended struct {
	err    protocol.Error
	after  int // Retry-After of the answer, in seconds
	relay  *protocol.Relay
	done   bool
	reply  []byte
	status int
	held   int64
}

// Run is Deps.Run: it takes the call from admission to the end of its answer, and settles it once however it ends.
// The call's slots go when its settlement is written, or once it is the cleanup's (README.md「结束与结算」), which may
// be after Run returns.
func (x *Executor) Run(w http.ResponseWriter, r *http.Request, c *Call) {
	defer x.bg.add()()
	s := newSession(x, w, r, c)
	defer func() {
		if !s.handedOff {
			c.Done()
		}
	}()
	defer s.finish(nil) // whatever the way out, the keep-alive stops before the handler returns
	defer s.let()       // a reply no answer was made of, as a panic leaves, is let go
	ctx := r.Context()
	defer s.settleOnPanic(ctx)
	s.conclude(ctx, s.attempts(ctx))
}

// Drain lets the calls in flight and the settlements in the background end, until ctx does; then it cancels every
// attempt, as a node that is going away must, and gives what is left grace to settle. What still has not is the
// cleanup's (README.md「故障矩阵」). A call that begins meanwhile is waited for too.
func (x *Executor) Drain(ctx context.Context, grace time.Duration) {
	if x.bg.wait(ctx) {
		return
	}
	x.abort()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	x.bg.drain(ctx)
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

	ends     []billing.AttemptResult // the attempts made, in order; the last is the one charged
	currency platform.Currency       // of the last attempt's price
	skips    skips
	last     *attempt // the attempt in progress, or the last one made

	aggregate bool            // the client wants one body: its stream is aggregated, and nothing is written before it is whole
	part      *protocol.Relay // the aggregate reply of an attempt that generated content, kept for the answer

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
		x: x, w: w, rc: http.NewResponseController(w), c: c, req: r, start: now,
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
	o := billing.Outcome{Attempts: s.ends, Cancelled: s.isGone()}
	f, err := s.x.settle(ctx, s.c, o, time.Now().Add(s.x.Limits.cfg.SettleRetry))
	var fin *protocol.Final
	if err == nil && len(o.Attempts) > 0 {
		fin = s.final(f)
	}
	switch {
	case e.reply != nil:
		s.answerReply(e)
	case e.relay == nil:
		if fin != nil && fin.Cost.Cmp(platform.Exact{}) == 0 {
			fin = nil // an error that cost nothing says nothing of the charge
		}
		s.answer(e, fin)
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
	s.handedOff = s.x.settled(ctx, s.c, o, err)
}

// answerAggregate writes the whole body of a call that did not stream, after its settlement (I3): the reply the relay
// aggregated, or the failure, which carries what was generated and the error for a Chat or Responses client and is the
// error alone for a Messages one (docs/domains/gateway.md「错误」). Finish and Fail return the body and the status of a
// relay that has not ended yet, which is always the case here.
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

// answerReply writes the body an upstream answered whole, as the compact endpoint does: it goes to the client as it came
// (docs/domains/gateway.md「转发规则」), after the settlement, which nothing in it reports.
func (s *session) answerReply(e ended) {
	defer s.x.Memory.Refund(e.held)
	s.respond(e.status, e.reply)
}

// let drops the aggregate reply kept for the answer, if there is one: the call is over, or another attempt replaced it.
func (s *session) let() {
	if s.part != nil {
		drop(s.part)
		s.part = nil
	}
}

// final is what the ending reports of a settlement.
func (s *session) final(f billing.Final) *protocol.Final {
	fin := &protocol.Final{Cost: f.Charged, Currency: s.currency, Estimated: f.Estimated}
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
		_, err := s.x.settle(ctx, s.c, o, time.Now().Add(s.x.Limits.cfg.SettleRetry))
		s.handedOff = s.x.settled(ctx, s.c, o, err)
	}
	panic(p)
}
