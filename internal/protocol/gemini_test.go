package protocol_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/sse"
)

// A Gemini client's request becomes the Chat request the call runs as; what Chat cannot say is refused by name.
func TestGeminiToChat(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, body, want string
		thoughts         bool
	}{
		{
			"system, text and settings",
			`{"systemInstruction":{"parts":[{"text":"Rules"}]},"contents":[{"role":"user","parts":[{"text":"Hi"}]}],` +
				`"generationConfig":{"temperature":0.5,"topP":0.9,"topK":40,"maxOutputTokens":100,"stopSequences":["END"],"candidateCount":1}}`,
			`{"model":"m","stream":false,"service_tier":"default","messages":[{"role":"system","content":[{"type":"text","text":"Rules"}]},{"role":"user","content":[{"type":"text","text":"Hi"}]}],` +
				`"temperature":0.5,"top_p":0.9,"top_k":40,"max_tokens":100,"stop":["END"]}`, false,
		},
		{
			"calls without IDs pair with their responses by name and order, signatures go on the call",
			`{"contents":[{"role":"user","parts":[{"text":"Go"}]},{"role":"model","parts":[{"functionCall":{"name":"f","args":{"x":1}},"thoughtSignature":"c2ln"},{"functionCall":{"name":"f"}}]},` +
				`{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"ok":1}}},{"functionResponse":{"name":"f","response":{"ok":2}}}]}]}`,
			`{"model":"m","stream":false,"service_tier":"default","messages":[{"role":"user","content":[{"type":"text","text":"Go"}]},` +
				`{"role":"assistant","content":"","tool_calls":[{"id":"call_1_0","type":"function","function":{"name":"f","arguments":"{\"x\":1}"},"extra_content":{"google":{"thought_signature":"c2ln"}}},{"id":"call_1_1","type":"function","function":{"name":"f","arguments":"{}"}}]},` +
				`{"role":"tool","tool_call_id":"call_1_0","content":"{\"ok\":1}"},{"role":"tool","tool_call_id":"call_1_1","content":"{\"ok\":2}"}]}`, false,
		},
		{
			"tools in Gemini's schema, one forced, JSON out, thinking shown",
			`{"contents":[{"parts":[{"text":"Hi"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}],` +
				`"tools":[{"functionDeclarations":[{"name":"f","description":"F","parameters":{"type":"OBJECT","properties":{"x":{"type":"STRING","nullable":true}}}},{"name":"g"}]}],` +
				`"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["f"]}},` +
				`"generationConfig":{"responseMimeType":"application/json","responseJsonSchema":{"type":"object"},"thinkingConfig":{"includeThoughts":true,"thinkingBudget":2048}}}`,
			`{"model":"m","stream":true,"service_tier":"default","stream_options":{"include_usage":true},"messages":[{"role":"user","content":[{"type":"text","text":"Hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}],` +
				`"tools":[{"type":"function","function":{"name":"f","description":"F","parameters":{"type":"object","properties":{"x":{"type":["string","null"]}}}}},{"type":"function","function":{"name":"g","parameters":{"type":"object","properties":{}}}}],` +
				`"tool_choice":{"type":"function","function":{"name":"f"}},"response_format":{"type":"json_schema","json_schema":{"name":"response","schema":{"type":"object"}}},"reasoning":{"max_tokens":2048}}`, true,
		},
		{
			"thinking off",
			`{"contents":[{"role":"user","parts":[{"text":"Hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":0}},"safetySettings":[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_NONE"}]}`,
			`{"model":"m","stream":false,"service_tier":"default","messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],"reasoning_effort":"none"}`, false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, opts, err := protocol.GeminiToChat([]byte(c.body), "m", strings.Contains(c.want, `"stream":true`))
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, c.want, got)
			if opts.Thoughts != c.thoughts {
				t.Errorf("thoughts %v, want %v", opts.Thoughts, c.thoughts)
			}
			if _, err := protocol.Inspect(protocol.Chat, got); err != nil {
				t.Errorf("the Chat request is not one a call takes: %v", err)
			}
		})
	}
	for name, body := range map[string]string{
		"cachedContent":               `{"contents":[],"cachedContent":"cachedContents/x"}`,
		"googleSearch":                `{"contents":[],"tools":[{"googleSearch":{}}]}`,
		"candidateCount":              `{"contents":[],"generationConfig":{"candidateCount":2}}`,
		"answers no function call":    `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"f","response":{}}}]}]}`,
		"audioTimestamp":              `{"contents":[],"generationConfig":{"audioTimestamp":true}}`,
		"inline data of type":         `{"contents":[{"parts":[{"inlineData":{"mimeType":"video/mp4","data":"AA"}}]}]}`,
		"is not user or model":        `{"contents":[{"role":"system","parts":[{"text":"x"}]}]}`,
		"executableCode":              `{"contents":[{"role":"model","parts":[{"executableCode":{"code":"1"}}]}]}`,
		"responseModalities":          `{"contents":[],"generationConfig":{"responseModalities":["IMAGE"]}}`,
		"function calling mode \"X\"": `{"contents":[],"toolConfig":{"functionCallingConfig":{"mode":"X"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, err := protocol.GeminiToChat([]byte(body), "m", false)
			if !errors.Is(err, protocol.ErrInvalid) || !strings.Contains(err.Error(), name) {
				t.Fatalf("want ErrInvalid naming %s, got %v", name, err)
			}
		})
	}
}

// ChatToGemini is GeminiToChat the other way: a request converted to Chat and back is the request it was.
func TestChatToGeminiUndoesGeminiToChat(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"systemInstruction":{"parts":[{"text":"Rules"}]},"contents":[{"role":"user","parts":[{"text":"Hi"},{"inlineData":{"mimeType":"application/pdf","data":"AAAA"}}]},` +
			`{"role":"model","parts":[{"text":"Let me look."},{"functionCall":{"id":"a","name":"f","args":{"x":1}},"thoughtSignature":"c2ln"},{"functionCall":{"id":"b","name":"g","args":{}}}]},` +
			`{"role":"user","parts":[{"functionResponse":{"id":"a","name":"f","response":{"ok":1}}},{"functionResponse":{"id":"b","name":"g","response":{"result":"done"}}},{"text":"next"}]}],` +
			`"tools":[{"functionDeclarations":[{"name":"f","description":"F","parametersJsonSchema":{"type":"object","properties":{"x":{"type":"integer"}}}},{"name":"g","parametersJsonSchema":{"type":"object"}}]}],` +
			`"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["f"]}},` +
			`"generationConfig":{"temperature":0.5,"topP":0.9,"topK":40,"maxOutputTokens":100,"stopSequences":["END"],"seed":7,` +
			`"responseMimeType":"application/json","responseJsonSchema":{"type":"object"},"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"high"}}}`,
		`{"contents":[{"role":"user","parts":[{"text":"Hi"}]}],"toolConfig":{"functionCallingConfig":{"mode":"NONE"}},"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`,
	} {
		chat, _, err := protocol.GeminiToChat([]byte(body), "m", true)
		if err != nil {
			t.Fatal(err)
		}
		back, err := protocol.ChatToGemini(chat)
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, body, back)
	}
}

