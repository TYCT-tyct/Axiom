package protocol_test

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/sse"
	"axiom/internal/protocol/internal/streamcheck"
)

func mustMarshal(t testing.TB, v any) []byte {
	t.Helper()
	b, err := jsonx.MarshalCanonical(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Independently written content goldens for the part-A samples. Stream grammar,
// byte boundaries, aggregation and billable items are checked separately.
var nativeChatContent = map[string][2]string{
	"chat-image": {
		`[{"type":"text","text":"A single pixel."}]`,
		`[{"id":"gen-01_0","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"A single pixel.","annotations":[]}]}]`,
	},
	"chat-reasoning-cache": {
		`[{"type":"thinking","thinking":"1071 = 2 x 462 + 147. Then 462 = 3 x 147 + 21."},{"type":"text","text":"The GCD is 21."}]`,
		`[{"id":"gen-01_0","type":"reasoning","summary":[{"type":"summary_text","text":"1071 = 2 x 462 + 147. Then 462 = 3 x 147 + 21."}]},{"id":"gen-01_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"The GCD is 21.","annotations":[]}]}]`,
	},
	"chat-tool-calls": {
		`[{"type":"text","text":"Let me check."},{"type":"tool_use","id":"call_00_Kq3vT9","name":"get_weather","input":{"location":"Paris"}},{"type":"tool_use","id":"call_01_Zb8mW2","name":"get_time","input":{}}]`,
		`[{"id":"gen-01_0","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Let me check.","annotations":[]}]},{"id":"gen-01_1","type":"function_call","status":"completed","call_id":"call_00_Kq3vT9","name":"get_weather","arguments":"{\"location\": \"Paris\"}"},{"id":"gen-01_2","type":"function_call","status":"completed","call_id":"call_01_Zb8mW2","name":"get_time","arguments":"{}"}]`,
	},
}

func TestChatToNativeSamples(t *testing.T) {
	for fi, format := range formats[:2] {
		for ci, c := range chatUpstreamCases {
			t.Run(format.dir+"/"+c.name, func(t *testing.T) { checkNativeChatSample(t, fi, ci) })
		}
	}
}

func TestChatResponsesLifecycleMembers(t *testing.T) {
	stream := sample(t, "assembled/chat-tool-calls.sse")
	got := relayTo(t, protocol.Responses, protocol.Chat, bytes.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
	if got.err != nil {
		t.Fatal(got.err)
	}
	r := sse.NewReader(bytes.NewReader(got.out), 32<<20)
	var names []string
	created := false
	for data, err := r.Next(); err == nil; data, err = r.Next() {
		switch member(t, data, "type") {
		case "response.created":
			response := member(t, data, "response").(map[string]any)
			usage, ok := response["usage"]
			if !ok || usage != nil {
				t.Fatalf("initial usage must be null: %s", data)
			}
			created = true
		case "response.function_call_arguments.done":
			names = append(names, member(t, data, "name").(string))
		}
	}
	if !created || strings.Join(names, ",") != "get_weather,get_time" {
		t.Fatalf("created=%v, completed functions=%v", created, names)
	}
}

func checkNativeChatSample(t *testing.T, formatIndex, sampleIndex int) {
	t.Helper()
	format, c := formats[formatIndex], chatUpstreamCases[sampleIndex]
	stream := sample(t, "assembled/"+c.name+".sse")
	got := relayTo(t, format.f, protocol.Chat, bytes.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
	cut := relayTo(t, format.f, protocol.Chat, iotest.OneByteReader(bytes.NewReader(stream)), protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
	if got.err != nil || cut.err != nil || !bytes.Equal(got.out, cut.out) {
		t.Fatalf("whole %v, bytewise %v\n%s\n%s", got.err, cut.err, got.out, cut.out)
	}
	if err := format.check(bytes.NewReader(got.out)); err != nil {
		t.Fatalf("client grammar: %v\n%s", err, got.out)
	}
	whole := aggregate(t, format.f, protocol.Chat, bytes.NewReader(stream), protocol.NewMemory(64<<20), unsettled)
	bytewise := aggregate(t, format.f, protocol.Chat, iotest.OneByteReader(bytes.NewReader(stream)), protocol.NewMemory(64<<20), unsettled)
	if whole.err != nil || whole.status != http.StatusOK || !bytes.Equal(whole.body, bytewise.body) {
		t.Fatalf("aggregate: %v status %d\n%s\n%s", whole.err, whole.status, whole.body, bytewise.body)
	}
	checkBilledAs(t, "stream", got.r.Usage(), c.want)
	checkBilledAs(t, "aggregate", whole.r.Usage(), c.want)
	index, key := 0, "content"
	if format.f == protocol.Responses {
		index, key = 1, "output"
	}
	value := mustMarshal(t, member(t, whole.body, key))
	sameJSON(t, nativeChatContent[c.name][index], value)
	if c.name == "chat-image" {
		usage := member(t, whole.body, "usage").(map[string]any)
		if usage["input_tokens"] != float64(90) {
			t.Fatalf("image tokens lost from client usage: %s", whole.body)
		}
	}
}

func chatPiece(delta, finish string) string {
	return ev(`{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}`)
}

func TestChatParallelToolsAreStreamedWithoutMixingArguments(t *testing.T) {
	first := chatPiece(`{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"f","arguments":"{\"x\":"}}]}`, "null")
	second := chatPiece(`{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"g","arguments":"{\"y\":"}}]}`, "null")
	finish := chatPiece(`{"tool_calls":[{"index":0,"function":{"arguments":"1}"}},{"index":1,"function":{"arguments":"2}"}}]}`, `"tool_calls"`) + ev("[DONE]")
	stream := []byte(first + second + finish)
	for fi := range formats[:2] {
		t.Run(formats[fi].dir, func(t *testing.T) {
			checkFirstArgumentsStream(t, fi, stream)
			checkParallelToolsAggregate(t, fi, stream)
		})
	}
}

// checkFirstArgumentsStream holds a tool call's arguments to the first committed step: waiting for its block to close
// would keep the client from seeing the call's progress at all.
func checkFirstArgumentsStream(t *testing.T, fi int, stream []byte) {
	t.Helper()
	format := formats[fi]
	o := options
	o.Memory = protocol.NewMemory(1 << 20)
	r := protocol.NewRelay(format.f, protocol.Chat, strings.NewReader(string(stream)), protocol.UsageContext{}, o)
	defer r.Release()
	step, err := r.Next()
	if err != nil || step.Kind != protocol.StepCommit || !bytes.Contains(step.Out, []byte(`{\"x\":`)) {
		t.Fatalf("first argument was not streamed: %v %+v", err, step)
	}
	got := relayTo(t, format.f, protocol.Chat, bytes.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(1<<20), settled)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if err := format.check(bytes.NewReader(got.out)); err != nil {
		t.Fatal(err)
	}
}

// checkParallelToolsAggregate holds two calls apart in the aggregated reply: each keeps its own arguments.
func checkParallelToolsAggregate(t *testing.T, fi int, stream []byte) {
	t.Helper()
	format := formats[fi]
	whole := aggregate(t, format.f, protocol.Chat, bytes.NewReader(stream), protocol.NewMemory(1<<20), unsettled)
	if whole.err != nil || whole.status != http.StatusOK {
		t.Fatalf("%v: %s", whole.err, whole.body)
	}
	key, want := "content", `[{"type":"tool_use","id":"a","name":"f","input":{"x":1}},{"type":"tool_use","id":"b","name":"g","input":{"y":2}}]`
	if format.f == protocol.Responses {
		key, want = "output", `[{"id":"gen-01_0","type":"function_call","status":"completed","call_id":"a","name":"f","arguments":"{\"x\":1}"},{"id":"gen-01_1","type":"function_call","status":"completed","call_id":"b","name":"g","arguments":"{\"y\":2}"}]`
	}
	sameJSON(t, want, mustMarshal(t, member(t, whole.body, key)))
}

func TestChatToNativeStopReasons(t *testing.T) {
	for fi := range formats[:2] {
		for _, reason := range []string{"stop", "length", "content_filter", "tool_calls"} {
			t.Run(formats[fi].dir+"/"+reason, func(t *testing.T) { checkStopReason(t, fi, reason) })
		}
	}
}

func checkStopReason(t *testing.T, fi int, reason string) {
	t.Helper()
	format := formats[fi]
	stream := chatPiece(`{"content":"hi"}`, `"`+reason+`"`) + ev("[DONE]")
	got := relayTo(t, format.f, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(1<<20), settled)
	if got.err != nil || format.check(bytes.NewReader(got.out)) != nil {
		t.Fatalf("%v\n%s", got.err, got.out)
	}
	if want := stopMarker(format.f, reason); !bytes.Contains(got.out, []byte(want)) {
		t.Fatalf("missing %s: %s", want, got.out)
	}
}

// stopMarker is what the client's format says about a Chat finish reason: Messages' stop reason, or the Responses event
// that carries the end.
func stopMarker(f protocol.Format, reason string) string {
	stopReasons := map[string]string{"stop": "end_turn", "length": "max_tokens", "content_filter": "refusal", "tool_calls": "tool_use"}
	if f != protocol.Responses {
		return stopReasons[reason]
	}
	if reason == "length" || reason == "content_filter" {
		return "response.incomplete"
	}
	return "response.completed"
}

func TestChatConversionFailureBeforeClientPrelude(t *testing.T) {
	stream := chatPiece(`{"tool_calls":[{"index":3,"function":{"arguments":"{}"}}]}`, `"tool_calls"`) + ev("[DONE]")
	for _, format := range formats[:2] {
		got := relayTo(t, format.f, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(1<<20), settled)
		if !errors.Is(got.err, protocol.ErrUpstream) {
			t.Fatalf("invalid first tool accepted: %v", got.err)
		}
		if err := format.check(bytes.NewReader(got.out)); err != nil {
			t.Fatalf("failure without a prelude: %v\n%s", err, got.out)
		}
		if bytes.Contains(got.out, []byte("response.failed")) || bytes.Contains(got.out, []byte("response.created")) {
			t.Fatalf("an unpublished Responses prelude was assumed: %s", got.out)
		}
	}
}

func TestChatToMessagesRejectsInvalidToolInput(t *testing.T) {
	stream := chatPiece(`{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"f","arguments":"{\"x\":"}}]}`, `"length"`) + ev("[DONE]")
	got := relayTo(t, protocol.Messages, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(1<<20), settled)
	if protocol.FaultOf(got.err) != protocol.FaultToolInput {
		t.Fatalf("invalid generated arguments were not classified as tool input: %v", got.err)
	}
	if bytes.Contains(got.out, []byte("message_stop")) {
		t.Fatalf("invalid tool input succeeded: %s", got.out)
	}
}

func TestChatToNativeFailures(t *testing.T) {
	for _, format := range formats[:2] {
		for _, suffix := range []string{
			"", ev(`{"error":{"type":"server_error","message":"secret"}}`),
			chatPiece(`{"content":"late"}`, `"unknown"`) + ev("[DONE]"),
			chatPiece(`{"tool_calls":[{"index":3,"function":{"arguments":"{}"}}]}`, `"tool_calls"`) + ev("[DONE]"),
		} {
			stream := chatPiece(`{"content":"hi"}`, "null") + suffix
			got := relayTo(t, format.f, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(1<<20), settled)
			if !errors.Is(got.err, protocol.ErrUpstream) {
				t.Fatalf("expected failed stream, got %v\n%s", got.err, got.out)
			}
			if err := format.check(bytes.NewReader(got.out)); err != nil {
				t.Fatalf("failure shape: %v\n%s", err, got.out)
			}
			if bytes.Contains(got.out, []byte(`"secret"`)) || bytes.Contains(got.out, []byte("message_stop")) || bytes.Contains(got.out, []byte("response.completed")) {
				t.Fatalf("upstream error leaked or success emitted: %s", got.out)
			}
		}
	}
}

func FuzzChatToNative(f *testing.F) {
	for _, c := range chatUpstreamCases {
		for which := range uint8(2) {
			f.Add(which, sample(f, "assembled/"+c.name+".sse"), []byte{1, 7, 255})
		}
	}
	f.Fuzz(func(t *testing.T, which uint8, stream, cuts []byte) {
		if len(stream) > 1<<20 || streamcheck.Chat(bytes.NewReader(stream)) != nil {
			return
		}
		format := formats[int(which)%2]
		a := relayTo(t, format.f, protocol.Chat, bytes.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
		b := relayTo(t, format.f, protocol.Chat, &chunks{stream, cuts}, protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
		if !bytes.Equal(a.out, b.out) || (a.err == nil) != (b.err == nil) {
			t.Fatalf("chunk boundaries change conversion:\n%s\n%s", a.out, b.out)
		}
		if a.err == nil {
			if err := format.check(bytes.NewReader(a.out)); err != nil {
				t.Fatalf("%v\n%s", err, a.out)
			}
		}
	})
}

func TestChatConversionCannotOutgrowTheNodeBudget(t *testing.T) {
	stream := chatPiece(`{"content":"`+strings.Repeat("x", 100000)+`"}`, `"stop"`) + ev("[DONE]")
	for _, format := range formats[:2] {
		mem := protocol.NewMemory(0)
		got := relayTo(t, format.f, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{}, mem, settled)
		if !errors.Is(got.err, protocol.ErrMemory) || mem.Left() != 0 {
			t.Fatalf("%s: error %v, budget %d", format.dir, got.err, mem.Left())
		}
	}
}

func TestChatConversionUsesOneReasoningCopyAndWaitsForSettlement(t *testing.T) {
	stream := chatPiece(`{"reasoning":"why","reasoning_content":"why","reasoning_details":[ ]}`, `"stop"`) + ev("[DONE]")
	for _, format := range formats[:2] {
		got := relayTo(t, format.f, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(1<<20),
			func(u protocol.Usage) protocol.Final { return protocol.Final{Usage: u, Pending: true} })
		if got.err != nil {
			t.Fatal(got.err)
		}
		if bytes.Contains(got.out, []byte("whywhy")) || !bytes.Contains(got.out, []byte(`"billing_pending":true`)) ||
			bytes.Contains(got.out, []byte(`"cost"`)) {
			t.Fatalf("reasoning or unsettled ending: %s", got.out)
		}
		if format.f == protocol.Messages && bytes.Count(got.out, []byte(`"thinking":"why"`)) != 1 {
			t.Fatalf("reasoning was not emitted exactly once: %s", got.out)
		}
	}
}

func BenchmarkChatToNative(b *testing.B) {
	stream := sample(b, "assembled/chat-tool-calls.sse")
	for fi := range formats[:2] {
		b.Run(formats[fi].dir, func(b *testing.B) { benchChatToNative(b, formats[fi].f, stream) })
	}
}

func benchChatToNative(b *testing.B, f protocol.Format, stream []byte) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(len(stream)))
	for b.Loop() {
		o := options
		o.Memory = protocol.NewMemory(64 << 20)
		r := protocol.NewRelay(f, protocol.Chat, bytes.NewReader(stream), protocol.UsageContext{}, o)
		for {
			step, err := r.Next()
			if err != nil {
				b.Fatal(err)
			}
			if step.Kind == protocol.StepUpstreamDone {
				r.Finish(settled(r.Usage()))
				break
			}
		}
	}
}
