package protocol_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// aggregated is what a gateway-like caller gets for a reply it aggregates.
type aggregated struct {
	body   []byte
	status int
	err    error // what ended the reading, nil at the upstream's normal end
	r      *protocol.Relay
	// left is what the node's budget had left when the body was returned, before the relay was released.
	left int64
}

// settledFinal and unsettled are the settlements a failure may have: one that was written, and none.
func settledFinal(u protocol.Usage) *protocol.Final {
	f := settled(u)
	return &f
}

func unsettled(protocol.Usage) *protocol.Final { return nil }

// aggregate runs a Relay as the gateway does for a client that did not ask for a stream: it writes nothing but the
// body, which Finish returns at the upstream's normal end and Fail after a failed step, with the settlement fail
// gives. It checks that no step writes and that the ending is written once.
func aggregate(t testing.TB, client, upstream protocol.Format, src io.Reader, mem *protocol.Memory,
	fail func(protocol.Usage) *protocol.Final,
) aggregated {
	t.Helper()
	o := options
	o.Memory, o.Aggregate = mem, true
	res := aggregated{r: protocol.NewRelay(client, upstream, src, protocol.UsageContext{}, o)}
	if res.r.Status() != 0 {
		t.Fatalf("status %d before the ending", res.r.Status())
	}
	for {
		step, err := res.r.Next()
		if err != nil {
			res.err = err
			res.body = res.r.Fail(protocol.Error{Type: protocol.UpstreamFailed, Message: "upstream failed"}, fail(res.r.Usage()))
			break
		}
		if len(step.Out) > 0 {
			t.Fatalf("step %d writes %q", step.Kind, step.Out)
		}
		if step.Kind == protocol.StepUpstreamDone {
			res.body = res.r.Finish(settled(res.r.Usage()))
			break
		}
	}
	res.status = res.r.Status()
	res.left = mem.Left()
	res.r.Release()
	res.r.Release() // a second call gives nothing back again
	_, err := res.r.Next()
	if again := append(res.r.Finish(settled(protocol.Usage{})), res.r.Fail(protocol.Error{}, nil)...); len(again) > 0 ||
		!errors.Is(err, protocol.ErrTerminated) || res.r.Status() != res.status {
		t.Fatalf("after the ending: Next %v, another ending %q, status %d", err, again, res.r.Status())
	}
	return res
}

func decode(t testing.TB, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := jsonx.Unmarshal(body, &m); err != nil {
		t.Fatalf("%v:\n%s", err, body)
	}
	return m
}

// aggregatePaths are the five ways a reply is aggregated: a client of each format from an upstream of its own, and a
// Chat client from either of the others.
var aggregatePaths = []struct {
	name             string
	client, upstream protocol.Format
}{
	{"messages", protocol.Messages, protocol.Messages},
	{"responses", protocol.Responses, protocol.Responses},
	{"chat", protocol.Chat, protocol.Chat},
	{"messages to chat", protocol.Chat, protocol.Messages},
	{"responses to chat", protocol.Chat, protocol.Responses},
}

// sampledPath is a stream that is aggregated for a client of one format, and the golden copy of its body.
type sampledPath struct {
	stream, golden   string
	client, upstream protocol.Format
}

// samplePaths are the streams to aggregate: each sample for each client it serves, and the assembled stream.
func samplePaths(t *testing.T) []sampledPath {
	t.Helper()
	paths := []sampledPath{
		{"assembled/responses-reasoning-tool.sse", "assembled/responses-reasoning-tool.aggregate-to-chat.json", protocol.Chat, protocol.Responses},
		{"assembled/responses-reasoning-tool.sse", "assembled/responses-reasoning-tool.aggregate-responses.json", protocol.Responses, protocol.Responses},
	}
	for _, format := range formats {
		dirs, err := fs.Glob(samples, format.dir+"/*")
		if err != nil || len(dirs) == 0 {
			t.Fatalf("no %s samples: %v", format.dir, err)
		}
		for _, dir := range dirs {
			paths = append(paths, sampledPath{dir + "/upstream.sse", dir + "/aggregate-" + format.dir + ".json", format.f, format.f})
			if format.f != protocol.Chat {
				paths = append(paths, sampledPath{dir + "/upstream.sse", dir + "/aggregate-to-chat.json", protocol.Chat, format.f})
			}
		}
	}
	return paths
}