// What a Gemini endpoint cannot be sent is refused, so the call goes to another; a call made on another model gets
// the signature Google documents for one whose own is lost.
func TestChatToGemini(t *testing.T) {
	t.Parallel()
	got, err := protocol.ChatToGemini([]byte(`{"model":"m","messages":[{"role":"user","content":"Go"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"plain"}],` +
		`"stream":true,"stream_options":{"include_usage":true},"user":"u","parallel_tool_calls":false,"max_completion_tokens":50,"reasoning":{"effort":"low","exclude":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, `{"contents":[{"role":"user","parts":[{"text":"Go"}]},{"role":"model","parts":[{"functionCall":{"id":"c1","name":"f","args":{}},"thoughtSignature":"skip_thought_signature_validator"}]},`+
		`{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"f","response":{"result":"plain"}}}]}],"generationConfig":{"maxOutputTokens":50,"thinkingConfig":{"thinkingLevel":"low"}}}`, got)
	for _, body := range []string{
		`{"model":"m","messages":[{"role":"user","content":"Hi"},{"role":"system","content":"late"}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/i.png"}}]}]}`,
		`{"model":"m","messages":[{"role":"user","content":"Hi"}],"logprobs":true}`,
		`{"model":"m","messages":[{"role":"user","content":"Hi"}],"tools":[{"type":"custom","custom":{"name":"x"}}]}`,
	} {
		if _, err := protocol.ChatToGemini([]byte(body)); err == nil {
			t.Errorf("converted %s", body)
		}
	}
}

