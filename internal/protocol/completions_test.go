package protocol_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// completionSettled is settledFinal as a relay's settle function returns it.
func completionSettled(u protocol.Usage) protocol.Final { return *settledFinal(u) }

// completionsChunk is one chunk of a text completion with one token's logprobs; finish is the JSON of its
// finish_reason.
func completionsChunk(i int, text, token, finish string) string {
	return fmt.Sprintf(`data: {"id":"cmpl-1","object":"text_completion","created":1760000000,"model":"m",`+
		`"choices":[{"index":0,"text":%q,"logprobs":{"tokens":[%q],"token_logprobs":[-0.25],"text_offset":[%d],`+
		`"top_logprobs":[{%q:-0.25}]},"finish_reason":%s}]}`+"\n\n", text, token, i, token, finish)
}

// completionsStreamOf builds a stream of n chunks that each add a token of size bytes, a usage chunk and [DONE].
func completionsStreamOf(n, size int) string {
	var b strings.Builder
	for i := range n {
		finish := "null"
		if i == n-1 {
			finish = `"length"`
		}
		b.WriteString(completionsChunk(i, "x", strings.Repeat("t", size), finish))
	}
	b.WriteString(`data: {"id":"cmpl-1","object":"text_completion","created":1760000000,"model":"m","choices":[],` +
		`"usage":{"prompt_tokens":9,"completion_tokens":7,"total_tokens":16}}` + "\n\n")
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// TestCompletionsInspect: the prompt's four forms, the counts the reservation is made of, and the members that are
// refused before an upstream sees them.
//
//nolint:cyclop,gocognit // the table and its assertions are the test
func TestCompletionsInspect(t *testing.T) {
	for _, c := range []struct {
		name                            string
		body                            string
		text, maxInput, choices, maxOut int64
		stream                          bool
		err                             string
	}{
		{name: "string", body: `{"model":"m","prompt":"Say hi"}`, text: 6, maxInput: 6, choices: 1},
		{name: "strings", body: `{"model":"m","prompt":["a","bc"]}`, text: 3, maxInput: 2, choices: 2},
		{name: "strings with n", body: `{"model":"m","prompt":["a","bc"],"n":3}`, text: 3, maxInput: 2, choices: 6},
		{name: "tokens", body: `{"model":"m","prompt":[1,2,3]}`, text: 12, maxInput: 12, choices: 1},
		{name: "token arrays", body: `{"model":"m","prompt":[[1,2],[3,4,5]],"n":2}`, text: 20, maxInput: 12, choices: 4},
		{name: "best_of", body: `{"model":"m","prompt":["a","b"],"n":2,"best_of":5}`, text: 2, maxInput: 1, choices: 10},
		{name: "escapes", body: `{"model":"m","prompt":"a\nb"}`, text: 3, maxInput: 3, choices: 1},
		{name: "stream", body: `{"model":"m","prompt":"x","stream":true}`, text: 1, maxInput: 1, choices: 1, stream: true},
		{name: "max_tokens", body: `{"model":"m","prompt":"x","max_tokens":5}`, text: 1, maxInput: 1, choices: 1, maxOut: 5},
		{name: "n zero", body: `{"model":"m","prompt":"x","n":0}`, text: 1, maxInput: 1, choices: 1},
		{name: "model missing", body: `{"prompt":"x"}`, err: "model is missing"},
		{name: "prompt missing", body: `{"model":"m"}`, err: "prompt is missing"},
		{name: "prompt null", body: `{"model":"m","prompt":null}`, err: "prompt is null"},
		{name: "empty prompt array", body: `{"model":"m","prompt":[]}`, err: "prompt is an empty array"},
		{name: "mixed prompt", body: `{"model":"m","prompt":["a",1]}`, err: "mixes strings with other values"},
		{name: "mixed token arrays", body: `{"model":"m","prompt":[[1],2]}`, err: "mixes token arrays with other values"},
		{name: "prompt object", body: `{"model":"m","prompt":{}}`, err: "prompt is an object"},
		{name: "n not a number", body: `{"model":"m","prompt":"x","n":"2"}`, err: "n is not an integer"},
		{name: "stream not a bool", body: `{"model":"m","prompt":"x","stream":"yes"}`, err: "stream is not a boolean"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := protocol.Inspect(protocol.Completions, []byte(c.body))
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("error %v, want %q", err, c.err)
				}
				if !errors.Is(err, protocol.ErrInvalid) {
					t.Errorf("%v is not an invalid request", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Format != protocol.Completions || r.Model != "m" || r.TextBytes != c.text || r.MaxInputBytes != c.maxInput ||
				r.Choices != c.choices || r.MaxOutput != c.maxOut || r.Stream != c.stream {
				t.Errorf("request %+v, want text %d, largest %d, choices %d, cap %d, stream %v",
					r, c.text, c.maxInput, c.choices, c.maxOut, c.stream)
			}
			if r.InputItems != 1<<catalog.ItemInput || r.ServiceTiers != 1<<catalog.ServiceTierStandard {
				t.Errorf("items %b, tiers %b", r.InputItems, r.ServiceTiers)
			}
		})
	}
}

// TestCompletionsPrepare: the model and the identifiers are rewritten, the prompt is copied as it is, and the request
// asks for the stream the gateway reads, with the cap it was quoted at.
//
//nolint:cyclop,gocognit // the table and its assertions are the test
func TestCompletionsPrepare(t *testing.T) {
	for _, c := range []struct {
		name, body string
		maxOutput  int64
		wantCap    any
		wantPrompt any
	}{
		{
			name: "no cap", body: `{"model":"m","prompt":"hi","user":"u","stream":false}`, maxOutput: 16, wantCap: 16.0,
			wantPrompt: "hi",
		},
		{
			name: "client cap", body: `{"model":"m","prompt":["a","b"],"max_tokens":5}`, maxOutput: 16, wantCap: 5.0,
			wantPrompt: []any{"a", "b"},
		},
		{
			name: "null cap", body: `{"model":"m","prompt":"hi","max_tokens":null}`, maxOutput: 16, wantCap: 16.0,
			wantPrompt: "hi",
		},
		{
			name: "zero cap", body: `{"model":"m","prompt":"hi","max_tokens":0,"echo":true}`, maxOutput: 16, wantCap: 0.0,
			wantPrompt: "hi",
		},
		{
			name: "usage forced", body: `{"model":"m","prompt":"hi","stream_options":{"include_usage":false,"x":1}}`,
			maxOutput: 16, wantCap: 16.0, wantPrompt: "hi",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := []byte(c.body)
			r, err := protocol.Inspect(protocol.Completions, body)
			if err != nil {
				t.Fatal(err)
			}
			up, err := protocol.Prepare(&r, body, protocol.Target{
				Format: protocol.Completions, Model: "up", Streams: true,
				MaxOutput: c.maxOutput,
			}, protocol.Rewrites{Identity: func(string) string { return "derived" }})
			if err != nil {
				t.Fatal(err)
			}
			got := decode(t, up.Body)
			if got["model"] != "up" || got["stream"] != true {
				t.Errorf("model, stream = %v, %v", got["model"], got["stream"])
			}
			if _, has := decode(t, []byte(c.body))["user"]; has && got["user"] != "derived" {
				t.Errorf("user = %v, want the derived identifier", got["user"])
			}
			if diff := cmp.Diff(c.wantPrompt, got["prompt"]); diff != "" {
				t.Errorf("prompt changed: %s\n%s", diff, up.Body)
			}
			if c.wantCap != nil && got["max_tokens"] != c.wantCap {
				t.Errorf("max_tokens = %v, want %v\n%s", got["max_tokens"], c.wantCap, up.Body)
			}
			options, ok := got["stream_options"].(map[string]any)
			if !ok || options["include_usage"] != true {
				t.Errorf("stream_options = %v\n%s", got["stream_options"], up.Body)
			}
		})
	}
	if _, err := protocol.Prepare(&protocol.Request{Format: protocol.Completions}, []byte(`{"model":"m","prompt":"x"}`),
		protocol.Target{Format: protocol.Chat, Model: "up"}, protocol.Rewrites{}); err == nil || !strings.Contains(err.Error(), "not converted") {
		t.Errorf("preparing a completion for a Chat endpoint: %v", err)
	}
}

