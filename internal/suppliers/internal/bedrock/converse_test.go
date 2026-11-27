package bedrock

import (
	"bytes"
	"errors"
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
// configuration and the functions as the tool configuration. A member that changes no answer, or holds Chat's default,
// is dropped, and a name goes before the message's text. What Converse cannot say is refused before anything is sent, as
// the endpoint's failure.
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
		{
			"the members that cannot change the answer, and the defaults",
			`{"messages":[{"role":"user","content":"Hi"}],"seed":null,"store":true,"metadata":{"k":"v"},"top_logprobs":2,"n":1,"logprobs":false,
			"frequency_penalty":0,"presence_penalty":0.0,"logit_bias":{ },"modalities":["text"],"service_tier":"auto","response_format":{"type":"text"},
			"reasoning_effort":"","parallel_tool_calls":false}`,
			base + "converse", `{"messages":[{"role":"user","content":[{"text":"Hi"}]}]}`,
		},
		{
			"an earlier turn's reasoning, which is not sent back",
			`{"messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hello","reasoning_content":"Greet back."},{"role":"user","content":"Again"}]}`,
			base + "converse",
			`{"messages":[{"role":"user","content":[{"text":"Hi"}]},{"role":"assistant","content":[{"text":"Hello"}]},{"role":"user","content":[{"text":"Again"}]}]}`,
		},
		{
			"names",
			`{"messages":[{"role":"system","name":"rules","content":"Be brief."},{"role":"user","name":"ann","content":[{"type":"text","text":"Hi"}]},
			{"role":"assistant","name":"bot","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","name":"f","tool_call_id":"c1","content":"ok"}]}`,
			base + "converse",
			`{"system":[{"text":"rules: Be brief."}],"messages":[{"role":"user","content":[{"text":"ann: Hi"}]},{"role":"assistant","content":[{"text":"bot: "},
			{"toolUse":{"toolUseId":"c1","name":"f","input":{}}}]},{"role":"user","content":[{"toolResult":{"toolUseId":"c1","content":[{"text":"ok"}]}}]}]}`,
		},
		{"an image by address", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`, "", ""},
		{"a system message mid-conversation", `{"messages":[{"role":"user","content":"Hi"},{"role":"system","content":"Now French."}]}`, "", ""},
		{"several choices", `{"messages":[{"role":"user","content":"Hi"}],"n":2}`, "", ""},
		{"token probabilities", `{"messages":[{"role":"user","content":"Hi"}],"logprobs":true}`, "", ""},
		{"a penalty", `{"messages":[{"role":"user","content":"Hi"}],"frequency_penalty":0.5}`, "", ""},
		{"a presence penalty", `{"messages":[{"role":"user","content":"Hi"}],"presence_penalty":-1}`, "", ""},
		{"a seed", `{"messages":[{"role":"user","content":"Hi"}],"seed":7}`, "", ""},
		{"a low-detail image", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0K","detail":"low"}}]}]}`, "", ""},
		{"an effort the model has no word for", `{"messages":[{"role":"user","content":"Hi"}],"reasoning_effort":"none"}`, "", ""},
		{"a JSON reply", `{"messages":[{"role":"user","content":"Hi"}],"response_format":{"type":"json_object"}}`, "", ""},
		{"a reply schema", `{"messages":[{"role":"user","content":"Hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"r","strict":true,"schema":{}}}}`, "", ""},
		{"a member Chat does not define", `{"messages":[{"role":"user","content":"Hi"}],"top_k":5}`, "", ""},
		{"a member of a message it has none for", `{"messages":[{"role":"user","audio":{"id":"a"},"content":"Hi"}]}`, "", ""},
		{"no tool call allowed", `{"messages":[{"role":"user","content":"Hi"}],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"none"}`, "", ""},
		{"a strict schema", `{"messages":[{"role":"user","content":"Hi"}],"tools":[{"type":"function","function":{"name":"f","strict":true}}]}`, "", ""},
		{"one call at a time", `{"messages":[{"role":"user","content":"Hi"}],"tools":[{"type":"function","function":{"name":"f"}}],"parallel_tool_calls":false}`, "", ""},
		{"an audio part", `{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]}`, "", ""},
	} {
		signsAs(t, tc.name, "amazon.nova-pro-v1:0", tc.body, tc.url, tc.want)
	}
	// Nova 2 Lite takes the effort as its extended thinking, the nearest level it has, and none as its default, no
	// thinking; its high effort beside an output cap, which Nova refuses, goes elsewhere.
	const nova2 = "us.amazon.nova-2-lite-v1:0"
	const lite = "https://bedrock-runtime.us-east-1.amazonaws.com/model/us.amazon.nova-2-lite-v1%3A0/converse"
	const hi = `{"role":"user","content":"Hi"}`
	for _, tc := range []struct{ effort, want string }{
		{"none", `{"messages":[` + `{"role":"user","content":[{"text":"Hi"}]}]}`},
		{"minimal", `{"messages":[{"role":"user","content":[{"text":"Hi"}]}],"additionalModelRequestFields":{"reasoningConfig":{"type":"enabled","maxReasoningEffort":"low"}}}`},
		{"medium", `{"messages":[{"role":"user","content":[{"text":"Hi"}]}],"additionalModelRequestFields":{"reasoningConfig":{"type":"enabled","maxReasoningEffort":"medium"}}}`},
		{"xhigh", `{"messages":[{"role":"user","content":[{"text":"Hi"}]}],"additionalModelRequestFields":{"reasoningConfig":{"type":"enabled","maxReasoningEffort":"high"}}}`},
	} {
		signsAs(t, "effort "+tc.effort, nova2, `{"messages":[`+hi+`],"reasoning_effort":"`+tc.effort+`"}`, lite, tc.want)
	}
	signsAs(t, "high effort with a cap", nova2, `{"messages":[`+hi+`],"reasoning_effort":"high","max_tokens":64}`, "", "")
}