// geminiStream is a streamGenerateContent?alt=sse reply that thinks, says something and calls a function with a
// signature, with Gemini's cumulative usage on the last event.
const geminiStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Weighing it.","thought":true}]}}],"modelVersion":"gemini-3-pro","responseId":"r1"}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Checking."}]}}],"modelVersion":"gemini-3-pro","responseId":"r1"}

data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}},"thoughtSignature":"c2ln"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":7,"cachedContentTokenCount":4,"totalTokenCount":22},"modelVersion":"gemini-3-pro","responseId":"r1"}

`

// A Gemini stream reads as a Chat endpoint's: the relay bills Gemini's usage, and a Chat client gets the call with its
// signature where Google's own Chat endpoint puts it.
func TestChatFromGemini(t *testing.T) {
	t.Parallel()
	got := relayTo(t, protocol.Chat, protocol.Chat, protocol.ChatFromGemini(io.NopCloser(strings.NewReader(geminiStream))),
		protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
	if got.err != nil {
		t.Fatal(got.err)
	}
	checkBilledAs(t, "gemini", got.r.Usage(), map[catalog.Item]int64{catalog.ItemInput: 6, catalog.ItemCacheRead: 4, catalog.ItemOutput: 5, catalog.ItemReasoning: 7})
	out := string(got.out)
	for _, want := range []string{`"reasoning_content":"Weighing it."`, `"content":"Checking."`, `"name":"get_weather"`, `"thought_signature":"c2ln"`, `"finish_reason":"tool_calls"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the Chat stream lacks %s:\n%s", want, out)
		}
	}
	for name, stream := range map[string]string{
		"cut off":      strings.SplitAfter(geminiStream, "\n\n")[0],
		"an error":     `data: {"error":{"code":503,"message":"overloaded","status":"UNAVAILABLE"}}` + "\n\n",
		"other reason": `data: {"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}` + "\n\n",
	} {
		got := relayTo(t, protocol.Chat, protocol.Chat, protocol.ChatFromGemini(io.NopCloser(strings.NewReader(stream))),
			protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
		if !errors.Is(got.err, protocol.ErrUpstream) {
			t.Errorf("%s: %v, want an upstream failure", name, got.err)
		}
	}
}

// What a Gemini stream reported is billed, by modality, also when the stream then fails: audio and image tokens at
// their own price, and the cumulative usage of a stream that broke off or stopped for a reason Chat has no word for.
func TestChatFromGeminiBillsWhatItReported(t *testing.T) {
	t.Parallel()
	const usage = `"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"totalTokenCount":13}`
	for _, c := range []struct {
		name, stream string
		failed       bool
		want         map[catalog.Item]int64
		reset        bool // the connection is reset after the stream's bytes
	}{
		{
			"audio and image tokens",
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Heard."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":5,"totalTokenCount":14,` +
				`"promptTokensDetails":[{"modality":"TEXT","tokenCount":4},{"modality":"AUDIO","tokenCount":3},{"modality":"IMAGE","tokenCount":2}],` +
				`"candidatesTokensDetails":[{"modality":"TEXT","tokenCount":3},{"modality":"AUDIO","tokenCount":2}]}}` + "\n\n",
			false,
			map[catalog.Item]int64{catalog.ItemInput: 4, catalog.ItemAudioIn: 3, catalog.ItemImageIn: 2, catalog.ItemOutput: 3, catalog.ItemAudioOut: 2},
			false,
		},
		{
			"cut off after reporting",
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Half"}]}}],` + usage + `}` + "\n\n",
			true,
			map[catalog.Item]int64{catalog.ItemInput: 10, catalog.ItemOutput: 3},
			false,
		},
		{
			"reset after reporting",
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Half"}]}}],` + usage + `}` + "\n\n",
			true,
			map[catalog.Item]int64{catalog.ItemInput: 10, catalog.ItemOutput: 3},
			true,
		},
		{
			"a malformed call after reporting",
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Half"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],` + usage + `}` + "\n\n",
			true,
			map[catalog.Item]int64{catalog.ItemInput: 10, catalog.ItemOutput: 3},
			false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var src io.Reader = strings.NewReader(c.stream)
			if c.reset {
				src = io.MultiReader(src, iotest.ErrReader(syscall.ECONNRESET))
			}
			got := relayTo(t, protocol.Chat, protocol.Chat, protocol.ChatFromGemini(io.NopCloser(src)),
				protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
			if failed := errors.Is(got.err, protocol.ErrUpstream); failed != c.failed {
				t.Fatalf("error %v, want failed %v", got.err, c.failed)
			}
			checkBilledAs(t, c.name, got.r.Usage(), c.want)
		})
	}
}

