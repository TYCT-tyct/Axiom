package bedrock

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// canonical is the JSON text in RFC 8785 form, so two texts compare by value.
func canonical(t *testing.T, text string) string {
	t.Helper()
	b, err := jsonx.MarshalCanonical(jsonx.Raw(text))
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return string(b)
}

// TestChatBecomesConverse: a Chat body is sent to Converse, or ConverseStream for a stream, as Converse's body: the
// leading system messages the system prompt, text and inline images as blocks, the assistant's calls as tool uses and
// their results as the user's tool results, a role's messages in a row one turn, the sampling as the inference
// configuration and the functions as the tool configuration. What Converse cannot say is refused before anything is
// sent.
func TestChatBecomesConverse(t *testing.T) {
	t.Parallel()
	const base = "https://bedrock-runtime.us-east-1.amazonaws.com/model/amazon.nova-pro-v1%3A0/"
	for _, tc := range []struct {
		name, body, url, want string
	}{
		{
			"plain text", `{"model":"amazon.nova-pro-v1:0","messages":[{"role":"user","content":"Hi"}],"max_tokens":16,"temperature":0.5}`,
			base + "converse", `{"messages":[{"role":"user","content":[{"text":"Hi"}]}],"inferenceConfig":{"maxTokens":16,"temperature":0.5}}`,
		},
		{
			"a system prompt and a stream",
			`{"model":"m","stream":true,"stream_options":{"include_usage":true},"user":"u-1","messages":[{"role":"system","content":"Be brief."},
			{"role":"developer","content":[{"type":"text","text":"Use English."}]},{"role":"user","content":[{"type":"text","text":"Hi"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0K"}}]}],"max_completion_tokens":32,"max_tokens":8,"top_p":0.9,"stop":"END"}`,
			base + "converse-stream",
			`{"system":[{"text":"Be brief."},{"text":"Use English."}],"messages":[{"role":"user","content":[{"text":"Hi"},
			{"image":{"format":"png","source":{"bytes":"iVBORw0K"}}}]}],"inferenceConfig":{"maxTokens":32,"topP":0.9,"stopSequences":["END"]}}`,
		},
		{
			"tools",
			`{"model":"m","messages":[{"role":"user","content":"Weather?"},{"role":"assistant","content":"","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}},
			{"id":"call_2","type":"function","function":{"name":"time","arguments":""}}]},
			{"role":"tool","tool_call_id":"call_1","content":"Sunny"},{"role":"tool","tool_call_id":"call_2","content":[{"type":"text","text":"Noon"}]},
			{"role":"user","content":"Thanks"}],
			"tools":[{"type":"function","function":{"name":"weather","description":"The weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}},
			{"type":"function","function":{"name":"time"}}],"tool_choice":{"type":"function","function":{"name":"weather"}},"parallel_tool_calls":true,"stop":["a","b"]}`,
			base + "converse",
			`{"messages":[{"role":"user","content":[{"text":"Weather?"}]},{"role":"assistant","content":[
			{"toolUse":{"toolUseId":"call_1","name":"weather","input":{"city":"Paris"}}},{"toolUse":{"toolUseId":"call_2","name":"time","input":{}}}]},
			{"role":"user","content":[{"toolResult":{"toolUseId":"call_1","content":[{"text":"Sunny"}]}},{"toolResult":{"toolUseId":"call_2","content":[{"text":"Noon"}]}},
			{"text":"Thanks"}]}],"inferenceConfig":{"stopSequences":["a","b"]},"toolConfig":{"tools":[
			{"toolSpec":{"name":"weather","description":"The weather","inputSchema":{"json":{"type":"object","properties":{"city":{"type":"string"}}}}}},
			{"toolSpec":{"name":"time","inputSchema":{"json":{"type":"object","properties":{}}}}}],"toolChoice":{"tool":{"name":"weather"}}}}`,
		},
		{"an image by address", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`, "", ""},
		{"a system message mid-conversation", `{"messages":[{"role":"user","content":"Hi"},{"role":"system","content":"Now French."}]}`, "", ""},
		{"a parameter Converse has no word for", `{"messages":[{"role":"user","content":"Hi"}],"n":2}`, "", ""},
		{"a member of a message it has none for", `{"messages":[{"role":"user","name":"ann","content":"Hi"}]}`, "", ""},
		{"no tool call allowed", `{"messages":[{"role":"user","content":"Hi"}],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"none"}`, "", ""},
		{"a strict schema", `{"messages":[{"role":"user","content":"Hi"}],"tools":[{"type":"function","function":{"name":"f","strict":true}}]}`, "", ""},
		{"one call at a time", `{"messages":[{"role":"user","content":"Hi"}],"parallel_tool_calls":false}`, "", ""},
		{"an audio part", `{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]}`, "", ""},
	} {
		req, f := quirks{}.Sign(t.Context(), nil, adapter.SignedCall{
			Format: protocol.Chat, Region: "us-east-1", Model: "amazon.nova-pro-v1:0", Body: []byte(tc.body), Key: exampleAPIKey, At: time.Now(),
		})
		if tc.want == "" {
			if f == nil || f.Cause != (adapter.BadRequest{}) || f.Executed != adapter.NotExecuted {
				t.Errorf("%s: sent, or failed as %+v", tc.name, f)
			}
			continue
		}
		if f != nil {
			t.Fatalf("%s: %+v", tc.name, f)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil || req.URL.String() != tc.url || string(body) != canonical(t, tc.want) || req.Header.Get("Authorization") != "Bearer "+testAPIKey {
			t.Errorf("%s: sent %s %s (%v)", tc.name, req.URL, body, err)
		}
	}
}

// converseResponse is a 2xx answer of Bedrock's to a call at action, as the outbound client returns it.
func converseResponse(action, contentType string, body []byte) *http.Response {
	u := &url.URL{Scheme: "https", Host: "bedrock-runtime.us-east-1.amazonaws.com", Path: "/model/amazon.nova-pro-v1:0/" + action}
	return &http.Response{
		StatusCode: http.StatusOK, Request: &http.Request{Method: http.MethodPost, URL: u}, Body: io.NopCloser(bytes.NewReader(body)),
		Header: http.Header{"Content-Type": {contentType}, "X-Amzn-Requestid": {"req-1"}, "Date": {"Thu, 26 Nov 2026 08:00:00 GMT"}},
	}
}

// TestConverseAnswerBecomesChat: Converse's whole answer becomes Chat's, its text the content, its tool uses the
// calls, its stop reason the finish reason and its usage Chat's, whose prompt counts the cached tokens; InvokeModel's
// answer, already Anthropic's, is left as it is.
func TestConverseAnswerBecomesChat(t *testing.T) {
	t.Parallel()
	const head = `"id":"chatcmpl-req-1","object":"chat.completion","created":1795680000,"model":"amazon.nova-pro-v1:0"`
	for _, tc := range []struct {
		name, answer, want string
	}{
		{
			"text",
			`{"output":{"message":{"role":"assistant","content":[{"text":"Hel"},{"text":"lo"}]}},"stopReason":"end_turn",
			"usage":{"inputTokens":10,"outputTokens":2,"totalTokens":17,"cacheReadInputTokens":5},"metrics":{"latencyMs":300}}`,
			`{` + head + `,"choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":15,"completion_tokens":2,"total_tokens":17,"prompt_tokens_details":{"cached_tokens":5}}}`,
		},
		{
			"a tool use",
			`{"output":{"message":{"role":"assistant","content":[{"toolUse":{"toolUseId":"t1","name":"weather","input":{"city":"Paris"}}}]}},
			"stopReason":"tool_use","usage":{"inputTokens":10,"outputTokens":4,"totalTokens":14}}`,
			`{` + head + `,"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"t1","type":"function",
			"function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"prompt_tokens_details":{"cached_tokens":0}}}`,
		},
		{
			"the output cap",
			`{"output":{"message":{"role":"assistant","content":[{"text":"Lo"}]}},"stopReason":"max_tokens","usage":{"inputTokens":1,"outputTokens":1}}`,
			`{` + head + `,"choices":[{"index":0,"message":{"role":"assistant","content":"Lo"},"finish_reason":"length"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":0}}}`,
		},
	} {
		resp := quirks{}.Answer(converseResponse("converse", "application/json", []byte(tc.answer)))
		got, err := io.ReadAll(resp.Body)
		if err != nil || string(got) != canonical(t, tc.want) || resp.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s: %s (%v)", tc.name, got, err)
		}
	}
	invoked := `{"type":"message","content":[{"type":"text","text":"Hi"}]}`
	if got, err := io.ReadAll(quirks{}.Answer(converseResponse("invoke", "application/json", []byte(invoked))).Body); err != nil || string(got) != invoked {
		t.Errorf("InvokeModel's answer became %s (%v)", got, err)
	}
	if _, err := io.ReadAll(quirks{}.Answer(converseResponse("converse", "application/json", []byte(`{"output":`))).Body); err == nil {
		t.Error("an answer cut short read as one")
	}
}

