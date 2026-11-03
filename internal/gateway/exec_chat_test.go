package gateway

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// TestAChatCallToAChatEndpointIsForwardedAsChat: the endpoint is called in Chat with the request rewritten — its model
// name, the cap the call was reserved for, a stream that reports usage — and the client gets the stream with the ID of
// the call and the model it asked for, settled before its ending (docs/domains/gateway.md「转发规则」). A client that
// asked for no stream gets the aggregated reply from the same stream.
func TestAChatCallToAChatEndpointIsForwardedAsChat(t *testing.T) {
	t.Parallel()
	no := false
	checkChatCall(t, "streamed", options{client: protocol.Chat})
	checkChatCall(t, "aggregated", options{client: protocol.Chat, stream: &no})
}

func TestNativeClientsUseChatEndpoints(t *testing.T) {
	t.Parallel()
	for _, client := range []protocol.Format{protocol.Messages, protocol.Responses} {
		for _, stream := range []bool{true, false} {
			checkNativeClientOnChat(t, client, stream)
		}
	}
}

// checkNativeClientOnChat: a Messages or Responses request is converted for a Chat endpoint, which gets a Chat request
// that streams and reports usage, and the client gets its own format back, held before the settlement.
func checkNativeClientOnChat(t *testing.T, client protocol.Format, stream bool) {
	t.Helper()
	run(t, func(t *testing.T, ctx context.Context) {
		chat := protocol.Chat
		h := newHarness(t, ctx, options{
			client: client, upstream: &chat, stream: &stream,
			final: billing.Final{Charged: platform.AmountExact(3703701)},
		})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		checkChatUpstreamRequest(t, h, client, stream)
		checkNativeClientReply(t, h)
	})
}

func checkChatUpstreamRequest(t *testing.T, h *harness, client protocol.Format, stream bool) {
	t.Helper()
	if h.w.status != http.StatusOK {
		t.Fatalf("client %v streaming %v: %d %s", client, stream, h.w.status, h.w.body())
	}
	call := h.up.endpoint(1).calls[0]
	if call.Format != protocol.Chat {
		t.Fatalf("called format %v", call.Format)
	}
	var body map[string]any
	if err := jsonx.Unmarshal(call.Body, &body); err != nil {
		t.Fatal(err)
	}
	options, _ := body["stream_options"].(map[string]any)
	if body["stream"] != true || options["include_usage"] != true || body["messages"] == nil {
		t.Fatalf("Chat request: %s", call.Body)
	}
}

// checkNativeClientReply holds the client's own format, with the cost, and no success ending before the settlement.
func checkNativeClientReply(t *testing.T, h *harness) {
	t.Helper()
	checkContains(t, "client reply", h.w.body(), h.call.ID.String(), "Hello", `"cost":0.003703701`)
	before := h.ended().body
	for _, early := range []string{"message_stop", "response.completed", `"cost"`} {
		if strings.Contains(before, early) {
			t.Fatalf("%s was written before the settlement: %s", early, before)
		}
	}
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

// TestAChatCapTheEndpointIgnoresIsNotTheBound: a client that caps its output in max_completion_tokens, sent to an
// endpoint that reads only max_tokens (DeepSeek, Qwen), has the cap written in max_tokens, the member the endpoint
// honors, so that the output is bounded by what the call was reserved for and not by the endpoint's own default.
func TestAChatCapTheEndpointIgnoresIsNotTheBound(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{client: protocol.Chat, mutate: func(c *Call) {
			c.Body = bytes.Replace(c.Body, []byte(`"stream": true`), []byte(`"stream": true, "max_completion_tokens": 1`), 1)
			tiers := c.Request.ServiceTiers
			var err error
			if c.Request, err = protocol.Inspect(protocol.Chat, c.Body); err != nil {
				t.Fatal(err)
			}
			c.Request.ServiceTiers = tiers
		}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		sent := string(h.up.endpoint(1).calls[0].Body)
		if !strings.Contains(sent, `"max_tokens":1`) || strings.Contains(sent, "max_completion_tokens") {
			t.Errorf("the request sent:\n%s", sent)
		}
	})
}
