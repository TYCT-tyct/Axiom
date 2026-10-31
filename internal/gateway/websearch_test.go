package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
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

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10,"server_tool_use":{"web_search_requests":0}}}

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

// TestWebPluginSearchBudgetRefusedBeforeSearch: the adapter may read a 4 MiB response and decode it, so the call asks
// the node's budget for that working set before searching. A node without room answers 503 without running the
// search, reaching the upstream or settling an attempt, and gives the budget back when the call is over.
func TestWebPluginSearchBudgetRefusedBeforeSearch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com/one", Title: "One", Content: "text"}}}
		body := []byte(`{"model":"acme/alpha","plugins":[{"id":"web"}],"messages":[{"role":"user","content":"what is axiom"}]}`)
		upstream := protocol.Chat
		h := newHarness(t, t.Context(), options{
			client:   protocol.Chat,
			upstream: &upstream,
			request:  body,
			memory:   1 << 20,
			price:    `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			search:   searcher,
		})
		h.run()
		if h.w.status != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, body = %s, want 503", h.w.status, h.w.body())
		}
		if len(searcher.queries) != 0 {
			t.Fatalf("the backend ran %d searches, want none", len(searcher.queries))
		}
		if len(h.up.endpoint(1).calls) != 0 {
			t.Fatal("a search without memory reached the upstream")
		}
		if n := len(h.settlements[0].outcome.Attempts); n != 0 {
			t.Fatalf("a search without memory settled %d attempts, want none", n)
		}
	})
}

// captureCosts records the usage the supplier's price is computed from.
type captureCosts struct{ usage []catalog.Usage }

func (c *captureCosts) Credits(_ suppliers.AttemptReceipt, usage []catalog.Usage, _ platform.Currency) (suppliers.AttemptCost, int64, bool, error) {
	c.usage = append(c.usage, usage...)
	return suppliers.AttemptCost{Flag: suppliers.CostComplete}, 0, true, nil
}

// TestExternalSearchStaysOutOfSupplierCost: after a retry, the external search is charged to the user once, on the
// charged attempt, while the supplier cost of every attempt is computed from the supplier's own usage only. An
// upstream that reports zero searches explicitly must not hide the external one either.
func TestExternalSearchStaysOutOfSupplierCost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com/one", Title: "One", Content: "text"}}}
		costs := &captureCosts{}
		body := []byte(`{"model":"acme/alpha","plugins":[{"id":"web"}],"messages":[{"role":"user","content":"what is axiom"}]}`)
		upstream := protocol.Chat
		h := newHarness(t, t.Context(), options{
			client:   protocol.Chat,
			upstream: &upstream,
			entries:  2,
			request:  body,
			price:    `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			search:   searcher,
			costs:    costs,
			script: map[int][]scripted{
				1: {failure(routing.ScopeEndpoint, true)},
				2: {serve(beat{data: chatCited})},
			},
		})
		h.run()
		if h.w.status != 200 {
			t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
		}
		if len(searcher.queries) != 1 {
			t.Fatalf("the backend ran %d searches across the retry, want 1", len(searcher.queries))
		}
		for i, u := range costs.usage {
			if got := u.Count(catalog.ItemWebSearch); got != 0 {
				t.Errorf("supplier usage of segment %d has %d external searches, want 0", i, got)
			}
		}
		settled := h.settlements[0].outcome.Attempts
		if got := settled[len(settled)-1].Segments[0].Usage.Count(catalog.ItemWebSearch); got != 1 {
			t.Errorf("the user was charged %d searches, want 1", got)
		}
	})
}
