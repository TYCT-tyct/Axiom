package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/websearch"
)

const chatToolCall = `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"openrouter_web_search","arguments":"{\"query\":\"axiom\"}"}}]},"finish_reason":null}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}

data: [DONE]

`

const chatToolCallTwo = `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"openrouter_web_search","arguments":"{\"query\":\"gateway\"}"}}]},"finish_reason":null}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[],"usage":{"prompt_tokens":14,"completion_tokens":4,"total_tokens":18}}

data: [DONE]

`

const chatAnswer = `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{"content":"Axiom is a gateway."},"finish_reason":null}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}

data: [DONE]

`

const searchTool = `{"model":"acme/alpha","stream":true,"messages":[{"role":"user","content":"what is axiom"}],"tools":[{"type":"openrouter:web_search","parameters":{"engine":"exa","max_results":2}}]}`

// serverHarness runs the loop in the executor with one backend named test (which the tool's engine names as exa: the
// harness bypasses admission's engine check, as it bypasses routing).
func serverHarness(t *testing.T, ctx context.Context, searcher websearch.Searcher, scripts ...scripted) *harness {
	t.Helper()
	upstream := protocol.Chat
	return newHarness(t, ctx, options{
		client: protocol.Chat, upstream: &upstream, request: []byte(searchTool),
		price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
		search: searcher, server: &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2},
		script: map[int][]scripted{1: scripts},
	})
}

//nolint:cyclop,gocognit // one assertion per hop of the loop
func TestServerLoopSearchesAndAnswersInOneStream(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		searcher := &recordingSearcher{results: []websearch.Result{
			{URL: "https://example.com/one", Title: "Example One", Content: "The first result."},
			{URL: "https://example.com/two", Title: "Example Two", Content: "The second result."},
		}}
		h := serverHarness(t, ctx, searcher, serve(beat{data: chatToolCall}), serve(beat{data: chatAnswer}))
		h.run()

		if h.w.status != http.StatusOK {
			t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
		}
		if len(searcher.queries) != 1 || searcher.queries[0].Text != "axiom" || searcher.queries[0].MaxResults != 2 {
			t.Fatalf("searches = %+v", searcher.queries)
		}
		calls := h.up.endpoint(1).calls
		if len(calls) != 2 {
			t.Fatalf("the upstream got %d calls, want 2", len(calls))
		}
		if !strings.Contains(string(calls[0].Body), `"name":"openrouter_web_search"`) ||
			!strings.Contains(string(calls[0].Body), `"type":"function"`) {
			t.Fatalf("the first hop does not declare the function: %s", calls[0].Body)
		}
		if strings.Contains(string(calls[0].Body), "openrouter:web_search") {
			t.Fatalf("the first hop still carries the tool declaration: %s", calls[0].Body)
		}
		second := string(calls[1].Body)
		if !strings.Contains(second, `"role":"tool"`) || !strings.Contains(second, "Search results for") ||
			!strings.Contains(second, "https://example.com/one") {
			t.Fatalf("the second hop does not carry the result: %s", second)
		}
		if !strings.Contains(h.w.body(), "Axiom is a gateway.") || strings.Contains(h.w.body(), "call_1") ||
			strings.Count(h.w.body(), "[DONE]") != 1 {
			t.Fatalf("the client saw the tool hop or more than one stream: %s", h.w.body())
		}
		out := h.settlements[0].outcome
		if len(out.Attempts) != 1 || len(out.Attempts[0].Segments) != 2 {
			t.Fatalf("settled attempts = %+v, want one attempt with two samplings", out.Attempts)
		}
		if got := out.Attempts[0].Segments[0].Usage.Count(catalog.ItemWebSearch); got != 1 {
			t.Fatalf("settled searches = %d, want 1", got)
		}
	})
}

func TestServerLoopTwoSteps(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}}
		h := serverHarness(t, ctx, searcher,
			serve(beat{data: chatToolCall}), serve(beat{data: chatToolCallTwo}), serve(beat{data: chatAnswer}))
		h.run()
		if len(h.up.endpoint(1).calls) != 3 || len(searcher.queries) != 2 {
			t.Fatalf("calls = %d, searches = %d, want 3 and 2", len(h.up.endpoint(1).calls), len(searcher.queries))
		}
		if got := h.settlements[0].outcome.Attempts[0].Segments[0].Usage.Count(catalog.ItemWebSearch); got != 2 {
			t.Fatalf("settled searches = %d, want 2", got)
		}
	})
}

