package protocol

import (
	"strings"
	"testing"
	"unicode/utf8"

	"axiom/internal/platform/jsonx"
)

func TestResponseHealing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ input, want string }{
		{`{"name":"Alice","age":30`, `{"name":"Alice","age":30}`},
		{"```json\n{\"name\":\"Bob\"}\n```", `{"name":"Bob"}`},
		{`Here is the data: {name:"Eve", age:40,}`, `{"name":"Eve","age":40}`},
		{`{"a":[1 2,], "b":true}`, `{"a":[1,2],"b":true}`},
		{`{"a":"missing quote}`, `{"a":"missing quote}`},
		{`no JSON at all`, `no JSON at all`},
		{`{"a": 1, "a":2}`, `{"a": 1, "a":2}`},
		{` {"valid": "你好"} `, ` {"valid": "你好"} `},
		{`{"text":"a\\b\"c",}`, `{"text":"a\\b\"c"}`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			escaped := jsonValue(tc.input)
			got, err := requestString(appendContents(nil, healContent(escaped[1:len(escaped)-1]), true))
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestHealingOnlyForCompletedNonStreamingJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, members, finish string
		healed                bool
	}{
		{"json object", `"response_format":{"type":"json_object"}`, "stop", true},
		{"json schema", `"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object"}}}`, "stop", true},
		{"text", `"response_format":{"type":"text"}`, "stop", false},
		{"stream", `"stream":true,"response_format":{"type":"json_object"}`, "stop", false},
		{"truncated", `"response_format":{"type":"json_object"}`, "length", false},
		{"disabled", `"plugins":[{"id":"response-healing","enabled":false}],"response_format":{"type":"json_object"}`, "stop", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plugin := `"plugins":[{"id":"response-healing"}],`
			if tc.name == "disabled" {
				plugin = ""
			}
			body := `{"model":"a",` + plugin + tc.members + `}`
			got := healingReply(t, body, tc.finish)
			want := `{name:"Alice",}`
			if tc.healed {
				want = `{"name":"Alice"}`
			}
			if got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

func healingReply(t *testing.T, body, finish string) string {
	t.Helper()

	req, err := Inspect(Chat, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	up, err := Prepare(&req, []byte(body), Target{Format: Chat, Model: "up", Streams: true}, Rewrites{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(up.Body), "plugins") {
		t.Fatalf("control forwarded: %s", up.Body)
	}
	chunk := jsonValue(map[string]any{"id": "up", "model": "up", "created": 1, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": `{name:"Alice",}`}, "finish_reason": finish}}})
	src := "data: " + string(chunk) + "\n\ndata: [DONE]\n\n"
	memory := NewMemory(1 << 26)
	relay := NewRelay(Chat, Chat, strings.NewReader(src), up.Usage, RelayOptions{ID: "id", Model: "a", Aggregate: true, Memory: memory})
	defer relay.Release()
	for {
		step, err := relay.Next()
		if err != nil {
			t.Fatal(err)
		}
		if step.Kind == StepUpstreamDone {
			break
		}
	}
	if relay.Progress().ContentBytes != int64(len(`{name:"Alice",}`)) {
		t.Fatal("repair changed upstream metering")
	}
	result := relay.Finish(Final{Usage: relay.Usage()})
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := jsonx.Unmarshal(result, &reply); err != nil {
		t.Fatalf("%s: %v", result, err)
	}
	relay.Release()
	if memory.left.Load() != 1<<26 {
		t.Fatal("healing leaked memory budget")
	}
	return reply.Choices[0].Message.Content
}

func FuzzResponseHealing(f *testing.F) {
	for _, s := range []string{`{"a":[1,2,]`, "```json\n{hello: true}\n```", `{"a":"bad`, strings.Repeat("[", 300), `{"x":"\\\""}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			t.Skip()
		}
		raw := jsonValue(s)
		got := healContent(raw[1 : len(raw)-1])
		content, err := requestString(appendContents(nil, got, true))
		if err != nil {
			t.Fatal(err)
		}
		if content != s && jsonx.Validate([]byte(content)) != nil {
			t.Fatalf("repair emitted invalid JSON: %s", content)
		}
	})
}

func TestHealingDoesNotChangeFailedRefusedOrToolOutput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		failed, refused, tool bool
		finish                string
	}{
		{failed: true, finish: "stop"}, {refused: true, finish: "stop"}, {tool: true, finish: "tool_calls"},
	} {
		a := chatAggregate{healing: true, hadError: tc.failed}
		content := jsonValue(`{key:1,}`)
		c := chatChoice{finish: jsonValue(tc.finish)}
		c.strings[pieceContent] = content[1 : len(content)-1]
		if tc.refused {
			c.strings[pieceRefusal] = []byte("refused")
		}
		if tc.tool {
			c.tools = []chatTool{{}}
		}
		a.choices = []chatChoice{c}
		a.heal()
		if string(a.choices[0].strings[pieceContent]) != string(content[1:len(content)-1]) {
			t.Fatal("unsafe reply was healed")
		}
	}
}

func TestHealedReplyDropsStaleLogprobs(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`{key:1,}`, `{"key":1}`} {
		raw := jsonValue(input)
		c := chatChoice{finish: jsonValue("stop")}
		c.strings[pieceContent] = raw[1 : len(raw)-1]
		c.logs[0], c.logSeen[0] = []byte(`{"token":"x","logprob":0}`), true
		a := chatAggregate{healing: true, choices: []chatChoice{c}}
		body, err := a.body()
		if err != nil {
			t.Fatal(err)
		}
		cleared := strings.Contains(string(body), `"logprobs":null`)
		want := input == `{key:1,}`
		if cleared != want {
			t.Fatalf("logprobs: %s", body)
		}
	}
}
