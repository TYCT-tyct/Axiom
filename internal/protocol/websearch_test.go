package protocol_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

func TestInspectWebPlugin(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"m","plugins":[{"id":"web","max_results":7,"search_prompt":"Use these.","include_domains":["Example.COM"],"exclude_domains":["bad.example"]}]}`)
	req := inspected(t, protocol.Chat, body)
	w := req.Web
	if w == nil || w.MaxResults != 7 || w.SearchPrompt != "Use these." || w.Online {
		t.Fatalf("web = %+v", w)
	}
	if !slices.Equal(w.IncludeDomains, []string{"example.com"}) || !slices.Equal(w.ExcludeDomains, []string{"bad.example"}) {
		t.Fatalf("domains = %+v / %+v", w.IncludeDomains, w.ExcludeDomains)
	}
	if req.SearchBytes != 0 {
		t.Fatalf("SearchBytes = %d, want 0 from Inspect", req.SearchBytes)
	}
}

func TestInspectRefusesBadWebPlugins(t *testing.T) {
	t.Parallel()
	for _, bad := range []struct{ name, plugin string }{
		{"too many results", `{"id":"web","max_results":11}`},
		{"wildcard", `{"id":"web","include_domains":["*.example.com"]}`},
		{"a path", `{"id":"web","exclude_domains":["example.com/path"]}`},
		{"a bad engine name", `{"id":"web","engine":"Native"}`},
		{"a bad prompt", `{"id":"web","search_prompt":"` + strings.Repeat("x", 513) + `"}`},
	} {
		t.Run(bad.name, func(t *testing.T) {
			if _, err := protocol.Inspect(protocol.Chat, []byte(`{"model":"m","plugins":[`+bad.plugin+`]}`)); err == nil {
				t.Fatal("Inspect() accepted the plugin")
			}
		})
	}
	if _, err := protocol.Inspect(protocol.Messages, []byte(`{"model":"m","plugins":[{"id":"web"}]}`)); err == nil {
		t.Fatal("Inspect() accepted web outside Chat")
	}
	req, err := protocol.Inspect(protocol.Chat, []byte(`{"model":"m","plugins":[{"id":"web","enabled":false}]}`))
	if err != nil || req.Web != nil {
		t.Fatalf("a disabled web plugin = %+v, %v", req.Web, err)
	}
}

func TestChatSearchQuery(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":[{"type":"text","text":"first"}]},{"role":"assistant","content":"a"},{"role":"user","content":[{"type":"text","text":"second\nquestion"}]}]}`)
	got, err := protocol.ChatSearchQuery(body)
	if err != nil || got != "second\nquestion" {
		t.Fatalf("ChatSearchQuery() = %q, %v", got, err)
	}
	if got, err := protocol.ChatSearchQuery([]byte(`{"model":"m","messages":[{"role":"system","content":"s"}]}`)); err != nil || got != "" {
		t.Fatalf("ChatSearchQuery() without a user = %q, %v", got, err)
	}
}

// message is a Chat message as the injected body carries it.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// TestInjectWebSearch: the results become a system message before the first user turn, so a format that only takes
// them at the front keeps them, and the search still answered the last user turn.
func TestInjectWebSearch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		text string
		want []message
	}{
		{
			name: "after an existing system message",
			body: `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello"}]}`,
			text: "A web search was conducted.",
			want: []message{{"system", "be brief"}, {"system", "A web search was conducted."}, {"user", "hello"}},
		},
		{
			name: "before a second turn",
			body: `{"model":"m","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"a"},{"role":"user","content":"second"}]}`,
			text: "results",
			want: []message{{"system", "results"}, {"user", "first"}, {"assistant", "a"}, {"user", "second"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := protocol.InjectWebSearch([]byte(tc.body), tc.text)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Model    string    `json:"model"`
				Messages []message `json:"messages"`
			}
			if err := jsonx.Unmarshal(out, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Model != "m" {
				t.Fatalf("the injected body lost the model: %s", out)
			}
			if !slices.Equal(doc.Messages, tc.want) {
				t.Fatalf("injected messages = %+v, want %+v", doc.Messages, tc.want)
			}
		})
	}
}

