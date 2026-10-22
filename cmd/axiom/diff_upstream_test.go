//go:build diff

package main

import (
	"encoding/json/jsontext" //nolint:depguard // reads recorded samples; jsonx guards input from outside
	"encoding/json/v2"       //nolint:depguard // reads recorded samples; jsonx guards input from outside
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// diffUpstream is the one upstream both gateways call in the differential test: new-api through the Docker host
// gateway in CI or a tunnel on developers' machines, Axiom on loopback. It answers by the reply a request names (a
// "diff:<reply>" marker in its text, which both gateways forward untouched), as a stream when the request asks for one and whole otherwise, with the model name the
// request used, like a real upstream. The streams are the recorded samples of internal/protocol/testdata; what has no
// sample (a Chat tool call, the whole replies) is written below in the shape the official reference gives.
type diffUpstream struct {
	t       *testing.T
	server  *httptest.Server
	replies map[string]diffReply
	calls   atomic.Int64
}

// diffReply is one answer: a stream and the same answer whole, or an error.
type diffReply struct {
	stream, whole string
	status        int // an error reply; body is its body
	body          string
}

var (
	replyMarker = regexp.MustCompile(`diff:([a-z0-9-]+)`)
	modelMember = regexp.MustCompile(`"model"\s*:\s*"[^"]*"`)
)

// newAPIChannelKey is the credential new-api's channel holds. The fake upstream takes any.
const newAPIChannelKey = "sk-newapi-channel-secret"

func newDiffUpstream(t *testing.T) *diffUpstream {
	t.Helper()
	u := &diffUpstream{t: t, replies: diffReplies(t)}
	u.server = httptest.NewUnstartedServer(u)
	if os.Getenv("AXIOM_TEST_CGROUP_PARENT") != "" {
		// The CI container reaches the host gateway, while Axiom's fixture URL stays on loopback for its egress policy.
		// Listening on every interface is safe: the synthetic fixture holds no real credentials or user data.
		if err := u.server.Listener.Close(); err != nil {
			t.Fatal(err)
		}
		var config net.ListenConfig
		listener, err := config.Listen(t.Context(), "tcp4", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		u.server.Listener = listener
	}
	u.server.Start()
	u.server.URL = "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(u.server.Listener.Addr().(*net.TCPAddr).Port))
	t.Cleanup(u.server.Close)
	return u
}

func (u *diffUpstream) port() int {
	_, p, _ := strings.Cut(strings.TrimPrefix(u.server.URL, "http://"), ":")
	n, err := strconv.Atoi(p)
	if err != nil {
		u.t.Fatal(err)
	}
	return n
}

func (u *diffUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		u.t.Errorf("the upstream could not read a request: %v", err)
	}
	// What a relay sends and a gateway must not pass on (docs/domains/gateway.md「转发规则」).
	w.Header().Set("X-Oneapi-Request-Id", "relay-"+strconv.FormatInt(u.calls.Add(1), 10))
	w.Header().Set("X-Upstream", "secret")
	w.Header().Set("Openai-Organization", "org-secret")
	w.Header().Set("Cf-Ray", "0000000000000000-AMS")
	name := ""
	if m := replyMarker.FindSubmatch(body); m != nil {
		name = string(m[1])
		if wire, _, _ := strings.Cut(name, "-"); wire != wireOf(r.URL.Path) {
			u.t.Errorf("upstream received %s for %s fixture", r.URL.Path, name)
			http.Error(w, "wrong upstream format", http.StatusBadRequest)
			return
		}
	}
	reply, ok := u.replies[name]
	if !ok { // a request that carries no marker is one the upstream rejects
		reply = u.replies[wireOf(r.URL.Path)+"-400"]
	}
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &request) //nolint:errcheck // a body that is no JSON is answered like any other: by the reply it names
	u.write(w, reply, request.Model, request.Stream)
}

func (u *diffUpstream) write(w http.ResponseWriter, reply diffReply, model string, stream bool) {
	var content, text string
	switch {
	case reply.status != 0:
		content, text = "application/json", reply.body
	case stream:
		content, text = "text/event-stream", reply.stream
	default:
		content, text = "application/json", reply.whole
	}
	if model != "" {
		text = modelMember.ReplaceAllLiteralString(text, `"model":`+strconv.Quote(model))
	}
	w.Header().Set("Content-Type", content)
	if reply.status != 0 {
		w.WriteHeader(reply.status)
	}
	if _, err := io.WriteString(w, text); err != nil {
		u.t.Errorf("the upstream could not answer: %v", err)
	}
}

// wireOf names the format a path is the endpoint of: the upstream is asked in Messages, in Chat or in Responses.
func wireOf(path string) string {
	switch {
	case strings.HasSuffix(path, "/messages"):
		return "messages"
	case strings.HasSuffix(path, "/chat/completions"):
		return "chat"
	}
	return "responses"
}