func TestServerLoopStepCapDropsTheFunction(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}}
		h := newHarness(t, ctx, options{
			client: protocol.Chat, request: []byte(strings.Replace(searchTool, `"max_results":2`, `"max_results":2,"max_uses":1`, 1)),
			price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			search: searcher, server: &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2, MaxUses: 1},
			script: map[int][]scripted{1: {serve(beat{data: chatToolCall}), serve(beat{data: chatAnswer})}},
		})
		h.run()
		if len(h.up.endpoint(1).calls) != 2 {
			t.Fatalf("the upstream got %d calls, want 2", len(h.up.endpoint(1).calls))
		}
		if body := string(h.up.endpoint(1).calls[1].Body); !strings.Contains(body, `"tools":[]`) {
			t.Fatalf("the final hop still declares a tool: %s", body)
		}
		if h.w.status != http.StatusOK || !strings.Contains(h.w.body(), "Axiom is a gateway.") {
			t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
		}
	})
}

func TestServerLoopBackendFailureWaivesModelUsage(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		searcher := &recordingSearcher{err: errors.New("backend down")}
		h := serverHarness(t, ctx, searcher, serve(beat{data: chatToolCall}))
		h.run()
		if h.w.status != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
		}
		if len(h.up.endpoint(1).calls) != 1 {
			t.Fatalf("the upstream got %d calls, want only the tool hop", len(h.up.endpoint(1).calls))
		}
		rec := h.settlements[0].outcome.Attempts[0]
		if rec.Result != billing.FailedBeforeCommit || rec.Finish != billing.FinishError {
			t.Fatalf("attempt = %+v, want failed before commit", rec)
		}
		if got := rec.Segments[0].Usage.Count(catalog.ItemWebSearch); got != 0 {
			t.Fatalf("a search that failed was charged: %d", got)
		}
	})
}

func TestServerLoopUpstreamFailureIsUpstreamError(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}}
		h := serverHarness(t, ctx, searcher, serve(beat{data: chatToolCall}), failure(routing.ScopeEndpoint, false))
		h.run()
		if h.w.status != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s, want 502 after the search", h.w.status, h.w.body())
		}
		rec := h.settlements[0].outcome.Attempts[0]
		if rec.Result != billing.FailedBeforeCommit || rec.Finish != billing.FinishError {
			t.Fatalf("attempt = %+v, want failed before commit", rec)
		}
		if got := rec.Segments[0].Usage.Count(catalog.ItemWebSearch); got != 1 {
			t.Fatalf("the successful search was not charged: %d", got)
		}
	})
}

func TestServerLoopClientLeavesDuringSearch(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		started, stopped := make(chan struct{}), make(chan struct{})
		searcher := &blockingSearcher{started: started, stopped: stopped}
		h := serverHarness(t, ctx, searcher, serve(beat{data: chatToolCall}))
		done := h.start()
		<-started
		h.client()
		<-done
		<-stopped
		h.check()
		if len(h.up.endpoint(1).calls) != 1 {
			t.Fatalf("the upstream got %d calls after the client left", len(h.up.endpoint(1).calls))
		}
		rec := h.settlements[0].outcome.Attempts[0]
		if rec.Finish != billing.FinishCancelled {
			t.Fatalf("finish = %v, want cancelled", rec.Finish)
		}
	})
}

type blockingSearcher struct {
	started, stopped chan struct{}
}

func (s *blockingSearcher) Search(ctx context.Context, _ websearch.Query) ([]websearch.Result, error) {
	close(s.started)
	<-ctx.Done()
	close(s.stopped)
	return nil, ctx.Err()
}