func TestCitationAnnotations(t *testing.T) {
	t.Parallel()
	content := "See [example.com](https://example.com/one) and [other](https://example.org/two), not [x](https://other.example)."
	got := protocol.CitationAnnotations(content, []protocol.Citation{
		{URL: "https://example.com/one", Title: "One", Content: "first"},
		{URL: "https://example.org/two", Title: "Two", Content: "second"},
	})
	var entries []struct {
		Type        string `json:"type"`
		URLCitation struct {
			URL        string `json:"url"`
			Title      string `json:"title"`
			Content    string `json:"content"`
			StartIndex int    `json:"start_index"`
			EndIndex   int    `json:"end_index"`
		} `json:"url_citation"`
	}
	if err := jsonx.Unmarshal(append(append([]byte{'['}, got...), ']'), &entries); err != nil {
		t.Fatalf("annotations are not an array: %v: %s", err, got)
	}
	if len(entries) != 2 || entries[0].Type != "url_citation" || entries[0].URLCitation.URL != "https://example.com/one" || entries[1].URLCitation.URL != "https://example.org/two" {
		t.Fatalf("annotations = %+v", entries)
	}
	// "See " is 4 characters, so the first link starts at 4 and its whole span ends at 4+len("[example.com](https://example.com/one)").
	if got, want := entries[0].URLCitation.StartIndex, 4; got != want {
		t.Errorf("start_index = %d, want %d", got, want)
	}
	if got, want := entries[0].URLCitation.EndIndex, 4+len("[example.com](https://example.com/one)"); got != want {
		t.Errorf("end_index = %d, want %d", got, want)
	}
}

func TestValidSearchDomain(t *testing.T) {
	t.Parallel()
	for d, want := range map[string]bool{
		"example.com": true, "sub.example.co.uk": true, "example": false, "*.example.com": false,
		"https://example.com": false, "exa mple.com": false, ".example.com": false, "example..com": false,
	} {
		if got := protocol.ValidSearchDomain(d); got != want {
			t.Errorf("ValidSearchDomain(%q) = %v, want %v", d, got, want)
		}
	}
}

func TestChatSearchQueryScansLongHistoryAndBoundsTheQuery(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString(`{"model":"m","messages":[`)
	for i := range 500 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"role":"assistant","content":"history %d"},{"role":"user","content":"old %d"}`, i, i)
	}
	b.WriteString(`,{"content":"last question","role":"user"}]}`)
	got, err := protocol.ChatSearchQuery([]byte(b.String()))
	if err != nil || got != "last question" {
		t.Fatalf("ChatSearchQuery() = %q, %v", got, err)
	}
	if _, err := protocol.ChatSearchQuery([]byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", protocol.MaxWebSearchQueryBytes+1) + `"}]}`)); err == nil {
		t.Fatal("ChatSearchQuery() accepted a query over the bound")
	}
	got, err = protocol.ChatSearchQuery([]byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a"}},{"type":"text","text":"only text"}]}]}`))
	if err != nil || got != "only text" {
		t.Fatalf("non-text parts = %q, %v", got, err)
	}
}

func TestChatSearchQueryIgnoresLongEarlierMessagesAndMemberOrder(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", protocol.MaxWebSearchQueryBytes+1)
	cases := map[string]string{
		"long earlier user message":          `{"messages":[{"role":"user","content":"` + long + `"},{"role":"assistant","content":"ok"},{"role":"user","content":"hello"}]}`,
		"long assistant content before role": `{"messages":[{"content":"` + long + `","role":"assistant"},{"role":"user","content":"hello"}]}`,
		"long assistant content after role":  `{"messages":[{"role":"assistant","content":"` + long + `"},{"role":"user","content":"hello"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := protocol.ChatSearchQuery([]byte(body))
			if err != nil || got != "hello" {
				t.Fatalf("ChatSearchQuery() = %q, %v; want hello", got, err)
			}
		})
	}
	longUser := `{"messages":[{"role":"assistant","content":"ok"},{"role":"user","content":"` + long + `"}]}`
	if _, err := protocol.ChatSearchQuery([]byte(longUser)); err == nil {
		t.Fatal("ChatSearchQuery() accepted a long final user message")
	}
	beforeRole := `{"messages":[{"content":"` + long + `","role":"user"}]}`
	if _, err := protocol.ChatSearchQuery([]byte(beforeRole)); err == nil {
		t.Fatal("ChatSearchQuery() accepted a long final user message written before its role")
	}
}