// signsAs checks what Sign makes of a Chat body for model: the call sent to url with the Converse body want, or, for
// no want, the endpoint's failure before anything is sent, as the request is sound and another endpoint may take it.
func signsAs(t *testing.T, name, model, chat, url, want string) {
	t.Helper()
	req, f := quirks{}.Sign(t.Context(), nil, adapter.SignedCall{
		Format: protocol.Chat, Region: "us-east-1", Model: model, Body: []byte(chat), Key: exampleAPIKey, At: time.Now(),
	})
	if want == "" {
		if f == nil || f.Scope != adapter.ScopeEndpoint || f.Executed != adapter.NotExecuted {
			t.Errorf("%s: sent, or failed as %+v", name, f)
		}
		return
	}
	if f != nil {
		t.Fatalf("%s: %+v", name, f)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil || req.URL.String() != url || string(body) != canonical(t, want) || req.Header.Get("Authorization") != "Bearer "+testAPIKey {
		t.Errorf("%s: sent %s %s (%v)", name, req.URL, body, err)
	}
}

// answered is what Answer makes of a 2xx answer of Bedrock's to a call at action, as the outbound client returns it,
// read a byte at a time as from a slow connection: the body and its media type.
func answered(action, contentType string, body []byte) ([]byte, string, error) {
	u := &url.URL{Scheme: "https", Host: "bedrock-runtime.us-east-1.amazonaws.com", Path: "/model/amazon.nova-pro-v1:0/" + action}
	resp := quirks{}.Answer(&http.Response{
		StatusCode: http.StatusOK, Request: &http.Request{Method: http.MethodPost, URL: u}, Body: io.NopCloser(bytes.NewReader(body)),
		Header: http.Header{"Content-Type": {contentType}, "X-Amzn-Requestid": {"req-1"}, "Date": {"Thu, 26 Nov 2026 08:00:00 GMT"}},
	})
	got, err := io.ReadAll(iotest.OneByteReader(resp.Body))
	return got, resp.Header.Get("Content-Type"), errors.Join(err, resp.Body.Close())
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
		got, media, err := answered("converse", "application/json", []byte(tc.answer))
		if err != nil || string(got) != canonical(t, tc.want) || media != "application/json" {
			t.Errorf("%s: %s (%v)", tc.name, got, err)
		}
	}
	invoked := `{"type":"message","content":[{"type":"text","text":"Hi"}]}`
	if got, _, err := answered("invoke", "application/json", []byte(invoked)); err != nil || string(got) != invoked {
		t.Errorf("InvokeModel's answer became %s (%v)", got, err)
	}
	if _, _, err := answered("converse", "application/json", []byte(`{"output":`)); err == nil {
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
		got, media, err := answered("converse-stream", eventStream, bytes.Join(tc.stream, nil))
		events := strings.SplitAfter(string(got), "\n\n")
		if err != nil || len(events) != len(tc.want)+1 || events[len(tc.want)] != "" || media != "text/event-stream" {
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
