//nolint:revive // the fixture helpers are one shared table; splitting them would move the harness, not shorten it
package gateway

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
	"axiom/internal/websearch"
)

// The executor's tests run in a synctest bubble, with nothing real in the way: a supplier that is a script of answers
// and bodies the test feeds, a client that is a writer which keeps what it is given, and a ledger that is a recorder.
// Time is the bubble's, so a three-minute idle timeout is waited for in no time, and no test passes or fails by the
// clock.

// The events of the recorded Messages stream (protocol/testdata/messages/text), in the order the upstream sent them.
const (
	evStart, evBlock, evPing, evHello, evBang, evBlockStop, evMsgDelta, evMsgStop = 0, 1, 2, 3, 4, 5, 6, 7
)

func events(all []string, which ...int) string {
	var b strings.Builder
	for _, i := range which {
		b.WriteString(all[i])
	}
	return b.String()
}

// scripted is how a credential answers one attempt: a response to read or a failure.
type scripted func(ctx context.Context) (*http.Response, *suppliers.Failure)

// failing builds a supplier's failure of the scope, which routing and suppliers number alike (routing.Scope says so).
// The scope's type is the adapter's, which a test outside suppliers cannot name, so it is set by its kind.
func failing(scope routing.Scope, notExecuted bool, cause suppliers.Failure) *suppliers.Failure {
	f := cause
	reflect.ValueOf(&f).Elem().FieldByName("Scope").SetUint(uint64(scope))
	if notExecuted {
		f.Executed = suppliers.NotExecuted
	}
	return &f
}

func failure(scope routing.Scope, notExecuted bool) scripted {
	f := failing(scope, notExecuted, suppliers.Failure{})
	return func(context.Context) (*http.Response, *suppliers.Failure) { return nil, f }
}

// throttled is a 429 the supplier would have the endpoint weighed down for.
func throttled() scripted {
	f := failing(routing.ScopeEndpoint, true, suppliers.Failure{Cause: suppliers.RateLimited{}})
	return func(context.Context) (*http.Response, *suppliers.Failure) { return nil, f }
}

// beat is a step of what the supplier sends: wait, then data; or hang up.
type beat struct {
	wait   time.Duration
	data   string
	hangup bool // the connection breaks: an unexpected end of the stream
}

// serve answers with a stream of the beats, then ends it. The body ends with the attempt's context, as a response
// body does when its request is cancelled. The goroutine that feeds it ends with the stream.
func serve(beats ...beat) scripted { return serveThen(true, beats...) }

// hold is serve, but the stream stays open after the beats, until the attempt is cancelled.
func hold(beats ...beat) scripted { return serveThen(false, beats...) }

func serveThen(end bool, beats ...beat) scripted {
	return func(ctx context.Context) (*http.Response, *suppliers.Failure) {
		pr, pw := io.Pipe()
		context.AfterFunc(ctx, func() { pw.CloseWithError(context.Cause(ctx)) })
		go feed(ctx, pw, beats, end)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}, nil
	}
}

// feed writes the beats to the stream, and ends it if the beats are all there is; it stops when the attempt does.
func feed(ctx context.Context, pw *io.PipeWriter, beats []beat, end bool) {
	for _, b := range beats {
		if !pause(ctx, b.wait) {
			return
		}
		if b.hangup {
			pw.CloseWithError(io.ErrUnexpectedEOF)
			return
		}
		if _, err := pw.Write([]byte(b.data)); err != nil {
			return
		}
	}
	if end {
		_ = pw.Close() //nolint:errcheck // a pipe's close returns nil
	}
}

// late answers after the wait: the response headers take that long. An attempt cancelled meanwhile gets the failure of a
// request that was cancelled.
func late(waits time.Duration, then scripted) scripted {
	return func(ctx context.Context) (*http.Response, *suppliers.Failure) {
		select {
		case <-time.After(waits):
			return then(ctx)
		case <-ctx.Done():
			return nil, failing(routing.ScopeEndpoint, false, suppliers.Failure{})
		}
	}
}