// A Chat stream reaches a Gemini client as Gemini's: the thoughts only when asked for, each call whole at the finish with
// its signature, and Gemini's usage.
func TestGeminiReply(t *testing.T) {
	t.Parallel()
	chat := relayTo(t, protocol.Chat, protocol.Chat, protocol.ChatFromGemini(io.NopCloser(strings.NewReader(geminiStream))),
		protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
	if chat.err != nil {
		t.Fatal(chat.err)
	}
	events := geminiEvents(t, protocol.NewGeminiReply(protocol.GeminiOptions{Thoughts: false}), chat.out)
	all := strings.Join(events, "\n")
	// The last event is the candidate's finish with the usage, as Google's own stream ends: a client that keeps the
	// chat's history (the Google SDKs) refuses a turn whose last event has no candidate.
	last := events[len(events)-1]
	if len(events) != 1 || strings.Contains(all, "Weighing") || !strings.Contains(last, `{"text":"Checking."}`) ||
		!strings.Contains(last, `"functionCall":{"args":{"city":"Paris"},"id":"call_r1_0","name":"get_weather"},"thoughtSignature":"c2ln"`) ||
		strings.Count(all, `"finishReason":"STOP"`) != 1 || !strings.Contains(last, `"finishReason":"STOP"`) ||
		!strings.Contains(last, `"usageMetadata":{"cachedContentTokenCount":4,"candidatesTokenCount":5,"promptTokenCount":10,"thoughtsTokenCount":7,"totalTokenCount":22}`) {
		t.Errorf("Gemini events:\n%s", all)
	}
}

// Every candidate a Gemini client is streamed holds parts, the last one with its finish and the usage, as Google's own
// stream does: the Google SDKs refuse a turn with a candidate that has none, so the text of a reply without calls has
// its last piece held for the finishing event.
func TestGeminiReplyEndsTextWithParts(t *testing.T) {
	t.Parallel()
	chat := relayTo(t, protocol.Chat, protocol.Chat, bytes.NewReader(sample(t, "chat/text/upstream.sse")), protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
	if chat.err != nil {
		t.Fatal(chat.err)
	}
	events := geminiEvents(t, protocol.NewGeminiReply(protocol.GeminiOptions{}), chat.out)
	var text strings.Builder
	for i, e := range events {
		var r struct {
			Candidates []struct {
				Content *struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			Usage *struct{} `json:"usageMetadata"`
		}
		if err := jsonx.Unmarshal([]byte(e), &r); err != nil || len(r.Candidates) != 1 || r.Candidates[0].Content == nil || len(r.Candidates[0].Content.Parts) == 0 {
			t.Fatalf("event %d has no candidate with parts (%v): %s", i, err, e)
		}
		for _, p := range r.Candidates[0].Content.Parts {
			text.WriteString(p.Text)
		}
		if last := i == len(events)-1; last != (r.Candidates[0].FinishReason == "STOP") || last != (r.Usage != nil) {
			t.Errorf("event %d of %d: finish %q, usage %v: %s", i, len(events), r.Candidates[0].FinishReason, r.Usage != nil, e)
		}
	}
	if text.String() != "Hello" {
		t.Errorf("the events say %q, want the sample's Hello", text.String())
	}
}

// geminiEvents is the data of the Gemini events the reply makes of a Chat stream.
func geminiEvents(t *testing.T, reply *protocol.GeminiReply, chat []byte) []string {
	t.Helper()
	var stream []byte
	r := sse.NewReader(bytes.NewReader(chat), 1<<20)
	for data, err := r.Next(); err == nil; data, err = r.Next() {
		out, err := reply.Event(data)
		if err != nil {
			t.Fatal(err)
		}
		stream = append(stream, out...)
	}
	var events []string
	r = sse.NewReader(bytes.NewReader(stream), 1<<20)
	for data, err := r.Next(); err == nil; data, err = r.Next() {
		events = append(events, string(data))
	}
	return events
}

// TestGeminiReplyBody: a whole Chat reply, and a refusal, become Gemini's.
func TestGeminiReplyBody(t *testing.T) {
	t.Parallel()
	whole := aggregate(t, protocol.Chat, protocol.Chat, protocol.ChatFromGemini(io.NopCloser(strings.NewReader(geminiStream))), protocol.NewMemory(64<<20), unsettled)
	if whole.err != nil {
		t.Fatal(whole.err)
	}
	_, body, err := protocol.NewGeminiReply(protocol.GeminiOptions{Thoughts: true}).Body(whole.status, whole.body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`{"text":"Weighing it.","thought":true}`, `{"text":"Checking."}`, `"thoughtSignature":"c2ln"`, `"finishReason":"STOP"`, `"promptTokenCount":10`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the whole reply lacks %s:\n%s", want, body)
		}
	}
	sameJSON(t, `{"error":{"code":429,"message":"Slow down.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"rate_limited","domain":"axiom"}]}}`,
		protocol.GeminiErrorBody(429, []byte(`{"error":{"message":"Slow down.","type":"rate_limit_error","param":"","code":429,"metadata":{"error_type":"rate_limited"}}}`)))
}

// geminiCutOff is a Gemini stream that broke off after some text and its usage.
const geminiCutOff = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Half"}]}}],` +
	`"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"totalTokenCount":13}}` + "\n\n"

// A reply that fails after its first content reaches a Gemini client with what it was charged for: a whole reply, which
// Chat answers 200 with the content and the error, is a 502 with the error and the usage, as Messages answers (Gemini
// has no answer holding both); a stream gives the text it held back and the usage before the error that ends it.
func TestGeminiReplyFailsAfterContent(t *testing.T) {
	t.Parallel()
	const usage = `"usageMetadata":{"candidatesTokenCount":3,"promptTokenCount":10,"totalTokenCount":13}` // as written, in key order
	whole := aggregate(t, protocol.Chat, protocol.Chat, protocol.ChatFromGemini(io.NopCloser(strings.NewReader(geminiCutOff))), protocol.NewMemory(64<<20), unsettled)
	if whole.status != http.StatusOK {
		t.Fatalf("Chat answered %d: %s", whole.status, whole.body)
	}
	status, body, err := protocol.NewGeminiReply(protocol.GeminiOptions{}).Body(whole.status, whole.body)
	if err != nil || status != http.StatusBadGateway || !strings.Contains(string(body), `"code":502`) || !strings.Contains(string(body), usage) {
		t.Errorf("the whole reply is %d (%v): %s", status, err, body)
	}
	chat := relayTo(t, protocol.Chat, protocol.Chat, protocol.ChatFromGemini(io.NopCloser(strings.NewReader(geminiCutOff))),
		protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
	events := geminiEvents(t, protocol.NewGeminiReply(protocol.GeminiOptions{}), chat.out)
	if n := len(events); n < 2 || !strings.Contains(events[n-2], `{"text":"Half"}`) || !strings.Contains(events[n-2], usage) ||
		!strings.HasPrefix(events[n-1], `{"error":`) {
		t.Errorf("Gemini events:\n%s", strings.Join(events, "\n"))
	}
}
