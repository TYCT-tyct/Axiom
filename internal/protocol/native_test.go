package protocol

import (
	"strings"
	"testing"
)

//nolint:gocognit // the table and its assertions are the test
func TestInjectNativeSearch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format Format
		body   string
		uses   int
		want   []string
		absent []string
	}{
		{
			name: "chat adds the switch", format: Chat, uses: 1,
			body: `{"model":"m","messages":[]}`,
			want: []string{`"web_search_options":{}`},
		},
		{
			name: "chat keeps its own switch", format: Chat, uses: 1,
			body: `{"model":"m","web_search_options":{"search_context_size":"low"}}`,
			want: []string{`"search_context_size":"low"`},
		},
		{
			name: "responses adds a tool", format: Responses, uses: 1,
			body: `{"model":"m","input":[]}`,
			want: []string{`"tools":[{"type":"web_search"}]`, `"max_tool_calls":1`},
		},
		{
			name: "responses appends after a tool", format: Responses, uses: 1,
			body: `{"model":"m","tools":[{"type":"function","name":"f"}]}`,
			want: []string{`{"type":"function","name":"f"},{"type":"web_search"}`, `"max_tool_calls":1`},
		},
		{
			name: "responses replaces null", format: Responses, uses: 1,
			body: `{"model":"m","tools":null}`,
			want: []string{`"tools":[{"type":"web_search"}]`, `"max_tool_calls":1`},
		},
		{
			name: "responses keeps its own search", format: Responses, uses: 1,
			body: `{"model":"m","tools":[{"type":"web_search_preview"}]}`,
			want: []string{`{"type":"web_search_preview"}`}, absent: []string{`"type":"web_search"}`},
		},
		{
			name: "responses uses the requested bound", format: Responses, uses: 3,
			body: `{"model":"m","input":[]}`,
			want: []string{`"max_tool_calls":3`},
		},
		{
			name: "responses keeps a stricter call limit", format: Responses, uses: 3,
			body: `{"model":"m","input":[],"max_tool_calls":2}`,
			want: []string{`"max_tool_calls":2`}, absent: []string{`"max_tool_calls":3`},
		},
		{
			name: "responses replaces a looser call limit", format: Responses, uses: 3,
			body: `{"model":"m","input":[],"max_tool_calls":99}`,
			want: []string{`"max_tool_calls":3`},
		},
		{
			name: "chat keeps its own tool", format: Chat, uses: 1,
			body: `{"model":"m","tools":[{"type":"web_search"}]}`,
			want: []string{`"type":"web_search"`}, absent: []string{`"web_search_options"`},
		},
		{
			name: "messages uses max uses", format: Messages, uses: 3,
			body: `{"model":"m","max_tokens":1,"messages":[]}`,
			want: []string{`"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":3}]`},
		},
		{
			name: "messages keeps its own search", format: Messages, uses: 2,
			body: `{"model":"m","tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
			want: []string{`{"type":"web_search_20250305","name":"web_search"}`}, absent: []string{`"max_uses":2`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := injectNativeSearch(tc.format, []byte(tc.body), tc.uses)
			if err != nil {
				t.Fatalf("injectNativeSearch() error = %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(string(got), want) {
					t.Errorf("body %s does not contain %s", got, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(string(got), absent) {
					t.Errorf("body %s contains %s", got, absent)
				}
			}
		})
	}
}

func TestInjectNativeSearchRefusesWhatItCannotRewrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format Format
		body   string
	}{
		{"completions has no native search", Completions, `{"model":"m"}`},
		{"tools is not an array", Responses, `{"model":"m","tools":{}}`},
		{"not an object", Chat, `[]`},
		{"empty", Chat, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := injectNativeSearch(tc.format, []byte(tc.body), 1); err == nil {
				t.Fatal("injectNativeSearch() succeeded")
			}
		})
	}
}

func TestPrepareBudgetIncludesTheNativeTool(t *testing.T) {
	r := &Request{Format: Chat}
	body := []byte(`{"model":"m","messages":[]}`)
	target := Target{Format: Chat, Model: "m"}
	without, _, err := PrepareBudgeted(NewMemory(1<<20), r, body, target, Rewrites{})
	if err != nil {
		t.Fatal(err)
	}
	with, _, err := PrepareBudgeted(NewMemory(1<<20), r, body, target, Rewrites{NativeSearch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(with.Body) <= len(without.Body) {
		t.Fatalf("a body with a native tool is %d bytes, without it %d", len(with.Body), len(without.Body))
	}
}