// fakeUpstreams are the endpoints a test gives credentials to, each with a script of answers, one per attempt (the
// last repeats).
type fakeUpstreams struct {
	mu     sync.Mutex
	eps    map[uuid.UUID]*fakeEndpoint
	client *clientWriter
	// beforeAcquire acts when an attempt asks for a credential of the endpoint, before it gets one: a test's way to
	// have the client leave between two attempts.
	beforeAcquire func(id suppliers.EndpointID)
}

type fakeEndpoint struct {
	script  []scripted
	policy  suppliers.CallPolicy
	noSlot  bool
	resting bool
	// receipt is what the attempts of this endpoint leave, as a real one from suppliers does; the zero value is a
	// receipt with nothing known, which prices every attempt at nothing. receipts, when set, is one per attempt, the
	// last repeating: a supplier's receipts differ in its own id of each attempt.
	receipt  suppliers.AttemptReceipt
	receipts []suppliers.AttemptReceipt

	acquired, sends, done int
	calls                 []suppliers.Call
}

// receiptOf is the receipt the nth attempt on the endpoint leaves.
func (e *fakeEndpoint) receiptOf(n int) suppliers.AttemptReceipt {
	if len(e.receipts) == 0 {
		return e.receipt
	}
	return e.receipts[min(n, len(e.receipts)-1)]
}

func (u *fakeUpstreams) endpoint(n int) *fakeEndpoint {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.eps[uuid.UUID(ep(n))]
}

func (u *fakeUpstreams) Acquire(id suppliers.EndpointID) (Conn, error) {
	if u.beforeAcquire != nil {
		u.beforeAcquire(id)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	e, ok := u.eps[id]
	switch {
	case !ok:
		return nil, suppliers.ErrNoEndpoint
	case e.resting:
		return nil, suppliers.ErrSupplierResting
	case e.noSlot:
		return nil, suppliers.ErrNoSlot
	}
	e.acquired++
	return &fakeConn{u: u, e: e, nth: e.acquired - 1}, nil
}

// outstanding is how many credentials are held.
func (u *fakeUpstreams) outstanding() (n int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, e := range u.eps {
		n += e.acquired - e.done
	}
	return n
}

type fakeConn struct {
	u    *fakeUpstreams
	e    *fakeEndpoint
	nth  int // which attempt of the endpoint this is, from 0
	once sync.Once
}

func (c *fakeConn) Send(ctx context.Context, call suppliers.Call) (*http.Response, *suppliers.Failure) {
	c.u.mu.Lock()
	next := c.e.script[min(c.e.sends, len(c.e.script)-1)]
	c.e.sends++
	call.Body = bytes.Clone(call.Body)
	c.e.calls = append(c.e.calls, call)
	c.u.mu.Unlock()
	return next(ctx)
}
func (c *fakeConn) Policy() suppliers.CallPolicy { return c.e.policy }
func (c *fakeConn) Done() suppliers.AttemptReceipt {
	c.once.Do(func() {
		c.u.mu.Lock()
		c.e.done++
		c.u.mu.Unlock()
	})
	return c.e.receiptOf(c.nth)
}

// Sanitize makes the test supplier's identity observable if the gateway skips sanitization.
func (*fakeConn) Sanitize(r protocol.Rejection) *protocol.Rejection {
	if r.Message == "" {
		return nil
	}
	r.Message = strings.ReplaceAll(r.Message, "upstream-vendor", "[hidden]")
	return &r
}

// clientWriter is the client of a call: it keeps what it is given with the time each write was made, and can be made
// to stop reading, as a client does that has gone, or to take its time.
type clientWriter struct {
	mu       sync.Mutex
	header   http.Header
	status   int
	writes   []write
	deadline time.Time // the last write deadline set
	failFrom int       // writes from this one on fail; 0 never
	stall    bool      // a write waits for its deadline and then fails, as one to a client that does not read
	rate     int64     // bytes a second the client reads at; 0 as fast as it is given
	onWrite  func()    // called at the start of every write, as a client that reads when it likes
	deadlns  int
}

type write struct {
	at, deadline time.Time
	data         string
}

func newClientWriter() *clientWriter { return &clientWriter{header: http.Header{}} }

func (w *clientWriter) Header() http.Header { return w.header }

func (w *clientWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}

func (w *clientWriter) Write(b []byte) (int, error) {
	if w.onWrite != nil {
		w.onWrite()
	}
	w.mu.Lock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	deadline, stall, rate := w.deadline, w.stall, w.rate
	fail := w.failFrom > 0 && len(w.writes)+1 >= w.failFrom
	w.mu.Unlock()
	if stall {
		time.Sleep(time.Until(deadline))
		return 0, os.ErrDeadlineExceeded
	}
	if fail {
		return 0, io.ErrClosedPipe
	}
	if rate > 0 { // a slow client: the write takes what the bytes take, and its deadline holds meanwhile
		time.Sleep(time.Duration(int64(len(b)) * int64(time.Second) / rate))
		if !time.Now().Before(deadline) {
			return 0, os.ErrDeadlineExceeded
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, write{time.Now(), w.deadline, string(b)})
	return len(b), nil
}

func (*clientWriter) Flush() {}

func (w *clientWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = t
	w.deadlns++
	return nil
}

func (w *clientWriter) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var b strings.Builder
	for _, x := range w.writes {
		b.WriteString(x.data)
	}
	return b.String()
}

func (w *clientWriter) wrote() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.writes)
}

