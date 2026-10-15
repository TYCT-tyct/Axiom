package protocol_test

import (
	"encoding/json/jsontext" //nolint:depguard // the test reads members as written
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// chatBody is a Chat request with members, after its model.
func chatBody(members string) string { return `{"model":"m",` + members + `}` }

const (
	userHi = `{"role":"user","content":"Hi"}`
	// A call and its result, with an ID Anthropic accepts as it is.
	callTurn = `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"done"}`
)

// convertCase is one row of the conversion matrix (README.md「跨格式转换」): what a Chat request becomes, or why it
// cannot, for an endpoint of format to.
type convertCase struct {
	name, body string
	target     func(*protocol.Target) // changes to the Messages model's defaults
	want       string                 // the upstream body; or "unsupported" or "invalid"
}

func convertCases(t *testing.T, to protocol.Format, cases []convertCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := convertBody(to, c.body, c.target)
			ns, _ := errors.AsType[*protocol.NotSupported](err)
			unsupported := ns != nil
			switch {
			case c.want == "unsupported" && !unsupported, c.want == "invalid" && !errors.Is(err, protocol.ErrInvalid):
				t.Fatalf("got %s, %v; want %s", got, err, c.want)
			case c.want == "unsupported", c.want == "invalid":
			case err != nil:
				t.Fatal(err)
			default:
				sameJSON(t, c.want, got)
			}
		})
	}
}

// sameJSON compares JSON as values, so member order and spacing do not count.
func sameJSON(t *testing.T, want string, got []byte) {
	t.Helper()
	var g, w any
	if err := jsonx.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	if err := jsonx.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want %s: %v", want, err)
	}
	if diff := cmp.Diff(w, g); diff != "" {
		t.Errorf("(-want +got):\n%s\n%s", diff, got)
	}
}

func convertBody(to protocol.Format, body string, change func(*protocol.Target)) ([]byte, error) {
	r, err := protocol.Inspect(protocol.Chat, []byte(body))
	if err != nil {
		return nil, err
	}
	target := protocol.Target{Format: to, Model: "up", MaxOutput: 4096, MidSystem: true}
	if change != nil {
		change(&target)
	}
	u, err := protocol.Prepare(&r, []byte(body), target, protocol.Rewrites{Identity: derive})
	return u.Body, err
}

func manual(t *protocol.Target)         { t.Thinking = protocol.ThinkingManual }
func defaultsOnly(t *protocol.Target)   { t.DefaultSamplingOnly = true }
func noMidSystem(t *protocol.Target)    { t.MidSystem = false }
func untrustedAfter(t *protocol.Target) { t.UntrustedSystemAfterTools = true }
func encrypting(t *protocol.Target)     { t.EncryptedReasoning = true }
func noDefaultCap(t *protocol.Target)   { t.MaxOutput = 0 }

// messagesHead is a Messages body's start, for the model's default cap.
const messagesHead = `{"model":"up","max_tokens":4096,`

