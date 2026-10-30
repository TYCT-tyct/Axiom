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

// TestNativeClientsUseNativeEndpoints: a Messages request is converted for a Responses endpoint, and the other way
// round, with the endpoint called in its own format and the client answered in its own, streamed and aggregated
// (protocol README.md「跨格式转换」「跨格式的流」).
func TestNativeClientsUseNativeEndpoints(t *testing.T) {
	for _, pair := range [][2]protocol.Format{{protocol.Messages, protocol.Responses}, {protocol.Responses, protocol.Messages}} {
		for _, stream := range []bool{true, false} {
			client, upstream := pair[0], pair[1]
			name := formatName(client) + " to " + formatName(upstream)
			if stream {
				name += " streamed"
			} else {
				name += " aggregated"
			}
			t.Run(name, func(t *testing.T) {
				run(t, func(t *testing.T, ctx context.Context) {
					h := newHarness(t, ctx, options{
						client: client, upstream: &upstream, stream: &stream,
						final: billing.Final{Charged: platform.AmountExact(3703701)},
					})
					h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
					h.run()
					checkNativeEndpointRequest(t, h, upstream, stream)
					checkNativeToNativeReply(t, h, textOf(upstream))
				})
			})
		}
	}
}

// checkNativeEndpointRequest holds the endpoint's request to its own format: the model it is called as, a stream when
// asked for, and the members only that format has.
func checkNativeEndpointRequest(t *testing.T, h *harness, upstream protocol.Format, stream bool) {
	t.Helper()
	if h.w.status != http.StatusOK {
		t.Fatalf("status %d: %s", h.w.status, h.w.body())
	}
	call := h.up.endpoint(1).calls[0]
	if call.Format != upstream {
		t.Fatalf("the endpoint was called in format %v, want %v", call.Format, upstream)
	}
	wants := []string{`"model":"up-1"`}
	switch upstream {
	case protocol.Responses:
		wants = append(wants, `"input":[`, `"store":false`, `"max_output_tokens":256`, `"input_text","text":"Hello"`)
		// A Messages request has no server-side storage to ask for, and no instructions to keep.
		if strings.Contains(string(call.Body), `"include"`) {
			t.Errorf("a Messages client's request asked for encrypted reasoning: %s", call.Body)
		}
	case protocol.Messages:
		wants = append(wants, `"system":[`, `"messages":[`, `"max_tokens":1000`, `"text":"You are a helpful assistant."`)
	case protocol.Chat:
		t.Fatalf("Chat is not a native format")
	}
	if stream {
		wants = append(wants, `"stream":true`)
	}
	checkContains(t, "request sent", string(call.Body), wants...)
}

// checkNativeToNativeReply holds the client's own format: its ID, its model, the text the upstream generated, and the
// cost, written no earlier than the settlement.
func checkNativeToNativeReply(t *testing.T, h *harness, text string) {
	t.Helper()
	checkContains(t, "client reply", h.w.body(), h.call.ID.String(), text, `"cost":0.003703701,"cost_currency":"USD"`)
	before := h.ended().body
	for _, early := range []string{"message_stop", "response.completed", `"cost"`} {
		if strings.Contains(before, early) {
			t.Fatalf("%s was written before the settlement: %s", early, before)
		}
	}
}

// formatName is a format as a test names it.
func formatName(f protocol.Format) string {
	switch f {
	case protocol.Messages:
		return "Messages"
	case protocol.Responses:
		return "Responses"
	case protocol.Chat:
		return "Chat"
	}
	return "Chat"
}

// textOf is the text the recorded stream of an upstream format generates.
func textOf(upstream protocol.Format) string {
	if upstream == protocol.Responses {
		return "there! How can I assist you today?" // the recorded stream's second text delta
	}
	return "Hello"
}
