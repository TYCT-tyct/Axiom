package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// fnCall is a function call as a client or an upstream reads it; args is the arguments' JSON, canonical.
type fnCall struct{ id, name, args string }

// toolUpstream is an upstream format of the tool call matrix: the model the world serves in it, the recorded stream in
// which the model calls tools, the calls that stream makes and what the call is booked as.
type toolUpstream struct {
	format, model, stream string
	calls                 []fnCall
	booked                booking
}

// toolFormat is how a generation format carries function calls: where it is called, why a client reads that the model
// stopped to call, the two functions every call of the matrix declares, the conversation a client sends (the question,
// and with calls, the model's calls and a result for each), and how the calls are read from a streamed event, from a
// whole answer and, with their results, from a request.
type toolFormat struct {
	path, stop, tools string
	conversation      func(calls []fnCall) string
	event             func(s *toolStream, data []byte) error
	whole             func(body []byte) (calls []fnCall, stop string, err error)
	sent              func(t *testing.T, body []byte) (calls []fnCall, results []string)
}

// toolQuestion is the user's turn every conversation of the matrix starts with, which all three formats read alike.
const toolQuestion = `{"role":"user","content":"Weather and time in Paris?"}`

var toolFormats = map[string]toolFormat{
	"chat": {
		"/v1/chat/completions", "tool_calls",
		`[{"type":"function","function":{"name":"get_weather","description":"Weather","parameters":{"type":"object","properties":{"location":{"type":"string"}}}}},` +
			`{"type":"function","function":{"name":"get_time","description":"Time","parameters":{"type":"object","properties":{}}}}]`,
		chatConversation, chatToolEvent, chatToolAnswer, chatToolsSent,
	},
	"responses": {
		"/v1/responses", "completed",
		`[{"type":"function","name":"get_weather","description":"Weather","parameters":{"type":"object","properties":{"location":{"type":"string"}}}},` +
			`{"type":"function","name":"get_time","description":"Time","parameters":{"type":"object","properties":{}}}]`,
		responsesConversation, responsesToolEvent, responsesToolAnswer, responsesToolsSent,
	},
	"messages": {
		"/v1/messages", "tool_use",
		`[{"name":"get_weather","description":"Weather","input_schema":{"type":"object","properties":{"location":{"type":"string"}}}},` +
			`{"name":"get_time","description":"Time","input_schema":{"type":"object","properties":{}}}]`,
		messagesConversation, messagesToolEvent, messagesToolAnswer, messagesToolsSent,
	},
}

// TestToolCallsRoundTrip: a model that calls functions, served in each of the three generation formats, answers a
// client of each of them, whole and streamed. The client reads the calls in its own format with the name and arguments
// the upstream sent, the call is billed for the tokens the upstream reported, and the client's next call, which carries
// the calls and their results under the IDs it was given, reaches the upstream as calls paired with their results in
// the upstream's format, and is answered and billed in turn.
func TestToolCallsRoundTrip(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	upstreams := w.toolUpstreams()
	w.serveChatModel()
	w.price()
	n := w.start()
	waitUntil(t, 15*time.Second, func() bool { return n.ready(t) })
	for _, up := range upstreams {
		w.supplier.answer(toolFormats[up.format].path, up.stream)
	}
	for _, up := range upstreams {
		for _, client := range []string{"chat", "responses", "messages"} {
			for _, streamed := range []bool{false, true} {
				name := client + " client of " + up.format
				if streamed {
					name += " streamed"
				}
				t.Run(name, func(t *testing.T) {
					sub := *w
					sub.t = t
					sub.checkToolRoundTrip(n, up, client, streamed)
				})
			}
		}
	}
}