func TestConvertToMessages(t *testing.T) {
	convertCases(t, protocol.Messages, []convertCase{
		{
			"system and text", chatBody(`"messages":[{"role":"developer","content":"Be brief."},{"role":"system","content":[{"type":"text","text":"Be kind."}]},` + userHi + `],"stream":true`), nil,
			messagesHead + `"system":[{"type":"text","text":"Be brief."},{"type":"text","text":"Be kind."}],"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],"stream":true}`,
		},
		{
			"the request's cap", chatBody(`"messages":[` + userHi + `],"max_tokens":10,"max_completion_tokens":20`), noDefaultCap,
			`{"model":"up","max_tokens":20,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`,
		},
		{"no cap at all", chatBody(`"messages":[` + userHi + `]`), noDefaultCap, "unsupported"},
		{"starts with the assistant", chatBody(`"messages":[{"role":"assistant","content":"Hello"},` + userHi + `]`), nil, "unsupported"},
		{
			"system in place", chatBody(`"messages":[` + userHi + `,{"role":"system","content":"Now French."},{"role":"assistant","content":"Salut"}]`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]},{"role":"system","content":[{"type":"text","text":"Now French."}]},` +
				`{"role":"assistant","content":[{"type":"text","text":"Salut"}]}]}`,
		},
		{"system in place, not taken", chatBody(`"messages":[` + userHi + `,{"role":"system","content":"x"}]`), noMidSystem, "unsupported"},
		{"system after the assistant", chatBody(`"messages":[` + userHi + `,{"role":"assistant","content":"a"},{"role":"system","content":"x"}]`), nil, "unsupported"},
		{"system after tool results, untrusted", chatBody(`"messages":[` + userHi + `,` + callTurn + `,{"role":"system","content":"x"}]`), untrustedAfter, "unsupported"},
		{
			"images and a document", chatBody(`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}},` +
				`{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"high"}},{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBE"}},` +
				`{"type":"text","text":"What?","cache_control":{"type":"ephemeral"}}]}]`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},` +
				`{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}},{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBE"}},` +
				`{"type":"text","text":"What?","cache_control":{"type":"ephemeral"}}]}]}`,
		},
		{"low detail", chatBody(`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://e/a.png","detail":"low"}}]}]`), nil, "unsupported"},
		{"a file by ID", chatBody(`"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file_1"}}]}]`), nil, "unsupported"},
		{"a file not PDF", chatBody(`"messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"data:text/plain;base64,QQ=="}}]}]`), nil, "unsupported"},
		{"audio", chatBody(`"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AA","format":"wav"}}]}]`), nil, "unsupported"},
		{
			"tool calls and results", chatBody(`"messages":[` + userHi + `,{"role":"assistant","content":"Let me see.","tool_calls":[` +
				`{"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}},{"id":"call.2","type":"function","function":{"name":"g","arguments":""}}]},` +
				`{"role":"tool","tool_call_id":"call_1","content":"one"},{"role":"tool","tool_call_id":"call.2","content":[{"type":"text","text":"two"}]}]`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]},{"role":"assistant","content":[{"type":"text","text":"Let me see."},` +
				`{"type":"tool_use","id":"call_1","name":"f","input":{"a":1}},{"type":"tool_use","id":"axid_63616c6c2e32","name":"g","input":{}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"one"},{"type":"tool_result","tool_use_id":"axid_63616c6c2e32","content":[{"type":"text","text":"two"}]}]}]}`,
		},
		{"arguments not an object", chatBody(`"messages":[` + userHi + `,{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"[1]"}}]},` +
			`{"role":"tool","tool_call_id":"c","content":"x"}]`), nil, "unsupported"},
		{"a call without its result", chatBody(`"messages":[` + userHi + `,{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}]`), nil, "unsupported"},
		{"a result without its call", chatBody(`"messages":[` + userHi + `,{"role":"tool","tool_call_id":"c","content":"x"}]`), nil, "unsupported"},
		{"an ID twice", chatBody(`"messages":[` + userHi + `,` + callTurn + `,` + callTurn + `]`), nil, "unsupported"},
		{
			"an empty assistant turn", chatBody(`"messages":[` + userHi + `,{"role":"assistant","content":""},` + userHi + `]`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]},{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`,
		},
		{
			"tools", chatBody(`"messages":[` + userHi + `],"tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object","properties":{"a":{"type":"integer"}},"required":["a"],"additionalProperties":false},"strict":true}},` +
				`{"type":"function","function":{"name":"g"}}],"tool_choice":"required","parallel_tool_calls":false`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],"tools":[{"name":"f","description":"d","input_schema":{"type":"object","properties":{"a":{"type":"integer"}},"required":["a"],"additionalProperties":false},"strict":true},` +
				`{"name":"g","input_schema":{"type":"object","properties":{}}}],"tool_choice":{"type":"any","disable_parallel_tool_use":true}}`,
		},
		{
			"a named tool", chatBody(`"messages":[` + userHi + `],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":{"type":"function","function":{"name":"f"}}`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],"tools":[{"name":"f","input_schema":{"type":"object","properties":{}}}],"tool_choice":{"type":"tool","name":"f"}}`,
		},
		{"a strict schema outside the subset", chatBody(`"messages":[` + userHi + `],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"a":{"type":"integer","minimum":1}},"additionalProperties":false},"strict":true}}]`), nil, "unsupported"},
		{"a forced tool while thinking", chatBody(`"messages":[` + userHi + `],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"required","reasoning_effort":"high"`), nil, "unsupported"},
		{"a forced tool the model refuses", chatBody(`"messages":[` + userHi + `],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"required"`), defaultsOnly, "unsupported"},
		{
			"adaptive effort", chatBody(`"messages":[` + userHi + `],"reasoning":{"effort":"high"}`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`,
		},
		{
			"effort none", chatBody(`"messages":[` + userHi + `],"reasoning_effort":"none"`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`,
		},
		{"effort minimal", chatBody(`"messages":[` + userHi + `],"reasoning_effort":"minimal"`), nil, "unsupported"},
		{"a budget for an adaptive model", chatBody(`"messages":[` + userHi + `],"reasoning":{"max_tokens":2000}`), nil, "unsupported"},
		{
			"manual effort", chatBody(`"messages":[` + userHi + `],"max_tokens":10000,"reasoning_effort":"medium"`), manual,
			`{"model":"up","max_tokens":10000,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],"thinking":{"type":"enabled","budget_tokens":5000}}`,
		},
		{"a manual budget above the cap", chatBody(`"messages":[` + userHi + `],"max_tokens":1000,"reasoning_effort":"high"`), manual, "unsupported"},
		{"manual thinking after a call without its reasoning", chatBody(`"messages":[` + userHi + `,` + callTurn + `],"reasoning_effort":"high"`), manual, "unsupported"},
		{
			"reasoning handed back", chatBody(`"messages":[` + userHi + `,{"role":"assistant","content":"x","reasoning_details":[` +
				`{"type":"reasoning.text","format":"anthropic-claude-v1","text":"hm","signature":"sig"},{"type":"reasoning.encrypted","format":"anthropic-claude-v1","data":"red"},` +
				`{"type":"reasoning.text","format":"anthropic-claude-v1","text":"unsigned","signature":null},{"type":"reasoning.summary","format":"openai-responses-v1","summary":"other"}]},` + userHi + `]`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"sig"},` +
				`{"type":"redacted_thinking","data":"red"},{"type":"text","text":"x"}]},{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`,
		},
		{
			"a structured reply", chatBody(`"messages":[` + userHi + `],"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":{"type":"object","properties":{},"additionalProperties":false}}}`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{},"additionalProperties":false}}}}`,
		},
		{"JSON mode", chatBody(`"messages":[` + userHi + `],"response_format":{"type":"json_object"}`), nil, "unsupported"},
		{"a reply schema outside the subset", chatBody(`"messages":[` + userHi + `],"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":{"type":"object","properties":{}}}}`), nil, "unsupported"},
		{
			"sampling", chatBody(`"messages":[` + userHi + `],"temperature":0.5,"top_p":1,"stop":"END","frequency_penalty":0,"n":1`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],"temperature":0.5,"stop_sequences":["END"]}`,
		},
		{"a temperature above 1", chatBody(`"messages":[` + userHi + `],"temperature":1.5`), nil, "unsupported"},
		{"a temperature the model refuses", chatBody(`"messages":[` + userHi + `],"temperature":0.5`), defaultsOnly, "unsupported"},
		{"a penalty", chatBody(`"messages":[` + userHi + `],"presence_penalty":0.5`), nil, "unsupported"},
		{"two choices", chatBody(`"messages":[` + userHi + `],"n":2`), nil, "unsupported"},
		{"a member not converted", chatBody(`"messages":[` + userHi + `],"prediction":{"type":"content","content":"x"}`), nil, "unsupported"},
		{
			"identity, routing and tier", chatBody(`"messages":[` + userHi + `],"user":"u1","prompt_cache_key":"k","provider":{"order":["x"]},"service_tier":"default"`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],"service_tier":"standard_only","metadata":{"user_id":"h(u1)"}}`,
		},
		{"priority", chatBody(`"messages":[` + userHi + `],"service_tier":"priority"`), nil, "unsupported"},
		{"verbosity", chatBody(`"messages":[` + userHi + `],"verbosity":"low"`), nil, "unsupported"},
		{"messages not an array", chatBody(`"messages":{"role":"user"}`), nil, "invalid"},
		{"a tool without a name", chatBody(`"messages":[` + userHi + `],"tools":[{"type":"function"}]`), nil, "invalid"},
		{"a call without string arguments", chatBody(`"messages":[` + userHi + `,{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":{}}}]},` +
			`{"role":"tool","tool_call_id":"c","content":"x"}]`), nil, "invalid"},
	})
}

