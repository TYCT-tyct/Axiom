package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// left is what the budget has left, found by asking: the budget only answers a charge.
func left(m *protocol.Memory) int64 {
	lo, hi := int64(0), int64(1)<<40
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if m.Charge(mid) {
			m.Refund(mid)
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// chatForResponses makes the call a Chat request for an endpoint of the Responses format, and a body that no Responses
// endpoint can express: stop has no counterpart there. Prepare refuses it, but only after it has read the body.
func chatForResponses(t *testing.T, c *Call) {
	t.Helper()
	c.Body = []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stop":["x"],"stream":true}`)
	var err error
	if c.Request, err = protocol.Inspect(protocol.Chat, c.Body); err != nil {
		t.Fatal(err)
	}
	c.Request.ServiceTiers = 1 << catalog.ServiceTierStandard
	c.Format = protocol.Chat
	c.Plan.Entries[0].Target.Format = protocol.Responses
}

// TestMemoryIsAskedForBeforePrepareAllocates: what Prepare may hold is drawn from the budget before it makes
// the request, so a node whose budget is spent refuses at once, 503, instead of finding that out after the allocation
// it was meant to prevent. The proof is the request Prepare would have refused: with the budget it gets that answer, a
// 400 about the request; without, it is never asked.
func TestMemoryIsAskedForBeforePrepareAllocates(t *testing.T) {
	for _, c := range []struct {
		name   string
		spare  int64 // the budget beyond the cost
		status int
	}{{"the budget covers it", 0, http.StatusBadRequest}, {"a byte short", -1, http.StatusServiceUnavailable}} {
		run(t, func(t *testing.T, ctx context.Context) {
			mutate := func(call *Call) { chatForResponses(t, call) }
			probe := newHarness(t, ctx, options{mutate: mutate}) // only to see what the request costs
			cost := neededBudget(t, &probe.call.Request, probe.call.Body, probe.call.Plan.Entries[0].Target, protocol.Rewrites{})
			h := newHarness(t, ctx, options{mutate: mutate, memory: cost + c.spare})
			h.run()
			if h.w.status != c.status {
				t.Errorf("%s: status %d, want %d\n%s", c.name, h.w.status, c.status, h.w.body())
			}
			if n := len(h.ended().outcome.Attempts); n != 0 || h.up.endpoint(1).sends != 0 {
				t.Errorf("%s: %d attempts, %d sends", c.name, n, h.up.endpoint(1).sends)
			}
		})
	}
}

// TestMemoryHoldsARequestByItsCapacityWhileItIsSent: the node's budget is charged for the buffer the request is in, its
// whole capacity, and no more once the request is sent; the length of what is sent is less.
func TestMemoryHoldsARequestByItsCapacityWhileItIsSent(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		var during int64
		h.up.eps[uuidOf(ep(1))].script = []scripted{func(ctx context.Context) (*http.Response, *suppliers.Failure) {
			during = h.budget - left(h.memory)
			return whole(h)(ctx)
		}}
		h.run()
		sent := h.up.endpoint(1).calls[0].Body
		_, capacity, err := protocol.PrepareBudgeted(nil, &h.call.Request, h.call.Body, h.call.Plan.Entries[0].Target, protocol.Rewrites{})
		if err != nil {
			t.Fatal(err)
		}
		if during != capacity || during <= int64(len(sent)) {
			t.Errorf("%d bytes were held while a request of %d bytes was sent, want its capacity, %d", during, len(sent), capacity)
		}
		if after := left(h.memory); after != h.budget {
			t.Errorf("%d bytes are left after the call, want %d", after, h.budget)
		}
	})
}