// settlement is one call of Settle, with what the client had been given by then and what it returned.
type settlement struct {
	outcome  billing.Outcome
	body     string
	held     int // credentials still held
	callerOK bool
	err      error
	at       time.Time
}

type options struct {
	client       protocol.Format  // the client's format; Messages when zero
	upstream     *protocol.Format // the endpoint's format; the client's when nil, which makes it a converted call
	entries      int              // endpoints in the route; 1 when zero
	maxAttempts  int              // 3 when zero
	config       func(*Config)
	routing      routing.Config
	script       map[int][]scripted // by endpoint number, from 1
	policy       suppliers.CallPolicy
	startBy      time.Duration // after the start of the call; two minutes when zero
	final        billing.Final
	settleErr    error               // every settlement fails with it
	settle       func(try int) error // the error of each settlement, from 1, when settleErr is nil
	extend       func(ctx context.Context, r billing.ExtendRequest) error
	segmentable  bool             // the entries are reserved in segments
	segments     billing.Segments // how they are segmented; 200 output tokens with a second's allowance when zero
	cost         int64            // of every attempt
	costNotKnown bool             // whether Costs says the cost leaves something out
	costErr      error            // the error Costs fails with
	costs        Costs            // replaces the fake when set
	request      []byte           // the client's body, instead of its format's sample
	stream       *bool            // the request's stream member; true when nil
	supplier     func(n int) int
	alive        func() bool
	mutate       func(*Call)
	noSlot       []int
	memory       int64                     // the node's budget; memorySize when zero
	price        string                    // the entries' price schedule; cheap when empty
	search       websearch.Searcher        // the web plugin's backend; nil means the feature is off
	server       *protocol.ServerWebSearch // openrouter:web_search's tool, run through search; nil means none
	fileParses   int64                     // the files the gateway parsed before admission
	filePages    int64                     // the pages a paid OCR engine produced, the new file_parse unit
	fileBilling  bool                      // whether the catalog switch bills file_parse instead of web_search
	logs         *bytes.Buffer             // the node's log, discarded when nil
	health       *quality.Monitor          // what the attempts tell; a new one when nil
}

type harness struct {
	t       *testing.T
	exec    *Executor
	up      *fakeUpstreams
	router  *routing.Router
	memory  *protocol.Memory
	budget  int64
	w       *clientWriter
	call    *Call
	req     *http.Request
	client  context.CancelFunc
	events  []string
	cfg     Config
	refused func() map[string]int64 // the 429s counted so far, by reason
	health  *quality.Monitor        // what the attempts told quality
	started time.Time

	mu          sync.Mutex
	settlements []settlement
	extensions  []billing.ExtendRequest
	released    chan struct{} // closed by the call's Done
	drained     bool          // the call starts after the node drained, so it writes no settlement
}

const memorySize = 1 << 30