func TestConvertToResponses(t *testing.T) {
	const head = `{"model":"up","input":[`
	convertCases(t, protocol.Responses, []convertCase{
		{
			"system, text and storage", chatBody(`"messages":[{"role":"developer","content":"Be brief."},` + userHi + `],"stream":true,"max_tokens":50`), encrypting,
			head + `{"type":"message","role":"developer","content":[{"type":"input_text","text":"Be brief."}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}],` +
				`"store":false,"include":["reasoning.encrypted_content"],"max_output_tokens":50,"stream":true}`,
		},
		{
			"images and files", chatBody(`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://e/a.png","detail":"low"}},` +
				`{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBE","filename":"a.pdf"}}]}]`), nil,
			head + `{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://e/a.png","detail":"low"},{"type":"input_file","file_data":"data:application/pdf;base64,JVBE","filename":"a.pdf"}]}],"store":false}`,
		},
		{"a file by ID", chatBody(`"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file_1"}}]}]`), nil, "unsupported"},
		{
			"calls and results", chatBody(`"messages":[` + userHi + `,` + callTurn + `]`), nil,
			head + `{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]},{"type":"function_call","call_id":"call_1","name":"f","arguments":"{\"a\":1}"},` +
				`{"type":"function_call_output","call_id":"call_1","output":"done"}],"store":false}`,
		},
		{
			"reasoning handed back", chatBody(`"messages":[` + userHi + `,{"role":"assistant","content":"x","reasoning_details":[` +
				`{"type":"reasoning.summary","format":"openai-responses-v1","id":"rs_1","summary":"thought"},{"type":"reasoning.encrypted","format":"openai-responses-v1","id":"rs_1","data":"enc"},` +
				`{"type":"reasoning.summary","format":"openai-responses-v1","id":"rs_2","summary":"no data"},{"type":"reasoning.text","format":"anthropic-claude-v1","text":"other","signature":"s"}]}]`), nil,
			head + `{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]},{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thought"}],"encrypted_content":"enc"},` +
				`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"x"}]}],"store":false}`,
		},
		{
			"tools", chatBody(`"messages":[` + userHi + `],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}},{"type":"function","function":{"name":"g","strict":true}}],` +
				`"tool_choice":{"type":"function","function":{"name":"f"}},"parallel_tool_calls":false`), nil,
			head + `{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}],"store":false,"tools":[{"type":"function","name":"f","parameters":{"type":"object"},"strict":false},` +
				`{"type":"function","name":"g","strict":true}],"tool_choice":{"type":"function","name":"f"},"parallel_tool_calls":false}`,
		},
		{
			"reasoning, format and verbosity", chatBody(`"messages":[` + userHi + `],"reasoning_effort":"high","response_format":{"type":"json_schema","json_schema":{"name":"r","schema":{"type":"object"},"strict":true}},"verbosity":"low","temperature":0.2`), nil,
			head + `{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}],"store":false,"temperature":0.2,"reasoning":{"effort":"high","summary":"auto"},` +
				`"text":{"format":{"type":"json_schema","name":"r","schema":{"type":"object"},"strict":true},"verbosity":"low"}}`,
		},
		{
			"reasoning left out, JSON mode", chatBody(`"messages":[` + userHi + `],"reasoning":{"effort":"low","exclude":true},"response_format":{"type":"json_object"}`), nil,
			head + `{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}],"store":false,"reasoning":{"effort":"low"},"text":{"format":{"type":"json_object"}}}`,
		},
		{"a reasoning budget", chatBody(`"messages":[` + userHi + `],"reasoning":{"max_tokens":100}`), nil, "unsupported"},
		{"stop", chatBody(`"messages":[` + userHi + `],"stop":["x"]`), nil, "unsupported"},
		{
			"identities and tier", chatBody(`"messages":[` + userHi + `],"user":"u","safety_identifier":"s","prompt_cache_key":"k","service_tier":"flex"`), nil,
			head + `{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}],"store":false,"user":"h(u)","safety_identifier":"h(s)","prompt_cache_key":"h(k)","service_tier":"flex"}`,
		},
		{
			"an empty assistant turn", chatBody(`"messages":[` + userHi + `,{"role":"assistant","content":""},` + userHi + `]`), nil,
			head + `{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":""}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}],"store":false}`,
		},
	})
}