func TestServerLoopAggregateCarriesTheFinalReply(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}}
		body := strings.Replace(strings.Replace(searchTool, `"stream":true`, `"stream":false`, 1),
			`"max_results":2`, `"max_results":2`, 1)
		h := newHarness(t, ctx, options{
			client: protocol.Chat, request: []byte(body), search: searcher,
			price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			server: &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2},
			script: map[int][]scripted{1: {serve(beat{data: chatToolCall}), serve(beat{data: chatAnswer})}},
		})
		h.run()
		var reply struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := jsonx.Unmarshal([]byte(h.w.body()), &reply); err != nil || len(reply.Choices) != 1 ||
			!strings.Contains(reply.Choices[0].Message.Content, "Axiom is a gateway.") {
			t.Fatalf("aggregated reply = %s (%v)", h.w.body(), err)
		}
		if len(searcher.queries) != 1 || len(h.settlements[0].outcome.Attempts[0].Segments) != 2 {
			t.Fatalf("searches = %d, attempts = %+v", len(searcher.queries), h.settlements[0].outcome.Attempts)
		}
	})
}

func TestServerToolNeedsABackend(t *testing.T) {
	r := newRig(t)
	a := r.newAccount(1e9)
	body := `{"model":"acme/beta","tools":[{"type":"openrouter:web_search"}],"messages":[{"role":"user","content":"hello"}]}`
	resp, out := r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(body))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(out, protocol.NoWebSearch) {
		t.Fatalf("%d %s, want a 400 saying %q", resp.StatusCode, out, protocol.NoWebSearch)
	}
	if got := r.count("SELECT count(*) FROM billing.reservations"); got != 0 {
		t.Fatalf("the refusal created %d reservations", got)
	}
	body = `{"model":"acme/beta","n":2,"tools":[{"type":"openrouter:web_search"}],"messages":[{"role":"user","content":"hello"}]}`
	resp, out = r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(body))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(out, "more than one choice") {
		t.Fatalf("%d %s, want a refusal of n > 1", resp.StatusCode, out)
	}
}

func TestServerToolConflictsWithTheWebPlugin(t *testing.T) {
	r := newRig(t)
	a := r.newAccount(1e9)
	body := `{"model":"acme/beta","plugins":[{"id":"web"}],"tools":[{"type":"openrouter:web_search"}],"messages":[{"role":"user","content":"hello"}]}`
	resp, out := r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(body))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(out, "cannot be combined with the web plugin") {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
}

//nolint:cyclop // native, refused-native and loop decisions in one fixture
func TestServerToolNativeMapping(t *testing.T) {
	f := newRouteFixture(t)
	snap, err := f.cat.Current()
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{}
	plan := routing.Plan{Entries: []routing.Entry{{
		Model: f.served, Target: protocol.Target{Format: protocol.Chat, Model: "served", MaxOutput: 100},
	}}}
	c := &Call{
		Format: protocol.Chat, snap: snap,
		Request: protocol.Request{
			Format: protocol.Chat, ServerSearch: &protocol.ServerWebSearch{Engine: "native", MaxResults: 5, MaxUses: 3},
		},
	}
	got, err := h.resolveServerTool(c, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || c.nativeSearch != 3 || c.serverLoop || c.Request.ServerSearch.Loop ||
		c.Request.WebSearches != 3 || !c.Request.Needs.Has(protocol.WebSearch) {
		t.Fatalf("native decision = plan %+v, call %+v", got, c)
	}

	// An explicit native engine on a model without the ability is refused, as the web plugin refuses it.
	f.nospec = f.seed("acme/native-no", specNoServing)
	if err := f.cat.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	snap, err = f.cat.Current()
	if err != nil {
		t.Fatal(err)
	}
	c = &Call{Format: protocol.Chat, snap: snap, Request: protocol.Request{
		Format: protocol.Chat, ServerSearch: &protocol.ServerWebSearch{Engine: "native", MaxResults: 5},
	}}
	plan.Entries[0].Model = f.nospec
	var ref *Refusal
	if _, err := h.resolveServerTool(c, plan); !errors.As(err, &ref) || !strings.Contains(ref.Message, "no native web search") {
		t.Fatalf("native without the ability: %v", err)
	}
}

func TestServerToolNameCollision(t *testing.T) {
	r := newRig(t)
	a := r.newAccount(1e9)
	body := `{"model":"acme/beta","tools":[{"type":"function","function":{"name":"openrouter_web_search"}},{"type":"openrouter:web_search"}],"messages":[{"role":"user","content":"hello"}]}`
	resp, out := r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(body))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(out, "already declares a function") {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
}