// TestMemoryHoldsConcurrentRequestsToTheBudget: with a budget for three requests' buffers, eight at once
// get three and are refused five, whatever the order: the room is drawn before the buffer is made, so the refused make
// none, and the three are held by the capacity they will have.
func TestMemoryHoldsConcurrentRequestsToTheBudget(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		probe := newHarness(t, ctx, options{})
		entry := probe.call.Plan.Entries[0]
		w := protocol.Rewrites{}
		_, buffer, err := protocol.PrepareBudgeted(nil, &probe.call.Request, probe.call.Body, entry.Target, w)
		if err != nil {
			t.Fatal(err)
		}
		cost := neededBudget(t, &probe.call.Request, probe.call.Body, entry.Target, w)
		h := newHarness(t, ctx, options{memory: 3 * cost})
		s := &session{x: h.exec, c: h.call}
		var wg sync.WaitGroup
		var made, refused atomic.Int64
		for range 8 {
			wg.Go(func() {
				switch _, held, err := s.prepare(entry); {
				case err == nil && held == buffer:
					made.Add(1)
				case errors.Is(err, ErrOverloaded):
					refused.Add(1)
				default:
					t.Errorf("held %d, want %d: %v", held, buffer, err)
				}
			})
		}
		wg.Wait()
		if rest := 3 * (cost - buffer); made.Load() != 3 || refused.Load() != 5 || left(h.memory) != rest {
			t.Errorf("%d requests made, %d refused, %d bytes left of the budget, want %d", made.Load(), refused.Load(), left(h.memory), rest)
		}
	})
}

// TestMemoryHoldsEchoPromptSizes: a completion that echoes keeps each prompt's size for every attempt, 8 bytes a prompt,
// which a batch of empty prompts makes larger than its body. While the upstream is asked, the call holds exactly that
// much more than the same call without echo; a node short of it refuses, 503, without asking an upstream; and either
// way the budget is whole again after the call.
func TestMemoryHoldsEchoPromptSizes(t *testing.T) {
	prompts := `"prompt":[""` + strings.Repeat(`,""`, 999) + `]}`
	echoed := []byte(`{"model":"m","echo":true ,"max_tokens":0,` + prompts) // as long as the body without echo
	req, err := protocol.Inspect(protocol.Completions, echoed)
	if err != nil {
		t.Fatal(err)
	}
	size := protocol.EchoSize(&req)
	if size != 8000 || size <= int64(len(echoed)) {
		t.Fatalf("1000 prompts in %d bytes hold %d bytes of sizes", len(echoed), size)
	}
	reply := `data: {"id":"c","object":"text_completion","created":0,"model":"up-1","choices":[{"index":0,"text":"",` +
		`"finish_reason":"length"}]}` + "\n\ndata: [DONE]\n\n"
	call := func(ctx context.Context, body []byte, memory int64) (held int64, h *harness) {
		h = newHarness(t, ctx, options{client: protocol.Completions, request: body, memory: memory})
		h.up.eps[uuidOf(ep(1))].script = []scripted{func(ctx context.Context) (*http.Response, *suppliers.Failure) {
			held = h.budget - left(h.memory)
			return serve(beat{data: reply})(ctx)
		}}
		h.run()
		if after := left(h.memory); after != h.budget {
			t.Errorf("%d bytes are left after the call, want %d", after, h.budget)
		}
		return held, h
	}
	run(t, func(t *testing.T, ctx context.Context) {
		with, h := call(ctx, echoed, memorySize)
		without, _ := call(ctx, []byte(`{"model":"m","echo":false,"max_tokens":0,`+prompts), memorySize)
		if h.w.status != http.StatusOK || with-without != size {
			t.Errorf("status %d; held %d with echo, %d without, want %d more", h.w.status, with, without, size)
		}
		if _, h := call(ctx, echoed, size-1); h.w.status != http.StatusServiceUnavailable || h.up.endpoint(1).sends != 0 {
			t.Errorf("a byte short: status %d, %d sends", h.w.status, h.up.endpoint(1).sends)
		}
	})
}

// neededBudget is the smallest budget PrepareBudgeted finishes with: what preparing the request draws at once.
func neededBudget(t *testing.T, r *protocol.Request, body []byte, target protocol.Target, w protocol.Rewrites) int64 {
	t.Helper()
	lo, hi := int64(0), int64(1<<20)
	for {
		_, _, err := protocol.PrepareBudgeted(protocol.NewMemory(hi), r, body, target, w)
		if err == nil {
			break
		}
		if !errors.Is(err, protocol.ErrMemory) {
			break
		}
		lo, hi = hi+1, hi*2
	}
	for lo < hi {
		mid := lo + (hi-lo)/2
		if _, _, err := protocol.PrepareBudgeted(protocol.NewMemory(mid), r, body, target, w); errors.Is(err, protocol.ErrMemory) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