// streamEvent is one of ConverseStream's events in Bedrock's framing.
func streamEvent(eventType, payload string) []byte {
	return frame(map[string]string{":message-type": "event", ":event-type": eventType, ":content-type": "application/json"}, []byte(payload))
}

// TestConverseStreamBecomesChatSSE: each of ConverseStream's events becomes the Chat chunk that says the same, read a
// byte at a time as from a slow connection: the role, the text, a tool call's start and its input, the finish reason,
// then the usage in a chunk of its own and [DONE]. An exception ends the stream with a chunk of Chat's error naming
// only its type; a stream that ends before its message stopped has no [DONE].
func TestConverseStreamBecomesChatSSE(t *testing.T) {
	t.Parallel()
	const head = `"id":"chatcmpl-req-1","object":"chat.completion.chunk","created":1795680000,"model":"amazon.nova-pro-v1:0"`
	opening := [][]byte{
		streamEvent("messageStart", `{"p":"abc","role":"assistant"}`),
		streamEvent("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"Hi"},"p":"abcdef"}`),
		streamEvent("contentBlockStop", `{"contentBlockIndex":0}`),
		streamEvent("contentBlockStart", `{"contentBlockIndex":1,"start":{"toolUse":{"toolUseId":"t1","name":"weather"}}}`),
		streamEvent("contentBlockDelta", `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"{\"city\":"}}}`),
	}
	chunks := []string{
		`{` + head + `,"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"weather","arguments":""}}]},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
	}
	closing := [][]byte{
		streamEvent("messageStop", `{"stopReason":"tool_use"}`),
		streamEvent("metadata", `{"usage":{"inputTokens":20,"outputTokens":5,"totalTokens":27,"cacheReadInputTokens":2},"metrics":{"latencyMs":90}}`),
	}
	ended := []string{
		`{` + head + `,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{` + head + `,"choices":[],"usage":{"prompt_tokens":22,"completion_tokens":5,"total_tokens":27,"prompt_tokens_details":{"cached_tokens":2}}}`,
	}
	exception := frame(map[string]string{":message-type": "exception", ":exception-type": "throttlingException"}, []byte(`{"message":"quote of the request"}`))
	for _, tc := range []struct {
		name   string
		stream [][]byte
		want   []string
	}{
		{"a whole stream", append(append([][]byte{}, opening...), closing...), append(append(append([]string{}, chunks...), ended...), "[DONE]")},
		{"an exception", append(append([][]byte{}, opening...), exception), append(append([]string{}, chunks...),
			`{"error":{"type":"overloaded_error","message":"Bedrock ended the stream: throttlingException"}}`)},
		{"cut before its stop", opening, chunks},
	} {
		resp := quirks{}.Answer(converseResponse("converse-stream", eventStream, bytes.Join(tc.stream, nil)))
		resp.Body = io.NopCloser(iotest.OneByteReader(resp.Body))
		got, err := io.ReadAll(resp.Body)
		events := strings.SplitAfter(string(got), "\n\n")
		if err != nil || len(events) != len(tc.want)+1 || events[len(tc.want)] != "" || resp.Header.Get("Content-Type") != "text/event-stream" {
			t.Errorf("%s: the SSE is %q (%v)", tc.name, got, err)
			continue
		}
		for i, w := range tc.want {
			data, ok := strings.CutPrefix(strings.TrimSuffix(events[i], "\n\n"), "data: ")
			if ok && data != "[DONE]" && w != "[DONE]" {
				data, w = canonical(t, data), canonical(t, w)
			}
			if !ok || data != w {
				t.Errorf("%s: event %d is %q, want %s", tc.name, i, events[i], w)
			}
		}
	}
}
