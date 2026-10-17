package protocol_test

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// TestPrepareAsksForAStream: an endpoint that streams is called with a stream whatever the client asked, since the
// gateway reads every answer as one and aggregates it for a client that wants no stream
// (docs/domains/gateway.md「转发规则」): a request that names no stream gets the member, and false or null is turned
// true. An endpoint that does not stream keeps what the client asked.
func TestPrepareAsksForAStream(t *testing.T) {
	w := protocol.Rewrites{Identity: derive}
	prepared := func(t *testing.T, from, to protocol.Format, body string, streams bool) protocol.Request {
		t.Helper()
		r, err := protocol.Inspect(from, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		target := protocol.Target{Format: to, Model: "up", MaxOutput: 100, Streams: streams}
		u, err := protocol.Prepare(&r, []byte(body), target, w)
		if err != nil {
			t.Fatalf("format %d to %d: %v", from, to, err)
		}
		got, err := protocol.Inspect(from, u.Body)
		if err != nil {
			t.Fatalf("format %d to %d: %v", from, to, err)
		}
		return got
	}
	for _, f := range []protocol.Format{protocol.Messages, protocol.Responses} {
		for _, c := range []struct{ name, body string }{
			{"absent", `{"model":"m"}`},
			{"false", `{"model":"m","stream":false}`},
			{"null", `{"model":"m","stream":null}`},
			{"true", `{"model":"m","stream":true}`},
		} {
			if !prepared(t, f, f, c.body, true).Stream {
				t.Errorf("format %d, %s: the request does not ask for a stream", f, c.name)
			}
			if prepared(t, f, f, `{"model":"m","stream":false}`, false).Stream {
				t.Errorf("format %d: an endpoint that cannot stream was asked for one", f)
			}
		}
	}
	// A Chat request is converted, and gets the member the same way.
	if !prepared(t, protocol.Chat, protocol.Messages, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, true).Stream {
		t.Error("the converted request does not ask for a stream")
	}
}

// TestPrepareRefusesChat: Chat's rewrites depend on the endpoint's dialect and are still to come, so a Chat endpoint
// cannot express a request. The gateway passes the endpoint over (a *NotSupported) instead of failing the call.
func TestPrepareRefusesChat(t *testing.T) {
	_, err := protocol.Prepare(&protocol.Request{Format: protocol.Chat}, []byte(`{"model":"m"}`),
		protocol.Target{Format: protocol.Chat, Model: "up"}, protocol.Rewrites{})
	ns, ok := errors.AsType[*protocol.NotSupported](err)
	if !ok || !strings.Contains(ns.Reason, "not rewritten") {
		t.Fatalf("got %v, want a *NotSupported", err)
	}
}