// TestCompletionsSample relays and aggregates the transcription of OpenAI's streaming example (testdata/completions).
//
//nolint:cyclop,gocognit // the table and its assertions are the test
func TestCompletionsSample(t *testing.T) {
	body := sample(t, "completions/text/request.json")
	r, err := protocol.Inspect(protocol.Completions, body)
	if err != nil {
		t.Fatal(err)
	}
	up, err := protocol.Prepare(&r, body, protocol.Target{
		Format: protocol.Completions, Model: "gpt-3.5-turbo-instruct",
		Streams: true, MaxOutput: 16,
	}, protocol.Rewrites{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(up.Body, []byte(`"stream_options":{"include_usage":true}`)) {
		t.Errorf("the prepared request does not ask for usage: %s", up.Body)
	}
	stream := sample(t, "completions/text/upstream.sse")
	settle := func(protocol.Usage) protocol.Final {
		u := must(catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 9, catalog.ItemOutput: 3},
			catalog.ServiceTierStandard, nil))
		return protocol.Final{Usage: protocol.Usage{Segments: []protocol.Segment{{Usage: u, Reported: items(
			catalog.ItemInput, catalog.ItemOutput)}}}, Cost: price.Mul(1234), Currency: usd}
	}
	got := relay(t, protocol.Completions, bytes.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0), settle)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !strings.HasSuffix(string(got.out), "data: [DONE]\n\n") {
		t.Fatalf("the stream does not end with [DONE]:\n%s", got.out)
	}
	chunks := completionChunks(t, got.out)
	if len(chunks) != 4 { // three text chunks and the usage chunk
		t.Fatalf("%d chunks:\n%s", len(chunks), got.out)
	}
	for i, chunk := range chunks[:3] {
		if chunk["object"] != "text_completion" || chunk["id"] != "gen-01" || chunk["model"] != "claude-client-name" ||
			chunk["created"] != 1589478378.0 {
			t.Errorf("chunk %d: %v", i, chunk)
		}
	}
	if choices := objects(chunks[2]["choices"]); len(choices) != 1 || choices[0]["text"] != " is" ||
		choices[0]["finish_reason"] != "length" {
		t.Errorf("the last text chunk: %v", chunks[2])
	}
	if len(objects(chunks[3]["choices"])) != 0 {
		t.Errorf("the usage chunk has choices: %v", chunks[3])
	}
	if usage := obj(chunks[3]["usage"]); usage["completion_tokens"] != 3.0 || usage["prompt_tokens"] != 9.0 {
		t.Errorf("usage = %v", chunks[3]["usage"])
	}

	agg := aggregate(t, protocol.Completions, protocol.Completions, bytes.NewReader(stream), protocol.NewMemory(128<<20),
		settledFinal)
	if agg.err != nil || agg.status != 200 {
		t.Fatalf("aggregated: %v, status %d", agg.err, agg.status)
	}
	reply := decode(t, agg.body)
	choices := objects(reply["choices"])
	if len(choices) != 1 || choices[0]["text"] != "Say this is" || choices[0]["finish_reason"] != "length" ||
		choices[0]["logprobs"] != nil {
		t.Errorf("the reply: %v", choices)
	}
	if reply["id"] != "gen-01" || reply["model"] != "claude-client-name" || reply["object"] != "text_completion" {
		t.Errorf("the reply's identity: %v", reply)
	}
}

