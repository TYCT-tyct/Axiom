package gateway

import (
	"context"
	"net/http"
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

// TestTheNodesMemoryIsAskedForBeforePrepareAllocates: what Prepare may hold is drawn from the budget before it makes
// the request, so a node whose budget is spent refuses at once, 503, instead of finding that out after the allocation
// it was meant to prevent. The proof is the request Prepare would have refused: with the budget it gets that answer, a
// 400 about the request; without, it is never asked.
func TestTheNodesMemoryIsAskedForBeforePrepareAllocates(t *testing.T) {
	for _, c := range []struct {
		name   string
		spare  int64 // the budget beyond the cost
		status int
	}{{"the budget covers it", 0, http.StatusBadRequest}, {"a byte short", -1, http.StatusServiceUnavailable}} {
		run(t, func(t *testing.T, ctx context.Context) {
			mutate := func(call *Call) { chatForResponses(t, call) }
			probe := newHarness(t, ctx, options{mutate: mutate}) // only to see what the request costs
			cost := protocol.PrepareCost(&probe.call.Request, len(probe.call.Body), probe.call.Plan.Entries[0].Target, protocol.Rewrites{})
			h := newHarness(t, ctx, options{mutate: mutate, memory: cost + c.spare})
			h.run()
			if h.w.status != c.status {
				t.Errorf("%s: status %d, want %d\n%s", c.name, h.w.status, c.status, h.w.body())
			}
			if n := len(h.ended().ending.Attempts); n != 0 || h.up.endpoint(1).sends != 0 {
				t.Errorf("%s: %d attempts, %d sends", c.name, n, h.up.endpoint(1).sends)
			}
		})
	}
}

// TestTheRequestIsHeldByItsCapacityWhileItIsSent: the node's budget is charged for the buffer the request is in, its
// whole capacity, and no more once the request is sent; the length of what is sent is less.
func TestTheRequestIsHeldByItsCapacityWhileItIsSent(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		var during int64
		h.up.eps[uuidOf(ep(1))].script = []scripted{func(ctx context.Context) (*http.Response, *suppliers.Failure) {
			during = h.budget - left(h.memory)
			return whole(h)(ctx)
		}}
		h.run()
		sent := h.up.endpoint(1).calls[0].Body
		capacity := protocol.PrepareCost(&h.call.Request, len(h.call.Body), h.call.Plan.Entries[0].Target, protocol.Rewrites{})
		if during != capacity || during <= int64(len(sent)) {
			t.Errorf("%d bytes were held while a request of %d bytes was sent, want its capacity, %d", during, len(sent), capacity)
		}
		if after := left(h.memory); after != h.budget {
			t.Errorf("%d bytes are left after the call, want %d", after, h.budget)
		}
	})
}
