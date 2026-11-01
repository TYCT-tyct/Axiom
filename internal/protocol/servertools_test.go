package protocol_test

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

//nolint:cyclop // a table of accepted parameters
func TestInspectWebSearchTool(t *testing.T) {
	req, err := protocol.Inspect(protocol.Chat, []byte(`{"model":"m","max_tool_calls":7,"tools":[{"type":"openrouter:web_search","parameters":{"engine":"native","max_results":9,"max_uses":4,"max_total_results":12,"allowed_domains":["example.com"],"excluded_domains":["spam.example"]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	tool := req.ServerSearch
	if tool == nil || tool.Engine != "native" || tool.MaxResults != 9 || tool.MaxUses != 4 ||
		tool.MaxTotalResults != 12 || len(tool.IncludeDomains) != 1 || len(tool.ExcludeDomains) != 1 {
		t.Fatalf("tool = %+v", tool)
	}
	if !req.HasMaxToolCalls || req.MaxToolCalls != 7 || !req.Needs.Has(protocol.Tools|protocol.WebSearch) || req.NativeWeb {
		t.Fatalf("request = %+v", req)
	}
	req, err = protocol.Inspect(protocol.Responses, []byte(`{"model":"m","tools":[{"type":"openrouter:web_search"}]}`))
	if err != nil || req.ServerSearch == nil || req.ServerSearch.MaxResults != protocol.DefaultWebSearchResults || req.ServerSearch.MaxUses != 0 {
		t.Fatalf("defaults: %+v, %v", req.ServerSearch, err)
	}
}

func TestInspectWebSearchParameterOrder(t *testing.T) {
	for _, tc := range []struct {
		format protocol.Format
		body   string
	}{
		{protocol.Chat, `{"model":"m","tools":[{"parameters":{"engine":"native","max_results":7},"type":"openrouter:web_search"}]}`},
		{protocol.Messages, `{"model":"m","tools":[{"max_uses":4,"parameters":{"engine":"native","max_results":7},"type":"openrouter:web_search"}]}`},
		{protocol.Responses, `{"model":"m","tools":[{"parameters":{"engine":"native","max_results":7},"type":"openrouter:web_search"}]}`},
	} {
		req, err := protocol.Inspect(tc.format, []byte(tc.body))
		if err != nil {
			t.Fatalf("%d: %v", tc.format, err)
		}
		if req.ServerSearch == nil || req.ServerSearch.Engine != "native" || req.ServerSearch.MaxResults != 7 {
			t.Fatalf("%d: parameters were not read after type: %+v", tc.format, req.ServerSearch)
		}
	}
	// A client tool's large parameters are its own; the web tool's are bounded.
	if _, err := protocol.Inspect(protocol.Chat, []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"f"},"parameters":{"schema":"`+strings.Repeat("x", protocol.MaxServerToolBytes+1)+`"}}]}`)); err != nil {
		t.Fatalf("a client tool's parameters were bounded: %v", err)
	}
	if _, err := protocol.Inspect(protocol.Chat, []byte(`{"model":"m","tools":[{"parameters":{"x":"`+strings.Repeat("x", protocol.MaxServerToolBytes+1)+`"},"type":"openrouter:web_search"}]}`)); !errors.Is(err, protocol.ErrInvalid) {
		t.Fatalf("large web search parameters: %v", err)
	}
}

func TestInspectWebSearchToolRefusals(t *testing.T) {
	for _, parameters := range []string{
		`{"mode":"deep"}`,
		`{"search_context_size":"high"}`,
		`{"max_results":0}`,
		`{"max_results":26}`,
		`{"max_uses":31}`,
		`{"engine":"Not A Name"}`,
		`{"unknown":1}`,
		`{"allowed_domains":[""]}`,
		`[]`,
	} {
		body := []byte(`{"model":"m","tools":[{"type":"openrouter:web_search","parameters":` + parameters + `}]}`)
		if _, err := protocol.Inspect(protocol.Chat, body); !errors.Is(err, protocol.ErrInvalid) {
			t.Errorf("parameters %s: %v, want invalid", parameters, err)
		}
	}
	if _, err := protocol.Inspect(protocol.Chat, []byte(`{"model":"m","max_tool_calls":31,"messages":[]}`)); !errors.Is(err, protocol.ErrInvalid) {
		t.Errorf("max_tool_calls 31: %v", err)
	}
	if _, err := protocol.Inspect(protocol.Chat, []byte(`{"model":"m","stop_server_tools_when":{"tool_name":"x"},"messages":[]}`)); !errors.Is(err, protocol.ErrInvalid) {
		t.Errorf("stop_server_tools_when: %v", err)
	}
	if _, err := protocol.Inspect(protocol.Chat, []byte(`{"model":"m","tools":[{"type":"openrouter:web_search"},{"type":"openrouter:web_search"}]}`)); !errors.Is(err, protocol.ErrInvalid) {
		t.Errorf("duplicate tool: %v", err)
	}
}

