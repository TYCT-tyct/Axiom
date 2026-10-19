package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// panicBody is a response body that panics once what it was given is read.
type panicBody struct {
	r       io.Reader
	onPanic func()
}

func (b *panicBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		b.onPanic()
		panic("a bug")
	}
	return n, err
}

func (*panicBody) Close() error { return nil }

// bigStream is the beats of an answer whose text is long enough to draw on the node's budget.
func bigStream(h *harness, end ...beat) []beat {
	return append([]beat{h.feed(evStart, evBlock), {data: text(200 << 10)}}, end...)
}

// panicCase is a call that panics once it holds part of the node's budget: in the read of its upstream's body or in
// its settlement, which is before the answer registers anything. script is the upstream's answer; panics is what the
// panicking read calls first.
type panicCase struct {
	name   string
	o      options
	inRead bool // the panic is in the read, not in the settlement
	script func(h *harness, panics func()) scripted
}

func panicCases() []panicCase {
	no := false
	messages := protocol.Messages
	end := func(h *harness) beat { return h.feed(evBlockStop, evMsgDelta, evMsgStop) }
	served := func(end func(h *harness) []beat) func(h *harness, _ func()) scripted {
		return func(h *harness, _ func()) scripted { return serve(bigStream(h, end(h)...)...) }
	}
	finished := served(func(h *harness) []beat { return []beat{end(h)} })
	hangup := served(func(*harness) []beat { return []beat{{hangup: true}} })
	read := func(h *harness, panics func()) scripted {
		return func(context.Context) (*http.Response, *suppliers.Failure) {
			var b strings.Builder
			for _, beat := range bigStream(h) {
				b.WriteString(beat.data)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: &panicBody{strings.NewReader(b.String()), panics}}, nil
		}
	}
	return []panicCase{
		{name: "stream settles", script: finished},
		{name: "aggregate settles", o: options{stream: &no}, script: finished},
		// The only attempt hangs up with content: it is kept, and left hands it over to answer the call.
		{name: "aggregate left by the last attempt settles", o: options{client: protocol.Chat, upstream: &messages, stream: &no}, script: hangup},
		{name: "stream is read", inRead: true, script: read},
		{name: "aggregate is read", o: options{stream: &no}, inRead: true, script: read},
	}
}

// TestAPanicGivesBackTheNodesBudgetWhole: every relay a call took is released exactly once on every way out, a panic
// included (README.md「Relay 的所有权」). Each call panics only once it holds part of the node's budget, so the check at
// the end is not about a call that never took any; it is exact, so a second release shows too.
func TestAPanicGivesBackTheNodesBudgetWhole(t *testing.T) {
	for _, c := range panicCases() {
		t.Run(c.name, func(t *testing.T) {
			run(t, func(t *testing.T, ctx context.Context) {
				var h *harness
				drawn := func() {
					if left(h.memory) >= h.budget {
						t.Error("the call holds none of the node's budget where it panics")
					}
				}
				o := c.o
				if !c.inRead {
					o.settle = func(int) error { drawn(); panic("a bug") }
				}
				h = newHarness(t, ctx, o)
				h.up.eps[uuidOf(ep(1))].script = []scripted{c.script(h, drawn)}
				checkPanicGivesBackBudget(t, h)
			})
		})
	}
}

// TestAPanicInTheSettlementOfACompactReplyGivesBackItsMemory is the same for the reply the compact endpoint answers whole.
func TestAPanicInTheSettlementOfACompactReplyGivesBackItsMemory(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		var h *harness
		h = newHarness(t, ctx, options{mutate: func(c *Call) { compact(t, c) }, settle: func(int) error {
			if left(h.memory) >= h.budget {
				t.Error("the call holds none of the node's budget where it panics")
			}
			panic("a bug")
		}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{wholeAnswer(bigCompacted(200 << 10))}
		checkPanicGivesBackBudget(t, h)
	})
}

// TestAPanicInTheReadOfACompactReplyGivesBackItsMemory: the reply is charged as it is read, so a panic in the read has
// already drawn on the budget.
func TestAPanicInTheReadOfACompactReplyGivesBackItsMemory(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		var h *harness
		h = newHarness(t, ctx, options{mutate: func(c *Call) { compact(t, c) }})
		h.up.eps[uuidOf(ep(1))].script = []scripted{func(context.Context) (*http.Response, *suppliers.Failure) {
			body := &panicBody{r: strings.NewReader(bigCompacted(200 << 10)), onPanic: func() {
				if left(h.memory) >= h.budget {
					t.Error("the call holds none of the node's budget where it panics")
				}
			}}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}, nil
		}}
		checkPanicGivesBackBudget(t, h)
	})
}

// TestADiscardedRelayIsNotHeldBySession: once a relay is let go, the session does not keep it reachable: its buffers
// would stay in memory, uncounted, while the budget they were charged against is spent again, as it is while the next
// attempt waits for its response headers. A relay of the next attempt is not touched by the letting go of an old one.
func TestADiscardedRelayIsNotHeldBySession(t *testing.T) {
	cases := []struct {
		name string
		do   func(s *session, old, next *protocol.Relay)
		live func(old, next *protocol.Relay) *protocol.Relay // what the session holds afterwards
	}{
		{"the kept reply is let go", func(s *session, old, _ *protocol.Relay) { s.keep(old); s.let() },
			func(_, _ *protocol.Relay) *protocol.Relay { return nil }},
		{"a relay is dropped", func(s *session, old, _ *protocol.Relay) { s.drop(old) },
			func(_, _ *protocol.Relay) *protocol.Relay { return nil }},
		{"the next attempt's relay stays", func(s *session, old, next *protocol.Relay) { s.keep(old); s.live = next; s.let() },
			func(_, next *protocol.Relay) *protocol.Relay { return next }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run(t, func(t *testing.T, ctx context.Context) {
				h := newHarness(t, ctx, options{})
				s := newSession(h.exec, h.w, h.req, h.call)
				newRelay := func() *protocol.Relay {
					return protocol.NewRelay(protocol.Messages, protocol.Messages, strings.NewReader(""), protocol.UsageContext{},
						protocol.RelayOptions{ID: "x", Model: "m", Memory: h.memory, Aggregate: true})
				}
				old, next := newRelay(), newRelay()
				s.live = old
				c.do(s, old, next)
				if want := c.live(old, next); s.live != want {
					t.Errorf("the session holds %p, want %p", s.live, want)
				}
			})
		})
	}
}

// checkPanicGivesBackBudget runs the call, which panics, and checks the panic goes on and the node's budget is whole.
func checkPanicGivesBackBudget(t *testing.T, h *harness) {
	t.Helper()
	panicked := make(chan any)
	go func() {
		defer func() { panicked <- recover() }()
		h.exec.Run(h.w, h.req, h.call)
	}()
	if p := <-panicked; p != "a bug" {
		t.Fatalf("recovered %v", p)
	}
	<-h.released
	if got := left(h.memory); got != h.budget {
		t.Errorf("the node's budget has %d left of %d", got, h.budget)
	}
}