// toolUpstreams reads the recorded tool call streams of the three formats.
func (w *callWorld) toolUpstreams() []toolUpstream {
	w.t.Helper()
	samples, err := os.OpenRoot(filepath.Join("..", "..", "internal", "protocol", "testdata"))
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() {
		if err := samples.Close(); err != nil {
			w.t.Error(err)
		}
	}()
	read := func(name string) string {
		b, err := samples.ReadFile(name)
		if err != nil {
			w.t.Fatal(err)
		}
		return string(b)
	}
	weather := func(id, city string) fnCall { return fnCall{id, "get_weather", `{"location":"` + city + `"}`} }
	return []toolUpstream{
		{
			"messages", "claude-opus-5-5", read("messages/tool-use/upstream.sse"),
			[]fnCall{weather("toolu_01T1x1fJ34qAmk2tNTrN7Up6", "San Francisco, CA")},
			booking{charge: 472*4_500 + 89*22_500, cost: 472*10 + 89*40, upstream: "up-claude", input: 472, output: 89},
		},
		{
			"responses", "gpt-6-astra", read("assembled/responses-reasoning-tool.sse"),
			[]fnCall{weather("call_1", "Paris")},
			// 12 of the 31 output tokens are reasoning, which the call log keeps apart from the output.
			booking{charge: 52*4_500 + 31*22_500, cost: 52*10 + 31*40, upstream: "up-gpt", input: 52, output: 19},
		},
		{
			// 64 of the 120 input tokens are read from the cache, at a tenth of the price and free to the supplier.
			"chat", "deepseek-chat", read("assembled/chat-tool-calls.sse"),
			[]fnCall{weather("call_00_Kq3vT9", "Paris"), {"call_01_Zb8mW2", "get_time", "{}"}},
			booking{charge: 56*4_500 + 64*450 + 40*22_500, cost: 56*10 + 40*40, upstream: "up-deepseek", input: 56, output: 40},
		},
	}
}

// serveChatModel lists a third model and serves it on the world's supplier at an endpoint that speaks Chat.
func (w *callWorld) serveChatModel() {
	w.t.Helper()
	cat, err := catalog.Open(w.db, pg.PlatformScope(), catalog.Config{MaxStale: time.Hour})
	if err != nil {
		w.t.Fatal(err)
	}
	model := w.listModel(cat, "deepseek/deepseek-chat", "deepseek-chat")
	supplier := uuid.MustParse(w.query("SELECT id::text FROM suppliers.suppliers"))
	group := uuid.MustParse(w.query("SELECT id::text FROM suppliers.supplier_groups"))
	ctx := w.t.Context()
	if err := w.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		for _, s := range endpointStatements(supplier, group, model, "up-deepseek", "chat") {
			if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		w.t.Fatal(err)
	}
}

// checkToolRoundTrip makes the first call, which the upstream answers with its calls, and the second, which carries
// them back with their results.
func (w *callWorld) checkToolRoundTrip(n runningNode, up toolUpstream, client string, streamed bool) {
	t := w.t
	t.Helper()
	f, upstream := toolFormats[client], toolFormats[up.format]
	r := w.call(n, f.path, w.richKey, toolCallBody(f, up.model, streamed, nil))
	w.served(r, "the first call")
	if sent := w.supplier.sent(); sent[len(sent)-1].path != upstream.path {
		t.Errorf("the supplier was called at %s, want %s", sent[len(sent)-1].path, upstream.path)
	}
	calls, stop := w.clientToolCalls(r, f, streamed)
	if !slices.Equal(calls, up.calls) || stop != f.stop {
		t.Errorf("the client read the calls %+v, stopping for %q; want %+v, stopping for %q", calls, stop, up.calls, f.stop)
	}
	up.booked.ref = w.lastRef()
	w.checkBooked(r, up.booked)
	if len(calls) == 0 {
		return
	}

	r = w.call(n, f.path, w.richKey, toolCallBody(f, up.model, streamed, calls))
	w.served(r, "the call that carries the results")
	sent := w.supplier.sent()
	body := sent[len(sent)-1].body
	got, results := upstream.sent(t, []byte(body))
	ids := make([]string, len(calls))
	for i, c := range calls {
		ids[i] = c.id
	}
	if !slices.Equal(got, calls) || !slices.Equal(results, ids) {
		t.Errorf("the upstream was sent the calls %+v and results for %v, want %+v answered in that order: %s", got, results, calls, body)
	}
	up.booked.ref = w.lastRef()
	w.checkBooked(r, up.booked)
}

// toolCallBody is a call of the format to the model that declares the tools.
func toolCallBody(f toolFormat, model string, streamed bool, calls []fnCall) string {
	body := `{"model":` + quote(model) + `,` + f.conversation(calls) + `,"tools":` + f.tools
	if streamed {
		body += `,"stream":true`
	}
	return body + "}"
}

