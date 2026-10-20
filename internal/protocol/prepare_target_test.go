package protocol_test

import (
	"testing"

	"axiom/internal/protocol"
)

// TestPrepareAsksForAStream: an endpoint that streams is called with a stream whatever the client asked, since the
// gateway reads every answer as one and aggregates it for a client that wants no stream
// (docs/domains/gateway.md「转发规则」): a request that names no stream gets the member, and false or null is turned
// true. An endpoint that does not stream keeps what the client asked.
func TestPrepareAsksForAStream(t *testing.T) {
	names := []string{"absent", "false", "null", "true"}
	bodies := []string{`{"model":"m"}`, `{"model":"m","stream":false}`, `{"model":"m","stream":null}`, `{"model":"m","stream":true}`}
	for _, f := range []protocol.Format{protocol.Messages, protocol.Responses, protocol.Chat} {
		for i, body := range bodies {
			checkStreamAsked(t, f, names[i], body)
		}
	}
	// A Chat request is converted, and gets the member the same way.
	if !preparedReply(t, protocol.Chat, protocol.Messages, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, true).Stream {
		t.Error("the converted request does not ask for a stream")
	}
}

// checkStreamAsked: the request sent to a streaming endpoint asks for a stream, and the one sent to an endpoint that
// cannot stream keeps what the client wrote.
func checkStreamAsked(t *testing.T, f protocol.Format, name, body string) {
	t.Helper()
	if !preparedReply(t, f, f, body, true).Stream {
		t.Errorf("format %d, %s: the request does not ask for a stream", f, name)
	}
	if preparedReply(t, f, f, `{"model":"m","stream":false}`, false).Stream {
		t.Errorf("format %d: an endpoint that cannot stream was asked for one", f)
	}
}

// preparedReply is the summary of the request Prepare writes from body for an endpoint of format to.
func preparedReply(t *testing.T, from, to protocol.Format, body string, streams bool) protocol.Request {
	t.Helper()
	r, err := protocol.Inspect(from, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	w := protocol.Rewrites{Identity: derive}
	u, err := protocol.Prepare(&r, []byte(body), protocol.Target{Format: to, Model: "up", MaxOutput: 100, Streams: streams}, w)
	if err != nil {
		t.Fatalf("format %d to %d: %v", from, to, err)
	}
	got, err := protocol.Inspect(from, u.Body)
	if err != nil {
		t.Fatalf("format %d to %d: %v", from, to, err)
	}
	return got
}

// TestPrepareCompactionRewritesOnlyWhatMustNotGoUpstream: a compaction request is not a generation, so only the members
// that must not reach an upstream as they are change: the model becomes the endpoint's name, the three identifiers are
// derived, and the routing parameters go. Nothing is added — store, include, the output cap and the input stay as the
// client wrote them (docs/domains/gateway.md「转发规则」).
func TestPrepareCompactionRewritesOnlyWhatMustNotGoUpstream(t *testing.T) {
	body := `{"model":"client-model","input":[{"role":"user","content":"hi"}],"store":true,"max_output_tokens":7,` +
		`"include":["reasoning.encrypted_content"],"user":"u","prompt_cache_key":"k","safety_identifier":"s",` +
		`"provider":{"order":["a"]},"models":["m"],"route":"fallback","session_id":"abc","axiom":{"trace":"t"}}`
	want := `{"model":"endpoint-model","input":[{"role":"user","content":"hi"}],"store":true,"max_output_tokens":7,` +
		`"include":["reasoning.encrypted_content"],"user":"h(u)","prompt_cache_key":"h(k)","safety_identifier":"h(s)"}`
	target := protocol.Target{Format: protocol.Responses, Model: "endpoint-model", MaxOutput: 100, Streams: true, Compact: true}
	w := protocol.Rewrites{Identity: derive}
	r, err := protocol.Inspect(protocol.Responses, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	u, err := protocol.Prepare(&r, []byte(body), target, w)
	if err != nil {
		t.Fatal(err)
	}
	if string(u.Body) != want {
		t.Errorf("the compaction request sent is\n%s\nwant\n%s", u.Body, want)
	}
	if cost := protocol.PrepareCost(&r, []byte(body), target, w); cost < int64(cap(u.Body)) {
		t.Errorf("the cost %d does not cover the %d bytes written", cost, cap(u.Body))
	}
}