// TestToolUseIDs: an ID Anthropic accepts stays; any other is written in hex behind a prefix that the first kind
// cannot take, so distinct IDs stay distinct.
func TestToolUseIDs(t *testing.T) {
	ids := []string{"call_1", "call.1", "axid_63616c6c2e31", "", "名", "a-b_C9", strings.Repeat("x", 300)}
	seen := map[string]string{}
	for _, id := range ids {
		body := chatBody(`"messages":[` + userHi + `,{"role":"assistant","tool_calls":[{"id":` + quote(id) +
			`,"type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":` + quote(id) + `,"content":"x"}]`)
		got, err := convertBody(protocol.Messages, body, nil)
		if id == "" {
			if err == nil {
				t.Errorf("an empty ID converted: %s", got)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var b struct {
			Messages []struct {
				Content []struct {
					ID        string `json:"id"`
					ToolUseID string `json:"tool_use_id"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := jsonx.Unmarshal(got, &b); err != nil {
			t.Fatal(err)
		}
		call, result := b.Messages[1].Content[0].ID, b.Messages[2].Content[0].ToolUseID
		if call != result || strings.Trim(call, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
			t.Errorf("%q became %q and %q", id, call, result)
		}
		if other, ok := seen[call]; ok {
			t.Errorf("%q and %q both became %q", other, id, call)
		}
		seen[call] = id
	}
}

func quote(s string) string {
	b, _ := jsonx.MarshalCanonical(s) //nolint:errcheck // a string always encodes
	return string(b)
}

// TestConvertSamples converts each Chat sample's request for both other formats and compares the result with its
// golden copy, to-<format>.json, which go test -update rewrites.
func TestConvertSamples(t *testing.T) {
	for _, dir := range must(fs.Glob(samples, "chat/*")) {
		for _, to := range []struct {
			f    protocol.Format
			name string
		}{{protocol.Messages, "messages"}, {protocol.Responses, "responses"}} {
			t.Run(dir+"/"+to.name, func(t *testing.T) {
				got, err := convertBody(to.f, string(sample(t, dir+"/request.json")), nil)
				if err != nil {
					t.Fatal(err)
				}
				golden(t, dir+"/to-"+to.name+".json", append(got, '\n'))
			})
		}
	}
}

// conversation is a Chat conversation that each of its cut points ends validly: every call answered.
var conversation = []string{
	`{"role":"system","content":"Be brief."}`, userHi,
	`{"role":"assistant","content":"Looking.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"g","arguments":"{\"x\":1}"}}]}`,
	`{"role":"tool","tool_call_id":"call_1","content":"one"}`, `{"role":"tool","tool_call_id":"call_2","content":"two"}`,
	`{"role":"user","content":[{"type":"text","text":"And?"},{"type":"image_url","image_url":{"url":"https://e/a.png"}}]}`,
	`{"role":"system","content":"Answer in French."}`,
	`{"role":"assistant","content":"Voila.","reasoning_details":[{"type":"reasoning.text","format":"anthropic-claude-v1","text":"hm","signature":"s"}]}`,
	userHi,
}

// TestConvertPrefix: a conversion depends on the history alone, so the conversation converted after turn n is a prefix
// of the one converted after turn n+1 (I11): an upstream's prompt cache and Anthropic's thinking signatures see the
// same prefix every turn.
func TestConvertPrefix(t *testing.T) {
	for _, to := range []protocol.Format{protocol.Messages, protocol.Responses} {
		var last string
		for n := 2; n <= len(conversation); n++ {
			if n == 3 || n == 4 { // a call not yet answered
				continue
			}
			body := chatBody(`"messages":[` + strings.Join(conversation[:n], ",") + `]`)
			got, err := convertBody(to, body, nil)
			if err != nil {
				t.Fatalf("format %d, %d messages: %v", to, n, err)
			}
			turns := conversationOf(t, got)
			if !strings.HasPrefix(turns, strings.TrimRight(last, "]}")) {
				t.Errorf("format %d: after %d messages\n%s\nis not a continuation of\n%s", to, n, turns, last)
			}
			last = turns
		}
	}
}

// conversationOf is the messages or input of a converted body, as written.
func conversationOf(t *testing.T, body []byte) string {
	t.Helper()
	var b struct {
		Messages jsontext.Value `json:"messages"`
		Input    jsontext.Value `json:"input"`
	}
	if err := jsonx.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	return string(b.Messages) + string(b.Input)
}

// FuzzConvert holds the conversions to their contract: a Chat request Inspect accepts converts into a body our own
// reading of the endpoint's format accepts, or is refused as the endpoint's shortcoming or the client's error;
// nothing else, and never a panic.
func FuzzConvert(f *testing.F) {
	for _, c := range inspectCases {
		if c.format == protocol.Chat {
			f.Add(uint8(0), caseBody(f, c.body))
		}
	}
	f.Add(uint8(0), []byte(chatBody(`"messages":[`+strings.Join(conversation, ",")+`]`)))
	f.Add(uint8(1), []byte(chatBody(`"messages":[`+strings.Join(conversation, ",")+`],"tools":[{"type":"function","function":{"name":"f","strict":true}}]`)))
	f.Add(uint8(1), []byte(`{"model":"0","tools":[{"type":"function"}]}`))
	f.Fuzz(func(t *testing.T, which uint8, body []byte) {
		to := []protocol.Format{protocol.Messages, protocol.Responses}[which%2]
		r, err := protocol.Inspect(protocol.Chat, body)
		if err != nil {
			return
		}
		u, err := protocol.Prepare(&r, body, protocol.Target{Format: to, Model: "up", MaxOutput: 4096, MidSystem: true},
			protocol.Rewrites{Identity: derive})
		ns, _ := errors.AsType[*protocol.NotSupported](err)
		switch {
		case ns != nil, errors.Is(err, protocol.ErrInvalid):
			return
		case err != nil:
			t.Fatalf("%s: %v", body, err)
		}
		if _, err := protocol.Inspect(to, u.Body); err != nil {
			t.Fatalf("%s\nbecame\n%s\n%v", body, u.Body, err)
		}
	})
}