func chatConversation(calls []fnCall) string {
	turns := []string{toolQuestion}
	if calls != nil {
		var tc []string
		for _, c := range calls {
			tc = append(tc, `{"id":`+quote(c.id)+`,"type":"function","function":{"name":`+quote(c.name)+`,"arguments":`+quote(c.args)+`}}`)
		}
		turns = append(turns, `{"role":"assistant","content":null,"tool_calls":[`+strings.Join(tc, ",")+`]}`)
		for _, c := range calls {
			turns = append(turns, `{"role":"tool","tool_call_id":`+quote(c.id)+`,"content":"Sunny"}`)
		}
	}
	return `"messages":[` + strings.Join(turns, ",") + `]`
}

func responsesConversation(calls []fnCall) string {
	items := []string{toolQuestion}
	for _, c := range calls {
		items = append(items, `{"type":"function_call","call_id":`+quote(c.id)+`,"name":`+quote(c.name)+`,"arguments":`+quote(c.args)+`}`)
	}
	for _, c := range calls {
		items = append(items, `{"type":"function_call_output","call_id":`+quote(c.id)+`,"output":"Sunny"}`)
	}
	return `"input":[` + strings.Join(items, ",") + `]`
}

func messagesConversation(calls []fnCall) string {
	turns := []string{toolQuestion}
	if calls != nil {
		var uses, results []string
		for _, c := range calls {
			uses = append(uses, `{"type":"tool_use","id":`+quote(c.id)+`,"name":`+quote(c.name)+`,"input":`+c.args+`}`)
			results = append(results, `{"type":"tool_result","tool_use_id":`+quote(c.id)+`,"content":"Sunny"}`)
		}
		turns = append(turns, `{"role":"assistant","content":[`+strings.Join(uses, ",")+`]}`, `{"role":"user","content":[`+strings.Join(results, ",")+`]}`)
	}
	return `"max_tokens":256,"messages":[` + strings.Join(turns, ",") + `]`
}

func quote(s string) string {
	b, err := jsonx.MarshalCanonical(s)
	if err != nil {
		panic(err) // a string always marshals
	}
	return string(b)
}

// canonicalJSON is the JSON text with its whitespace and member order normalized, or the text itself when it is none.
func canonicalJSON(text string) string {
	var v any
	if err := jsonx.Unmarshal([]byte(text), &v); err != nil {
		return text
	}
	b, err := jsonx.MarshalCanonical(v)
	if err != nil {
		return text
	}
	return string(b)
}

// canonicalArgs is a tool use's input as canonical arguments; a tool use with no input has an empty object.
func canonicalArgs(input map[string]any) string {
	if input == nil {
		return "{}"
	}
	b, err := jsonx.MarshalCanonical(input)
	if err != nil {
		panic(err) // decoded JSON always marshals
	}
	return string(b)
}

// clientToolCalls reads the calls of an answer in the client's format, whole or streamed, and why the model stopped.
func (w *callWorld) clientToolCalls(r reply, f toolFormat, streamed bool) (calls []fnCall, stop string) {
	w.t.Helper()
	if !streamed {
		calls, stop, err := f.whole(r.body)
		if err != nil {
			w.t.Fatalf("the answer is no answer: %v: %s", err, r.body)
		}
		return calls, stop
	}
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		w.t.Errorf("the streamed answer is %q, want an event stream", ct)
	}
	var s toolStream
	for line := range strings.Lines(string(r.body)) {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		if err := f.event(&s, []byte(data)); err != nil {
			w.t.Fatalf("an event is no JSON: %v: %s", err, data)
		}
	}
	return s.done(), s.stop
}

// toolStream gathers the calls of a stream by the index it gives each: the Chat call's or the Messages block's.
type toolStream struct {
	calls map[int]*fnCall
	order []int
	stop  string
}

func (s *toolStream) at(i int) *fnCall {
	if s.calls == nil {
		s.calls = map[int]*fnCall{}
	}
	if s.calls[i] == nil {
		s.calls[i] = &fnCall{}
		s.order = append(s.order, i)
	}
	return s.calls[i]
}

// done is the calls in the order they began, their arguments canonical; a tool use streamed with no input has an empty
// object.
func (s *toolStream) done() []fnCall {
	var calls []fnCall
	for _, i := range s.order {
		c := *s.calls[i]
		if c.args == "" {
			c.args = "{}"
		}
		c.args = canonicalJSON(c.args)
		calls = append(calls, c)
	}
	return calls
}
