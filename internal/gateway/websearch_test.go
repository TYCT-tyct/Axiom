package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/websearch"
)

type recordingSearcher struct {
	queries []websearch.Query
	results []websearch.Result
	err     error
}

func (s *recordingSearcher) Search(_ context.Context, q websearch.Query) ([]websearch.Result, error) {
	s.queries = append(s.queries, q)
	return s.results, s.err
}

const chatCited = `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{"content":"See [example.com](https://example.com/one)."},"finish_reason":null}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}

data: [DONE]

`

func TestWebPluginSearchesInjectsAndAnnotates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		searcher := &recordingSearcher{results: []websearch.Result{{
			URL: "https://example.com/one", Title: "Example One", Content: "The first recorded result.",
		}}}
		body := []byte(`{"model":"acme/alpha","plugins":[{"id":"web","max_results":3}],"messages":[{"role":"user","content":"what is axiom"}],"stream":false}`)
		upstream := protocol.Chat
		h := newHarness(t, t.Context(), options{
			client:   protocol.Chat,
			upstream: &upstream,
			request:  body,
			price:    `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			search:   searcher,
			script:   map[int][]scripted{1: {serve(beat{data: chatCited})}},
		})
		h.run()

		if len(searcher.queries) != 1 {
			t.Fatalf("the backend ran %d searches, want 1", len(searcher.queries))
		}
		if q := searcher.queries[0]; q.Text != "what is axiom" || q.MaxResults != 3 {
			t.Fatalf("search query = %+v", q)
		}
		calls := h.up.endpoint(1).calls
		if len(calls) != 1 {
			t.Fatalf("the upstream got %d calls, want 1", len(calls))
		}
		sent := string(calls[0].Body)
		for _, want := range []string{`"role":"system"`, "The first recorded result.", "https://example.com/one"} {
			if !strings.Contains(sent, want) {
				t.Errorf("the upstream body does not contain %q: %s", want, sent)
			}
		}
		if strings.Contains(sent, `"plugins"`) {
			t.Errorf("the upstream body still carries the plugin control field: %s", sent)
		}
		if h.w.status != 200 {
			t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
		}
		var reply struct {
			Choices []struct {
				Message struct {
					Annotations []struct {
						Type        string `json:"type"`
						URLCitation struct {
							URL string `json:"url"`
						} `json:"url_citation"`
					} `json:"annotations"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(h.w.body()), &reply); err != nil {
			t.Fatalf("the reply is not JSON: %v: %s", err, h.w.body())
		}
		if len(reply.Choices) != 1 || len(reply.Choices[0].Message.Annotations) != 1 ||
			reply.Choices[0].Message.Annotations[0].Type != "url_citation" ||
			reply.Choices[0].Message.Annotations[0].URLCitation.URL != "https://example.com/one" {
			t.Fatalf("annotations = %+v, body = %s", reply.Choices, h.w.body())
		}
		usage := h.settlements[0].outcome.Attempts[0].Segments[0].Usage
		if got := usage.Count(catalog.ItemWebSearch); got != 1 {
			t.Fatalf("settled web searches = %d, want 1", got)
		}
	})
}

func TestWebPluginSearchFailureSearchesNothingElse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		searcher := &recordingSearcher{err: websearch.ErrUpstream}
		body := []byte(`{"model":"acme/alpha","plugins":[{"id":"web"}],"messages":[{"role":"user","content":"what is axiom"}]}`)
		upstream := protocol.Chat
		h := newHarness(t, t.Context(), options{
			client:   protocol.Chat,
			upstream: &upstream,
			request:  body,
			price:    `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			search:   searcher,
		})
		h.run()
		if h.w.status != 502 {
			t.Fatalf("status = %d, body = %s, want 502", h.w.status, h.w.body())
		}
		if len(h.up.endpoint(1).calls) != 0 {
			t.Fatal("a failed search still reached the upstream")
		}
		if n := len(h.settlements[0].outcome.Attempts); n != 0 {
			t.Fatalf("a failed search settled %d attempts, want none: %+v", n, h.settlements[0].outcome)
		}
	})
}

func TestOnlineReachesTheNodeAsAWebRequest(t *testing.T) {
	r := newRig(t, func(o *rigOptions) {
		o.search = &recordingSearcher{}
		o.price = `{"v":1,"currency":"USD",
			"rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","image_in":"0.000001","web_search":"0.01"},
			"service_tiers":{"priority":{"rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","image_in":"0.000001","web_search":"0.01"}}}}`
	})
	a := r.newAccount(1e9)
	resp, out := r.do(http.MethodPost, "/v1/chat/completions", a.key,
		strings.NewReader(`{"model":"acme/alpha:online","messages":[{"role":"user","content":"what is axiom"}]}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, out)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.admits) != 1 || r.admits[0].Request.Web == nil || !r.admits[0].Request.Web.Online {
		t.Fatalf("admitted %+v, want one :online request", r.admits)
	}
	if got := r.admits[0].Request.SearchBytes; got != protocol.WebSearchMaxBytes {
		t.Fatalf("reserved search bytes = %d, want %d", got, protocol.WebSearchMaxBytes)
	}
}
