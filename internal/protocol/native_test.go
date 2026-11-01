package protocol

import (
	"fmt"
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
			got, err := injectNativeSearch(tc.format, []byte(tc.body), tc.uses, nil)
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
			if _, err := injectNativeSearch(tc.format, []byte(tc.body), 1, nil); err == nil {
				t.Fatal("injectNativeSearch() succeeded")
			}
		})
	}
}

// TestInjectNativeSearchKnowsEveryEscape: a tool already searching is one whatever the escapes of its type, so the
// search is never added twice; a Messages type longer than the scan's own array is decoded too.
func TestInjectNativeSearchKnowsEveryEscape(t *testing.T) {
	escaped := func(s string) string {
		var b strings.Builder
		for _, c := range []byte(s) {
			fmt.Fprintf(&b, `\u%04x`, c)
		}
		return b.String()
	}
	for _, tc := range []struct {
		format       Format
		kind, tool   string
		maxUses, use int
	}{
		{Responses, "web_search", `{"type":"web_search"}`, 1, 1},
		{Responses, "web_search_preview", `{"type":"web_search"}`, 1, 1},
		{Chat, "web_search", `"web_search_options"`, 0, 1},
		{Messages, "web_search_20250305", `"max_uses":2`, 0, 2},
		{Messages, "web_search_" + strings.Repeat("9", 200), `"max_uses":2`, 0, 2},
	} {
		for _, spelling := range []string{tc.kind, escaped(tc.kind), escaped(tc.kind[:5]) + tc.kind[5:]} {
			body := `{"model":"m","tools":[{"type":"function","name":"f"},{"type":"` + spelling + `","name":"web_search"}]}`
			got, err := injectNativeSearch(tc.format, []byte(body), tc.use, nil)
			if err != nil {
				t.Fatalf("%v %s: %v", tc.format, spelling, err)
			}
			if n := strings.Count(string(got), tc.tool); n != strings.Count(body, tc.tool) {
				t.Errorf("%v %s: the search was added again: %s", tc.format, spelling, got)
			}
		}
	}
}