// completionChunks parses a relayed stream's data events, checking that it ends once with [DONE].
func completionChunks(t *testing.T, out []byte) []map[string]any {
	t.Helper()
	var chunks []map[string]any
	lines := strings.Split(strings.TrimSuffix(string(out), "\n\n"), "\n\n")
	for i, line := range lines {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			t.Fatalf("event %d is not data: %q", i, line)
		}
		if data == "[DONE]" {
			if i != len(lines)-1 {
				t.Fatalf("[DONE] is not the last event:\n%s", out)
			}
			continue
		}
		var m map[string]any
		if err := jsonx.Unmarshal([]byte(data), &m); err != nil {
			t.Fatalf("event %d: %v\n%s", i, err, data)
		}
		chunks = append(chunks, m)
	}
	return chunks
}

// TestCompletionsUsageEnding: the upstream's usage is taken from the ending chunk and the client gets the relay's own
// usage chunk, once, after the choices, with the cost; the aggregated body carries the same counts.
func TestCompletionsUsageEnding(t *testing.T) {
	stream := completionsStreamOf(1, 1)
	got := relay(t, protocol.Completions, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0),
		completionSettled)
	if got.err != nil {
		t.Fatal(got.err)
	}
	chunks := completionChunks(t, got.out)
	if n := len(chunks); n != 2 {
		t.Fatalf("%d chunks, want the text and the usage:\n%s", n, got.out)
	}
	usage := obj(chunks[1]["usage"])
	if usage["prompt_tokens"] != 9.0 || usage["completion_tokens"] != 7.0 || usage["total_tokens"] != 16.0 ||
		usage["cost"] == nil {
		t.Errorf("usage = %v", chunks[1]["usage"])
	}

	agg := aggregate(t, protocol.Completions, protocol.Completions, strings.NewReader(stream), protocol.NewMemory(128<<20),
		settledFinal)
	if agg.err != nil {
		t.Fatal(agg.err)
	}
	reply := decode(t, agg.body)
	if usage := obj(reply["usage"]); usage["prompt_tokens"] != 9.0 || usage["completion_tokens"] != 7.0 {
		t.Errorf("the aggregated usage = %v", reply["usage"])
	}
}

