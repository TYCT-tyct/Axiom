package protocol_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"axiom/internal/catalog"
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
	if len(events) != 3 || strings.Contains(all, "Weighing") || !strings.Contains(events[0], `{"text":"Checking."}`) ||
		!strings.Contains(events[1], `"functionCall":{"args":{"city":"Paris"},"id":"call_r1_0","name":"get_weather"},"thoughtSignature":"c2ln"`) ||
		strings.Count(all, `"finishReason":"STOP"`) != 1 || !strings.Contains(events[1], `"finishReason":"STOP"`) ||
		!strings.Contains(events[2], `"usageMetadata":{"cachedContentTokenCount":4,"candidatesTokenCount":5,"promptTokenCount":10,"thoughtsTokenCount":7,"totalTokenCount":22}`) {
		t.Errorf("Gemini events:\n%s", all)
	}
}

// geminiEvents is the Gemini events the reply makes of a Chat stream.
func geminiEvents(t *testing.T, reply *protocol.GeminiReply, chat []byte) []string {
	t.Helper()
	var events []string
	r := sse.NewReader(bytes.NewReader(chat), 1<<20)
	for data, err := r.Next(); err == nil; data, err = r.Next() {
		out, err := reply.Event(data)
		if err != nil {
			t.Fatal(err)
		}
		if out != nil {
			events = append(events, string(out))
		}
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
	body, err := protocol.NewGeminiReply(protocol.GeminiOptions{Thoughts: true}).Body(whole.status, whole.body)
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