// diffReplies is every answer the cases name: the recorded streams, the whole replies of the same conversations, and
// the three errors of each format in the shape that format's upstream gives them.
func diffReplies(t *testing.T) map[string]diffReply {
	t.Helper()
	sample := func(dir string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "protocol", "testdata", filepath.FromSlash(dir)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	// The sample cuts the text's deltas short ("Hi" for "Hi there! ..."); the stream is made to add up, as a real one does.
	responsesText := strings.Replace(sample("responses/text/upstream.sse"), `"delta":"Hi"`, `"delta":"Hi there! How can I assist you today?"`, 1)
	responsesTool := sample("assembled/responses-reasoning-tool.sse")
	replies := map[string]diffReply{
		"messages-text":       {stream: sample("messages/text/upstream.sse"), whole: messagesTextWhole},
		"messages-thinking":   {stream: sample("messages/thinking/upstream.sse"), whole: messagesThinkingWhole},
		"messages-tools":      {stream: sample("messages/tool-use/upstream.sse"), whole: messagesToolsWhole},
		"responses-text":      {stream: responsesText, whole: completedResponse(t, responsesText)},
		"responses-reasoning": {stream: responsesTool, whole: completedResponse(t, responsesTool)},
		"chat-text":           {stream: chatTextStream(sample("chat/text/upstream.sse")), whole: chatTextWhole},
		"chat-tools":          {stream: sample("assembled/chat-tool-calls.sse"), whole: chatToolsWhole},
		"chat-reasoning":      {stream: sample("assembled/chat-reasoning-cache.sse"), whole: chatReasoningWhole},
	}
	for name, status := range map[string]int{"429": 429, "500": 500, "400": 400} {
		replies["responses-"+name] = diffReply{status: status, body: openAIError[name]}
		replies["messages-"+name] = diffReply{status: status, body: anthropicError[name]}
		replies["chat-"+name] = diffReply{status: status, body: openAIError[name]}
	}
	return replies
}

// chatTextStream is the Chat sample with the usage chunk that stream_options.include_usage asks for, which the sample
// (the official Streaming example) lacks, so that both gateways report the upstream's count and not their own.
func chatTextStream(stream string) string {
	const usage = `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-6-astra","choices":[],` +
		`"usage":{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}}` + "\n\n"
	return strings.Replace(stream, "data: [DONE]", usage+"data: [DONE]", 1)
}

// completedResponse is the whole reply of a Responses stream: the response its last event carries.
func completedResponse(t *testing.T, stream string) string {
	t.Helper()
	for line := range strings.SplitSeq(stream, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var event struct {
			Type     string         `json:"type"`
			Response jsontext.Value `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &event); err == nil && event.Type == "response.completed" {
			return string(event.Response)
		}
	}
	t.Fatal("a Responses stream without response.completed")
	return ""
}

// The whole replies of the Chat conversations, in the shape of the official reference and with the content and the usage
// of the streams they go with (the Chat sample, and testdata/assembled/chat-tool-calls.sse and chat-reasoning-cache.sse).
const (
	chatTextWhole = `{"id":"chatcmpl-123","object":"chat.completion","created":1694268190,"model":"gpt-6-astra","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":"Hello"},"logprobs":null,"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}}`
	chatToolsWhole = `{"id":"chatcmpl-tool1","object":"chat.completion","created":1760000000,"model":"deepseek-chat","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":"Let me check.","tool_calls":[` +
		`{"id":"call_00_Kq3vT9","type":"function","function":{"name":"get_weather","arguments":"{\"location\": \"Paris\"}"}},` +
		`{"id":"call_01_Zb8mW2","type":"function","function":{"name":"get_time","arguments":"{}"}}]},"logprobs":null,"finish_reason":"tool_calls"}],` +
		`"usage":{"prompt_tokens":120,"completion_tokens":40,"total_tokens":160,"prompt_tokens_details":{"cached_tokens":64},` +
		`"prompt_cache_hit_tokens":64,"prompt_cache_miss_tokens":56}}`
	chatReasoningWhole = `{"id":"7a1e0c2b","object":"chat.completion","created":1760000100,"model":"deepseek-reasoner","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":"The GCD is 21.","reasoning_content":"1071 = 2 x 462 + 147. Then 462 = 3 x 147 + 21."},` +
		`"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":200,"completion_tokens":50,"total_tokens":250,` +
		`"prompt_tokens_details":{"cached_tokens":128},"completion_tokens_details":{"reasoning_tokens":30},` +
		`"prompt_cache_hit_tokens":128,"prompt_cache_miss_tokens":72}}`
)

const (
	messagesTextWhole = `{"id":"msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY","type":"message","role":"assistant","model":"claude-opus-5-5",` +
		`"content":[{"type":"text","text":"Hello!"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":15}}`
	messagesThinkingWhole = `{"id":"msg_01...","type":"message","role":"assistant","model":"claude-opus-5-5","content":[` +
		`{"type":"thinking","thinking":"I need to find the GCD of 1071 and 462 using the Euclidean algorithm.\n\n1071 = 2 × 462 + 147\n462 = 3 × 147 + 21\n147 = 7 × 21 + 0\nThe remainder is 0, so GCD(1071, 462) = 21.","signature":"EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiMOYds..."},` +
		`{"type":"text","text":"The greatest common divisor of 1071 and 462 is **21**."}],"stop_reason":"end_turn","stop_sequence":null}`
	messagesToolsWhole = `{"id":"msg_014p7gG3wDgGV9EUtLvnow3U","type":"message","role":"assistant","model":"claude-opus-5","content":[` +
		`{"type":"text","text":"Okay, let's check the weather for San Francisco, CA:"},` +
		`{"type":"tool_use","id":"toolu_01T1x1fJ34qAmk2tNTrN7Up6","name":"get_weather","input":{"location":"San Francisco, CA"}}],` +
		`"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":472,"output_tokens":89}}`
)

// The errors an upstream gives, in the shape of OpenAI's and Anthropic's references: a rejected request, a rate limit
// and a failure of its own.
var (
	openAIError = map[string]string{
		"400": `{"error":{"message":"Invalid value for 'max_tokens': expected a value above 0.","type":"invalid_request_error","param":"max_tokens","code":"invalid_value"}}`,
		"429": `{"error":{"message":"Rate limit reached for requests","type":"requests","param":null,"code":"rate_limit_exceeded"}}`,
		"500": `{"error":{"message":"The server had an error while processing your request. Sorry about that!","type":"server_error","param":null,"code":null}}`,
	}
	anthropicError = map[string]string{
		"400": `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: Input should be greater than 0"}}`,
		"429": `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`,
		"500": `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`,
	}
)