func TestRemoveServerTool(t *testing.T) {
	body := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"keep"}},{"type":"openrouter:web_search","parameters":{"max_uses":2}}],"messages":[{"role":"user","content":"hi"}]}`)
	out, err := protocol.RemoveServerTool(protocol.Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "openrouter:web_search") || !strings.Contains(string(out), `"name":"keep"`) ||
		!strings.Contains(string(out), `"content":"hi"`) {
		t.Fatalf("stripped body = %s", out)
	}
	req, err := protocol.Inspect(protocol.Chat, out)
	if err != nil || req.ServerSearch != nil || !req.Needs.Has(protocol.Tools) {
		t.Fatalf("stripped request = %+v, %v", req, err)
	}
	// A body without the tool is returned as it is.
	if again, err := protocol.RemoveServerTool(protocol.Chat, out); err != nil || string(again) != string(out) {
		t.Fatalf("second removal changed the body: %v", err)
	}
}

func TestAppendServerTurn(t *testing.T) {
	calls := []protocol.ServerToolCall{{ID: "call_1", Arguments: `{"query":"axiom"}`}}
	results := []string{"[1] Example\nhttps://example.com\nText."}
	for _, tc := range []struct {
		format protocol.Format
		body   string
		want   []string
	}{
		{
			protocol.Chat, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			[]string{`"tool_calls"`, `"role":"tool"`, `"tool_call_id":"call_1"`, `"content":"[1] Example`},
		},
		{
			protocol.Messages, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			[]string{`"type":"tool_use"`, `"input":{"query":"axiom"}`, `"type":"tool_result"`},
		},
		{
			protocol.Responses, `{"model":"m","input":[{"role":"user","content":"hi"}]}`,
			[]string{`"type":"function_call"`, `"call_id":"call_1"`, `"type":"function_call_output"`},
		},
		{
			protocol.Responses, `{"model":"m","input":"hi"}`,
			[]string{`"role":"user","content":"hi"`, `"type":"function_call_output"`},
		},
	} {
		out, err := protocol.AppendServerTurn(tc.format, []byte(tc.body), protocol.ServerTurn{Calls: calls}, results)
		if err != nil {
			t.Fatalf("%d: %v", tc.format, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(string(out), want) {
				t.Errorf("%d: %s misses %s", tc.format, out, want)
			}
		}
		if _, err := protocol.Inspect(tc.format, out); err != nil {
			t.Errorf("%d: appended body is invalid: %v: %s", tc.format, err, out)
		}
	}
}

func TestInjectServerFunction(t *testing.T) {
	for _, tc := range []struct {
		format     protocol.Format
		body, path string
	}{
		{protocol.Chat, `{"model":"m","messages":[]}`, `/tools/0/function/name`},
		{protocol.Chat, `{"model":"m","tools":[{"type":"function","function":{"name":"keep"}}],"messages":[]}`, `/tools/1/function/name`},
		{protocol.Messages, `{"model":"m","tools":[{"name":"keep","input_schema":{"type":"object"}}],"messages":[]}`, `/tools/1/name`},
		{protocol.Responses, `{"model":"m","tools":[{"type":"function","name":"keep"}],"input":[]}`, `/tools/1/name`},
	} {
		out, err := protocol.InjectServerFunctionForTest(tc.format, []byte(tc.body))
		if err != nil {
			t.Fatalf("%d: %v", tc.format, err)
		}
		var doc any
		if err := jsonx.Unmarshal(out, &doc); err != nil {
			t.Fatalf("%d: %v: %s", tc.format, err, out)
		}
		if !strings.Contains(string(out), `"`+protocol.ServerToolName+`"`) {
			t.Errorf("%d: %s misses the function", tc.format, out)
		}
	}
}

func TestServerFunctionDeclared(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format protocol.Format
		body   string
		want   bool
	}{
		{"chat function", protocol.Chat, `{"tools":[{"type":"function","function":{"name":"openrouter_web_search"}}]}`, true},
		{"chat server tool", protocol.Chat, `{"tools":[{"type":"openrouter:web_search"}]}`, false},
		{"messages function", protocol.Messages, `{"tools":[{"name":"openrouter_web_search"}]}`, true},
		{"responses function", protocol.Responses, `{"tools":[{"type":"function","name":"openrouter_web_search"}]}`, true},
		{"another name", protocol.Chat, `{"tools":[{"type":"function","function":{"name":"search"}}]}`, false},
	} {
		if got := protocol.ServerFunctionDeclared(tc.format, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: declared = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAppendServerTurnNormalizesArguments(t *testing.T) {
	calls := []protocol.ServerToolCall{{ID: "call_1", Arguments: `{"query":`}}
	for _, f := range []protocol.Format{protocol.Chat, protocol.Messages, protocol.Responses} {
		body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"input":[{"role":"user","content":"hi"}]}`
		out, err := protocol.AppendServerTurn(f, []byte(body), protocol.ServerTurn{Calls: calls}, []string{"no result"})
		if err != nil {
			t.Fatalf("%d: %v", f, err)
		}
		if _, err := protocol.Inspect(f, out); err != nil {
			t.Errorf("%d: malformed arguments made the body invalid: %v: %s", f, err, out)
		}
	}
}

func TestRemoveLoopControls(t *testing.T) {
	for _, tc := range []struct {
		body, want string
	}{
		{`{"model":"m","max_tool_calls":3,"messages":[]}`, `{"model":"m","messages":[]}`},
		{`{"max_tool_calls":3}`, `{}`},
		{`{"model":"m","max_tool_calls":3,"tool_choice":"required","messages":[]}`, `{"model":"m","messages":[]}`},
	} {
		body, err := protocol.RemoveMaxToolCalls([]byte(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		body, err = protocol.DropForcedToolChoice(protocol.Chat, body)
		if err != nil || string(body) != tc.want {
			t.Fatalf("%s -> %s (%v), want %s", tc.body, body, err, tc.want)
		}
	}
	withTool := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"required","messages":[]}`)
	if out, err := protocol.DropForcedToolChoice(protocol.Chat, withTool); err != nil || !strings.Contains(string(out), "tool_choice") {
		t.Fatalf("a client's forced choice was removed: %s (%v)", out, err)
	}
}
