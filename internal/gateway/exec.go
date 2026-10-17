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
	Journal(suppliers.JournalEntry)
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

// Ending is how a call's attempts ended, which is what the settlement prices: billing charges the last, and records
// the others' cost. Cancelled is whether the client was gone before the call ended.
type Ending struct {
	Attempts  []AttemptEnd
	Cancelled bool
}

// AttemptEnd is one attempt. Entry is the index in the route that was reserved, which billing bound the meter to;
// Receipt names the credential, the cost schedule and the supplier's request id; Usage is what the stream reported.
type AttemptEnd struct {
	Seq     int
	Entry   int
	Receipt suppliers.AttemptReceipt
	Result  billing.Result
	Finish  billing.FinishReason
	Usage   protocol.Usage
}

// SettleFunc ends a call's reservation from how its attempts ended, and says what the ending reports of the charge:
// nil when nothing was charged. It is what G7 supplies (internal/gateway/README.md「结束与结算」); an error means the
// settlement could not be written, which a stream that completed reports as billing_pending.
type SettleFunc func(ctx context.Context, c *Call, e Ending) (*protocol.Final, error)

// settleAttempt is how long one try of a settlement may take.
const settleAttempt = 2 * time.Second

// ExecDeps are what an Executor needs. Every one is required.
type ExecDeps struct {
	Router    *routing.Router
	Upstreams Upstreams
	Limits    *Backpressure
	Memory    *protocol.Memory // the node's budget for streams
	Logger    *slog.Logger
	// Settle ends every call, once.
	Settle SettleFunc
	// Alive is billing.Alive: a call whose holder may be gone starts no attempt.
	Alive func() bool
	// IdentitySecret derives the identifiers sent upstream in place of the client's session and end-user ones; it is
	// the same for every node, or a session's cache would not follow it from one to another.
	IdentitySecret []byte
}

// Executor runs the calls the Handler admits: the attempts down the route and the relay of the answer.
type Executor struct{ ExecDeps }

// minSecret is the shortest identity secret accepted.
const minSecret = 16

// NewExecutor checks the dependencies.
func NewExecutor(d ExecDeps) (*Executor, error) {
	if d.Router == nil || d.Upstreams == nil || d.Limits == nil || d.Memory == nil || d.Logger == nil || d.Settle == nil || d.Alive == nil {
		return nil, errors.New("gateway: the executor needs every dependency")
	}
	if len(d.IdentitySecret) < minSecret {
		return nil, errors.New("gateway: the identity secret is too short")
	}
	return &Executor{d}, nil
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
// normally.
type ended struct {
	err   protocol.Error
	after int // Retry-After of the answer, in seconds
	relay *protocol.Relay
	done  bool
}

// Run is Deps.Run: it takes the call from admission to the end of its answer, and settles it once however it ends.
// A request that wants no stream is refused until G8 aggregates it.
func (x *Executor) Run(w http.ResponseWriter, r *http.Request, c *Call) {
	defer c.Done()
	s := newSession(x, w, r, c)
	defer s.finish(nil) // whatever the way out, the keep-alive stops before the handler returns
	ctx := r.Context()
	defer s.settleOnPanic(ctx)
	if !c.Request.Stream {
		s.conclude(ctx, ended{err: protocol.Error{Type: protocol.InvalidRequest, Message: "Requests that do not stream are not supported yet; set stream to true."}})
		return
	}
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

	ends  []AttemptEnd // the attempts made, in order; the last is the one charged
	skips skips

	concluded bool

	wmu       sync.Mutex // guards what follows and every write to the client
	started   bool       // the stream began: nothing is retried from here (I4)
	gone      bool       // a write failed: the client is not reading
	closed    bool       // the ending is being written: no more keep-alives
	keepAlive *time.Timer
	cancel    context.CancelCauseFunc // of the attempt in progress, nil between attempts
	follows   bool                    // whether cancelling the client cancels that attempt
}

func newSession(x *Executor, w http.ResponseWriter, r *http.Request, c *Call) *session {
	now := time.Now()
	return &session{
		x: x, w: w, rc: http.NewResponseController(w), c: c, req: r, start: now,
		deadline: now.Add(x.Limits.cfg.MaxCall), logger: x.Logger.With("request_id", c.ID.String()),
	}
}

// conclude settles the call and writes what is left to tell the client. The order is the one that keeps the money
// right: the attempt is over (its upstream cancelled, its read returned) before Settle, and Settle is committed before
// the ending that reports the cost is written (I3).
func (s *session) conclude(ctx context.Context, e ended) {
	s.concluded = true
	ctx, cancel := SettleContext(ctx, settleAttempt)
	defer cancel()
	cancelled := s.isGone()
	fin, err := s.x.Settle(ctx, s.c, Ending{Attempts: s.ends, Cancelled: cancelled})
	if err != nil {
		s.logger.Warn("a settlement was not written", "err", err)
		fin = nil
	}
	switch {
	case e.relay == nil:
		s.answer(e, fin)
	case e.done:
		f := protocol.Final{Pending: err != nil}
		if fin != nil {
			f = *fin
		}
		s.finish(e.relay.Finish(f))
	default:
		s.finish(e.relay.Fail(e.err, fin))
	}
}

// settleOnPanic settles a call whose run panicked, with the attempts it had made, and lets the panic go on: the
// reservation must not wait for the cleanup because of a bug.
func (s *session) settleOnPanic(ctx context.Context) {
	p := recover()
	if p == nil {
		return
	}
	if !s.concluded {
		ctx, cancel := SettleContext(ctx, settleAttempt)
		defer cancel()
		if _, err := s.x.Settle(ctx, s.c, Ending{Attempts: s.ends}); err != nil {
			s.logger.Error("a settlement after a panic was not written", "err", err)
		}
	}
	panic(p)
}