// TestCompletionsLogprobsLongStream: many chunks whose reply stays far below the cap are all kept: the arrays are
// appended, not merged into a new object per chunk (docs/domains/gateway.md「决策与理由」).
func TestCompletionsLogprobsLongStream(t *testing.T) {
	const chunks, size = 130, 1 << 10
	stream := completionsStreamOf(chunks, size)
	agg := aggregate(t, protocol.Completions, protocol.Completions, strings.NewReader(stream), protocol.NewMemory(128<<20),
		settledFinal)
	if agg.err != nil || agg.status != 200 {
		t.Fatalf("aggregated: %v, status %d", agg.err, agg.status)
	}
	reply := decode(t, agg.body)
	choices := objects(reply["choices"])
	if len(choices) != 1 {
		t.Fatalf("%d choices", len(choices))
	}
	if got := choices[0]["text"]; got != strings.Repeat("x", chunks) {
		t.Errorf("text is %q", got)
	}
	if got := len(obj(choices[0]["logprobs"])); got != 4 {
		t.Errorf("%d logprobs arrays", got)
	}
	logs := obj(choices[0]["logprobs"])
	for _, name := range []string{"tokens", "token_logprobs", "text_offset", "top_logprobs"} {
		if got := len(objects(logs[name])); got != chunks {
			t.Errorf("%s has %d elements, want %d", name, got, chunks)
		}
	}
	if got := len(agg.body); got > 1<<20 {
		t.Errorf("the reply is %d bytes for %d chunks", got, chunks)
	}
}

// TestCompletionsFailures: a stream that breaks its shape is the upstream's failure, not a local capacity error.
func TestCompletionsFailures(t *testing.T) {
	text := `data: {"id":"x","object":"text_completion","created":1,"model":"m","choices":[{"index":0,"text":"Hi",` +
		`"finish_reason":null}]}` + "\n\n"
	usage := `data: {"id":"x","object":"text_completion","created":1,"model":"m","choices":[],` +
		`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n"
	for _, c := range []struct {
		name, stream string
	}{
		{"done before finished", text + "data: [DONE]\n\n"},
		{"no done", text},
		{"error chunk", text + `data: {"id":"x","choices":[],"error":{"message":"boom"}}` + "\n\n"},
		{"text after the ending", text + usage + text + "data: [DONE]\n\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := relay(t, protocol.Completions, strings.NewReader(c.stream), protocol.UsageContext{},
				protocol.NewMemory(0), completionSettled)
			if !errors.Is(got.err, protocol.ErrUpstream) || errors.Is(got.err, protocol.ErrMemory) {
				t.Errorf("error %v, want the upstream's", got.err)
			}
			if c.name == "error chunk" || c.name == "text after the ending" {
				chunks := completionChunks(t, got.out)
				if last := chunks[len(chunks)-1]; last["error"] == nil {
					t.Errorf("the failure ending has no error: %v", last)
				}
			}
		})
	}
}

// TestCompletionsAggregateBodyBudget: a reply past the 16 MiB cap is a memory error, and the pieces it did hold are
// given back by the relay's Release; the failure is not the upstream's.
func TestCompletionsAggregateBodyBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("17 MiB of logprobs")
	}
	const chunks = 70
	stream := completionsStreamOf(chunks, 256<<10)
	mem := protocol.NewMemory(256 << 20)
	agg := aggregate(t, protocol.Completions, protocol.Completions, strings.NewReader(stream), mem, settledFinal)
	if !errors.Is(agg.err, protocol.ErrMemory) || errors.Is(agg.err, protocol.ErrUpstream) {
		t.Fatalf("error %v, want a memory error", agg.err)
	}
	if mem.Left() != 256<<20 {
		t.Errorf("the budget is %d after Release, was %d", mem.Left(), 256<<20)
	}
}

// TestCompletionsFormatNumbers keeps the new format at the end: suppliers store the names, and a routing set keeps the
// numbers, so an inserted format would silently turn one into another on a node of the previous release.
func TestCompletionsFormatNumbers(t *testing.T) {
	if protocol.Completions != protocol.Rerank+1 {
		t.Errorf("Completions is %d, want %d", protocol.Completions, protocol.Rerank+1)
	}
	if protocol.Completions.Direct() || protocol.Completions.Convertible() {
		t.Error("a completion is not direct and does not convert")
	}
	for _, f := range []protocol.Format{protocol.Messages, protocol.Responses, protocol.Chat} {
		if !f.Convertible() {
			t.Errorf("%d does not convert", f)
		}
	}
	for _, f := range []protocol.Format{protocol.Embeddings, protocol.Rerank} {
		if f.Convertible() || !f.Direct() {
			t.Errorf("%d: direct %v, convertible %v", f, f.Direct(), f.Convertible())
		}
	}
}