// TestAggregateSamples aggregates every sample for each client it serves, whole and byte by byte, and compares the
// body with its golden copy, aggregate-<client>.json (aggregate-to-chat.json for a Chat client of another format),
// which go test -update rewrites. Each body has the official reply's shape, and is what the client's stream says:
// foldStream folds the stream the client would have got instead.
func TestAggregateSamples(t *testing.T) {
	paths := samplePaths(t)
	for _, c := range paths {
		t.Run(c.golden, func(t *testing.T) {
			stream := sample(t, c.stream)
			got := aggregate(t, c.client, c.upstream, bytes.NewReader(stream), protocol.NewMemory(0), unsettled)
			bytewise := aggregate(t, c.client, c.upstream, iotest.OneByteReader(bytes.NewReader(stream)), protocol.NewMemory(0), unsettled)
			if got.err != nil || got.status != http.StatusOK || !bytes.Equal(bytewise.body, got.body) {
				t.Fatalf("error %v, status %d; byte by byte:\n%s\nwhole:\n%s", got.err, got.status, bytewise.body, got.body)
			}
			checkShape(t, c.client, got.body)
			client := relayTo(t, c.client, c.upstream, bytes.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if diff := cmp.Diff(foldStream(t, c.client, client.out), decode(t, got.body)); diff != "" {
				t.Errorf("the body is not what the client's stream says (-stream +body):\n%s", diff)
			}
			golden(t, c.golden, got.body)
		})
	}
}

// checkShape holds a body to the members of the official reply of the client's format that every reply has
// (Anthropic《Messages API reference》, OpenAI《Create chat completion》《Responses API reference》, read 2026-10-15):
// they are the same whatever was generated, so a reply missing one is not one a client's SDK would take.
func checkShape(t testing.TB, f protocol.Format, body []byte) {
	t.Helper()
	if err := jsonx.Validate(body); err != nil {
		t.Fatalf("%v:\n%s", err, body)
	}
	has := func(where map[string]any, what string, names ...string) {
		for _, n := range names {
			if _, ok := where[n]; !ok {
				t.Errorf("%s has no %s:\n%s", what, n, body)
			}
		}
	}
	shapes[f](decode(t, body), has)
}

// shapes check the members of a reply of each format.
var shapes = [...]func(map[string]any, func(map[string]any, string, ...string)){
	protocol.Messages: func(m map[string]any, has func(map[string]any, string, ...string)) {
		has(m, "the message", "id", "type", "role", "model", "content", "stop_reason", "stop_sequence", "usage")
		members := map[string][]string{"text": {"text"}, "thinking": {"thinking", "signature"}, "tool_use": {"id", "name", "input"}}
		for _, b := range objects(m["content"]) {
			has(b, "a "+text(b["type"])+" block", members[text(b["type"])]...)
		}
	},
	protocol.Chat: func(m map[string]any, has func(map[string]any, string, ...string)) {
		has(m, "the completion", "id", "object", "created", "model", "choices", "usage")
		for _, choice := range objects(m["choices"]) {
			has(choice, "a choice", "index", "message", "finish_reason", "logprobs")
			has(obj(choice["message"]), "a message", "role", "content", "refusal")
			for _, call := range objects(obj(choice["message"])["tool_calls"]) {
				has(call, "a tool call", "id", "type", "function")
				has(obj(call["function"]), "a function", "name", "arguments")
			}
		}
	},
	protocol.Responses: func(m map[string]any, has func(map[string]any, string, ...string)) {
		has(m, "the response", "id", "object", "created_at", "status", "model", "output", "usage")
		for _, item := range objects(m["output"]) {
			has(item, "an output item", "id", "type")
		}
	},
}

// TestAggregateFail: a reply that fails after its first content is, for a Chat or Responses client, a 200 with what
// was generated and the error in it, and for a Messages client, which has no stop reason for an error, a 502 with
// the error alone; both carry the usage, and the cost if the settlement was written (docs/domains/gateway.md「错误」).
func TestAggregateFail(t *testing.T) {
	messages := messageStart(`{"input_tokens":3}`) + textBlock
	responses := responsesEvent("response.created", 0, strings.Replace(responsesObject, `"id"`, `"created_at":1700000000,"id"`, 1)) +
		responsesEvent("response.output_item.added", 1, `,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}`) +
		responsesEvent("response.content_part.added", 2, `,"output_index":0,"content_index":0,"item_id":"msg_1","part":{"type":"output_text","text":"","annotations":[]}`) +
		responsesEvent("response.output_text.delta", 3, `,"output_index":0,"content_index":0,"item_id":"msg_1","delta":"Hi"`)
	for _, c := range []struct {
		name, stream string
		path         int // in aggregatePaths
	}{
		{"messages", messages, 0},
		{"responses", responses, 1},
		{"chat", chatRole + chatHi, 2},
		{"messages to chat", messages, 3},
		{"responses to chat", responses, 4},
	} {
		for _, fail := range []struct {
			name string
			f    func(protocol.Usage) *protocol.Final
		}{{"settled", settledFinal}, {"not settled", unsettled}} {
			t.Run(c.name+", "+fail.name, func(t *testing.T) {
				p := aggregatePaths[c.path]
				got := aggregate(t, p.client, p.upstream, strings.NewReader(c.stream), protocol.NewMemory(0), fail.f)
				if !errors.Is(got.err, protocol.ErrUpstream) {
					t.Fatalf("error %v", got.err)
				}
				usage := checkFailure(t, p.client, got)
				if _, has := obj(usage)["cost"]; has != (fail.name == "settled") {
					t.Errorf("cost in the usage %v:\n%s", has, got.body)
				}
			})
		}
	}
}

// checkFailure checks the body of a reply that failed after its first content, "Hi", and returns its usage.
func checkFailure(t *testing.T, client protocol.Format, got aggregated) any {
	t.Helper()
	m := decode(t, got.body)
	return failures[client](t, got, m)
}

var failures = [...]func(*testing.T, aggregated, map[string]any) any{
	protocol.Messages: func(t *testing.T, got aggregated, m map[string]any) any {
		if got.status != http.StatusBadGateway || m["type"] != "error" || m["content"] != nil {
			t.Errorf("status %d:\n%s", got.status, got.body)
		}
		if e := obj(m["error"]); e["type"] != "api_error" || e["error_type"] != "upstream_error" || e["message"] != "upstream failed" {
			t.Errorf("error %v", e)
		}
		return obj(m["axiom"])["usage"]
	},
	protocol.Chat: func(t *testing.T, got aggregated, m map[string]any) any {
		choice := obj(objects(m["choices"])[0])
		if got.status != http.StatusOK || choice["finish_reason"] != "error" || obj(choice["message"])["content"] != "Hi" {
			t.Errorf("status %d:\n%s", got.status, got.body)
		}
		if e := obj(m["error"]); e["code"] != 502.0 || obj(e["metadata"])["error_type"] != "upstream_error" || e["message"] != "upstream failed" {
			t.Errorf("error %v", e)
		}
		return m["usage"]
	},
	protocol.Responses: func(t *testing.T, got aggregated, m map[string]any) any {
		part := obj(listOf(obj(objects(m["output"])[0])["content"])[0])
		if got.status != http.StatusOK || m["status"] != "failed" || part["text"] != "Hi" || m["created_at"] != 1700000000.0 || m["error_type"] != "upstream_error" {
			t.Errorf("status %d:\n%s", got.status, got.body)
		}
		if e := obj(m["error"]); e["code"] != "upstream_error" || e["message"] != "upstream failed" {
			t.Errorf("error %v", e)
		}
		return m["usage"]
	},
}

// TestAggregateFailBeforeContent: a reply that failed before any content is the answer RenderError gives, with its
// status, in each format; the gateway may still replace it with another endpoint's. Without a settlement the answer
// has the usage, no cost and no pending flag.
func TestAggregateFailBeforeContent(t *testing.T) {
	streams := [...]string{
		protocol.Messages: messageStart(`{"input_tokens":3}`),
		protocol.Responses: responsesEvent("response.created", 0, responsesObject) +
			responsesEvent("response.output_item.added", 1, `,"output_index":0,"item":{"id":"msg_1","type":"message","content":[]}`),
		protocol.Chat: chatRole,
	}
	e := protocol.Error{Type: protocol.UpstreamFailed, Message: "upstream failed"}
	for _, p := range aggregatePaths {
		t.Run(p.name, func(t *testing.T) {
			got := aggregate(t, p.client, p.upstream, strings.NewReader(streams[p.upstream]), protocol.NewMemory(0), settledFinal)
			fin := settled(got.r.Usage())
			if status, want := protocol.RenderError(p.client, e, &fin); got.status != status || !bytes.Equal(got.body, want) {
				t.Errorf("status %d, want %d:\n%s\nwant:\n%s", got.status, status, got.body, want)
			}
			got = aggregate(t, p.client, p.upstream, strings.NewReader(streams[p.upstream]), protocol.NewMemory(0), unsettled)
			usage := decode(t, got.body)["axiom"].(map[string]any)["usage"].(map[string]any)
			_, costed := usage["cost"]
			_, pending := decode(t, got.body)["axiom"].(map[string]any)["billing_pending"]
			if got.status != http.StatusBadGateway || costed || pending || len(usage) == 0 {
				t.Errorf("status %d, usage %v, pending %v", got.status, usage, pending)
			}
		})
	}
}