// newHarness builds a call with an executor; it must be called inside the bubble. The route has endpoints 1..entries,
// each of the upstream's format, which answer by script (a refusal at the start of the attempt when none). The client's
// request comes from its format's sample, the stream from the upstream's, which is the upstream's own for a same-format
// call and another's for a converted one.
func newHarness(t *testing.T, ctx context.Context, o options) *harness {
	t.Helper()
	client := cmpOr(o.client, protocol.Messages)
	upstream := client
	if o.upstream != nil {
		upstream = *o.upstream
	}
	var all []string
	var request []byte
	if o.request != nil {
		request = o.request
	} else {
		all, _ = sample(t, upstream)
		_, request = sample(t, client)
	}
	h := &harness{t: t, w: newClientWriter(), events: all, started: time.Now(), released: make(chan struct{}), health: o.health}
	if h.health == nil {
		h.health = quality.New(nil)
	}
	h.up = &fakeUpstreams{eps: map[uuid.UUID]*fakeEndpoint{}, client: h.w}
	h.cfg = DefaultConfig()
	// One source, as cmd/axiom wires it: the quote and the executor reserve by the same segment settings, so a test
	// that changes how a call is segmented cannot leave one of them on the defaults.
	segments := cmpOr(o.segments, billing.Segments{Output: 200, Overshoot: time.Second})
	h.cfg.SegmentOutput, h.cfg.SegmentOvershoot = segments.Output, segments.Overshoot
	if o.config != nil {
		o.config(&h.cfg)
	}
	var err error
	if h.router, err = routing.New(o.routing); err != nil {
		t.Fatal(err)
	}
	meter, refused := countingMeter(ctx, t)
	h.refused = refused
	limits, err := New(h.cfg, meter.Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	h.budget = cmpOr(o.memory, memorySize)
	h.memory = protocol.NewMemory(h.budget)
	alive := o.alive
	if alive == nil {
		alive = func() bool { return true }
	}
	logger := slog.New(slog.DiscardHandler)
	if o.logs != nil {
		logger = slog.New(slog.NewTextHandler(o.logs, nil))
	}
	var costs Costs = fakeCosts{cost: o.cost, known: !o.costNotKnown, err: o.costErr}
	if o.costs != nil {
		costs = o.costs
	}
	h.exec, err = NewExecutor(ExecDeps{
		Router: h.router, Upstreams: h.up, Limits: limits, Memory: h.memory, Logger: logger,
		Ledger: &fakeLedger{h: h, o: o}, Costs: costs,
		Alive: alive, IdentitySecret: []byte("0123456789abcdef"), Quality: h.health, Outstanding: NewOutstanding(),
		Search: singleSearch{o.search},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.call = h.newCall(o, request, client, upstream)
	clientCtx, cancel := context.WithCancel(ctx)
	h.client = cancel
	h.req = httptest.NewRequestWithContext(clientCtx, http.MethodPost, "/v1/messages", nil)
	return h
}

//nolint:cyclop,gocognit // the harness fills every request field the scenario may need
func (h *harness) newCall(o options, request []byte, client, upstream protocol.Format) *Call {
	h.t.Helper()
	n := max(o.entries, 1)
	schedule, err := catalog.ParseSchedule([]byte(cmpOr(o.price, cheap)))
	if err != nil {
		h.t.Fatal(err)
	}
	req, err := protocol.Inspect(client, request)
	if err != nil {
		h.t.Fatal(err)
	}
	if o.server != nil {
		// Admission strips the tool before routing and decides the loop; the harness builds its plan directly, so it
		// does the same here, with the one backend it was given.
		request, err = protocol.RemoveServerTool(client, request)
		if err != nil {
			h.t.Fatal(err)
		}
		req, err = protocol.Inspect(client, request)
		if err != nil {
			h.t.Fatal(err)
		}
		req.ServerSearch, req.Needs = o.server, req.Needs|protocol.Tools|protocol.WebSearch
		o.server.Loop = true
		cap := int(o.server.MaxUses)
		if cap == 0 {
			cap = protocol.DefaultServerToolCalls
		}
		if req.HasMaxToolCalls {
			cap = min(cap, int(req.MaxToolCalls))
		}
		req.WebSearches = int64(cap)
	}
	req.ServiceTiers = 1 << catalog.ServiceTierStandard // the schedule prices that tier only
	req.FileParses = o.fileParses                       // the parse step ran before admission, so the quote sees it
	req.FilePages = o.filePages
	req.FileBilling = o.fileBilling
	request = setStream(&req, request, o.stream)
	var quotes []billing.Quotable
	plan := routing.Plan{MaxAttempts: cmpOr(o.maxAttempts, 3), StartBy: h.started.Add(cmpOr(o.startBy, 2*time.Minute))}
	for i := 1; i <= n; i++ {
		limits := billing.Model{ContextLength: 100000, OutputSpeed: 100}
		quotes = append(quotes, billing.Quotable{Price: schedule, Model: limits, MaxOutput: 1000, Segmentable: o.segmentable})
		supplier := i
		if o.supplier != nil {
			supplier = o.supplier(i)
		}
		plan.Entries = append(plan.Entries, routing.Entry{
			Endpoint: ep(i), Supplier: sup(supplier), Target: protocol.Target{
				Format: upstream, Model: "up-" + string(rune('0'+i)), Streams: true, MaxOutput: 1000,
			},
			MaxOutput: 1000, Limits: limits, Segmentable: o.segmentable, Price: routing.PriceRef{Version: uuid.UUID{1}, Schedule: schedule},
		})
		plan.Record.Route = append(plan.Record.Route, ep(i))
		script := o.script[i]
		if script == nil {
			script = []scripted{failure(routing.ScopeEndpoint, false)}
		}
		h.up.eps[uuid.UUID(ep(i))] = &fakeEndpoint{script: script, policy: o.policy, noSlot: slices.Contains(o.noSlot, i)}
	}
	if req.Web != nil {
		req.SearchBytes = protocol.WebSearchMaxBytes
	}
	// The same segment settings the harness gave the executor (newHarness), from the one place they are chosen.
	segments := cmpOr(o.segments, billing.Segments{Output: 200, Overshoot: time.Second})
	r, err := billing.Quote(routing.DemandOf(&req), quotes, segments, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	c := &Call{
		ID: uuid.NewV7(), Format: client, Request: req, Body: request,
		Header: http.Header{"Anthropic-Beta": {"beta-1"}}, Hold: &billing.Hold{Reservation: r}, Plan: plan,
	}
	if o.server != nil {
		c.serverLoop = true
		if backend, ok := (singleSearch{o.search}).Default(); ok {
			c.searchBackend = backend
			if c.searchPrice, err = backend.Price(o.server.MaxResults); err != nil {
				h.t.Fatal(err)
			}
		}
	}
	if req.Web != nil {
		if c.search, err = protocol.ChatSearchQuery(request); err != nil {
			h.t.Fatal(err)
		}
		// Admission resolves the engine in production; the harness builds its plan directly, so it resolves the one
		// backend it was given here.
		if backend, ok := (singleSearch{o.search}).Default(); ok {
			c.searchBackend = backend
			if c.searchPrice, err = backend.Price(req.Web.MaxResults); err != nil {
				h.t.Fatal(err)
			}
		}
	}
	c.hold(func() { close(h.released) })
	if o.mutate != nil {
		o.mutate(c)
	}
	return c
}

// setStream applies the harness's stream option to the client's body too: the endpoint still has to be asked for a
// stream, so the body's own member has to agree with the request's.
func setStream(req *protocol.Request, body []byte, stream *bool) []byte {
	if stream == nil {
		return body
	}
	req.Stream = *stream
	if *stream {
		return body
	}
	return bytes.Replace(body, []byte(`"stream": true`), []byte(`"stream": false`), 1)
}

func cmpOr[T comparable](v, fallback T) T {
	var zero T
	if v == zero {
		return fallback
	}
	return v
}

// fakeLedger records the settlements, with what the client had seen when each was asked for, which is what shows the
// order of the ending: the cost is only written after it.
type fakeLedger struct {
	h *harness
	o options
}

func (l *fakeLedger) ExtendIn(ctx context.Context, _ pg.Tx, _ *billing.Hold, r billing.ExtendRequest) error {
	l.h.mu.Lock()
	l.h.extensions = append(l.h.extensions, r)
	l.h.mu.Unlock()
	if l.o.extend == nil {
		return nil
	}
	return l.o.extend(ctx, r)
}

func (l *fakeLedger) SettleIn(ctx context.Context, _ pg.Tx, _ *billing.Hold, o billing.Outcome) (billing.Final, func(), error) {
	h := l.h
	h.mu.Lock()
	defer h.mu.Unlock()
	err := l.o.settleErr
	if err == nil && l.o.settle != nil {
		err = l.o.settle(len(h.settlements) + 1)
	}
	h.settlements = append(h.settlements, settlement{
		outcome: o, body: h.w.body(), held: h.up.outstanding(), callerOK: ctx.Err() == nil, err: err, at: time.Now(),
	})
	if err != nil {
		return billing.Final{}, nil, err
	}
	return l.o.final, nil, nil
}

// RunBatch is the fake's one transaction: the works never touch the database, so a zero transaction is enough. A
// work's error carries its index as the real batch's does, so the caller can retry the other works.
func (*fakeLedger) RunBatch(_ context.Context, _ pg.Scope, works []func(pg.Tx) error) error {
	for i, work := range works {
		if err := work(pg.Tx{}); err != nil {
			return &billing.BatchWorkError{Index: i, Err: err}
		}
	}
	return nil
}

// fakeCosts is what Costs answers the tests: a cost, whether that is all of it, and an error to fail with. The native
// amount, which the settlement writes to suppliers, is the same figure in the supplier's own unit, so a test can see it
// where the attempt rows go. On an error it returns the figure with a zero AttemptCost, a state the real Credits never
// reaches (it returns 0 with its error); what that pins is that the executor books the amount that comes back.
type fakeCosts struct {
	cost  int64
	known bool
	err   error
}

func (c fakeCosts) Credits(suppliers.AttemptReceipt, []catalog.Usage, platform.Currency) (suppliers.AttemptCost, int64, bool, error) {
	if c.err != nil {
		return suppliers.AttemptCost{}, c.cost, c.known, c.err // the figure still comes: the executor books it whatever the error
	}
	return suppliers.AttemptCost{Native: platform.AmountExact(c.cost), Flag: suppliers.CostComplete}, c.cost, c.known, nil
}

// run runs the call to its end, in the bubble, and checks what holds however it ended: settled once, nothing held, no
// memory left charged.
func (h *harness) run() {
	h.t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.exec.Run(h.w, h.req, h.call)
	}()
	<-done
	h.check()
}

// start runs the call and returns a channel that closes when it has ended; the test acts in the meantime.
func (h *harness) start() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.exec.Run(h.w, h.req, h.call)
	}()
	return done
}

// check waits for the call to give its slots back, which may be after Run returns, and checks what holds however it
// ended: every try of the settlement had the same outcome, at most one was written (none after a drain), nothing is
// held, and the node's memory is back to the byte, neither left charged nor given back twice.
func (h *harness) check() {
	h.t.Helper()
	<-h.released
	h.mu.Lock()
	all := slices.Clone(h.settlements)
	h.mu.Unlock()
	written := 0
	for _, s := range all {
		if s.err == nil {
			written++
		}
		if !reflect.DeepEqual(s.outcome, all[0].outcome) {
			h.t.Errorf("a retry settled %+v, the first try %+v", s.outcome, all[0].outcome)
		}
	}
	if (len(all) == 0) != h.drained || written > 1 {
		h.t.Errorf("%d tries of the settlement, %d written", len(all), written)
	}
	if held := h.up.outstanding(); held != 0 {
		h.t.Errorf("%d credentials still held", held)
	}
	if got := left(h.memory); got != h.budget {
		h.t.Errorf("the node's memory budget has %d left of %d: not given back whole, or given back twice", got, h.budget)
	}
}

// settleAs makes the next settlement return f: for a test that learns what to settle only after the call is built.
func (h *harness) settleAs(f billing.Final) {
	h.t.Helper()
	h.exec.Ledger.(*fakeLedger).o.final = f
}

// ended is the last try of the settlement, or fails.
func (h *harness) ended() settlement {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.settlements) == 0 {
		h.t.Fatal("no settlement")
	}
	return h.settlements[len(h.settlements)-1]
}

func ep(n int) routing.EndpointID  { return routing.EndpointID(uuid.UUID{15: byte(n)}) }        //nolint:gosec // a test number
func sup(n int) routing.SupplierID { return routing.SupplierID(uuid.UUID{14: 1, 15: byte(n)}) } //nolint:gosec // a test number
