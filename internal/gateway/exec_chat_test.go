package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/protocol"
)

// TestAChatCallToAChatEndpointIsForwardedAsChat: the endpoint is called in Chat with the request rewritten — its model
// name, the cap the call was reserved for, a stream that reports usage — and the client gets the stream with the ID of
// the call and the model it asked for, settled before its ending (docs/domains/gateway.md「转发规则」). A client that
// asked for no stream gets the aggregated reply from the same stream.
func TestAChatCallToAChatEndpointIsForwardedAsChat(t *testing.T) {
	no := false
	checkChatCall(t, "streamed", options{client: protocol.Chat})
	checkChatCall(t, "aggregated", options{client: protocol.Chat, stream: &no})
}

func checkChatCall(t *testing.T, name string, o options) {
	t.Helper()
	run(t, func(t *testing.T, ctx context.Context) {
		o.final = billing.Final{Charged: platform.AmountExact(3703701)}
		h := newHarness(t, ctx, o)
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		checkContains(t, name+": reply", h.w.body(), h.call.ID.String(), `"model":"`+h.call.Request.Model+`"`, `Hello`, `"cost":0.003703701,"cost_currency":"USD"`)
		if h.w.status != http.StatusOK {
			t.Errorf("%s: status %d", name, h.w.status)
		}
		call := h.up.endpoint(1).calls[0]
		if call.Format != protocol.Chat {
			t.Errorf("%s: the endpoint was called in format %v", name, call.Format)
		}
		checkContains(t, name+": request sent", string(call.Body),
			`"model": "up-1"`, `"stream": true`, `,"max_tokens":1000,"stream_options":{"include_usage":true`)
		if a := h.ended().outcome.Attempts; len(a) != 1 || a[0].Finish != billing.FinishStop {
			t.Errorf("%s: ending %+v", name, h.ended().outcome)
		}
	})
}

// checkContains: text has each of wants.
func checkContains(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("%s lacks %s:\n%s", what, want, text)
		}
	}
}
