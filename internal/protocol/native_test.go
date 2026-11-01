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
			want: []string{`"tools":[{"type":"web_search"}]`},
		},
		{
			name: "responses appends after a tool", format: Responses, uses: 1,
			body: `{"model":"m","tools":[{"type":"function","name":"f"}]}`,
			want: []string{`{"type":"function","name":"f"},{"type":"web_search"}`},
		},
		{
			name: "responses replaces null", format: Responses, uses: 1,
			body: `{"model":"m","tools":null}`,
			want: []string{`"tools":[{"type":"web_search"}]`},
		},
		{
			name: "responses keeps its own search", format: Responses, uses: 1,
			body: `{"model":"m","tools":[{"type":"web_search_preview"}]}`,
			want: []string{`{"type":"web_search_preview"}`}, absent: []string{`"type":"web_search"}`},
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

func TestPrepareCostIncludesTheNativeTool(t *testing.T) {
	r := &Request{Format: Chat}
	body := []byte(`{"model":"m","messages":[]}`)
	without := PrepareCost(r, body, Target{Format: Chat, Model: "m"}, Rewrites{})
	with := PrepareCost(r, body, Target{Format: Chat, Model: "m"}, Rewrites{NativeSearch: 1})
	if with <= without {
		t.Fatalf("PrepareCost() with a native tool = %d, want more than %d", with, without)
	}
}
