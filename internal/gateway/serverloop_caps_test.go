package gateway

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
	"axiom/internal/websearch"
)

// toolHop is a Chat hop whose one turn calls the web search tool once for each arguments object, which is given as the
// JSON string's content, escaped.
func toolHop(arguments ...string) string {
	calls := make([]string, len(arguments))
	for i, args := range arguments {
		calls[i] = `{"index":` + strconv.Itoa(i) + `,"id":"call_` + strconv.Itoa(i) +
			`","type":"function","function":{"name":"openrouter_web_search","arguments":"` + args + `"}}`
	}
	return `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[` +
		strings.Join(calls, ",") + `]},"finish_reason":null}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}

data: [DONE]

`
}

// TestServerLoopToolResults: what the model reads back for each search call of a turn, and whether the next hop still
// declares the function. A call that runs no search says why; a cap that is reached takes the function away.
func TestServerLoopToolResults(t *testing.T) {
	t.Parallel()
	query := `{\"query\":\"axiom\"}`
	found := []websearch.Result{{URL: "https://example.com/one", Title: "One", Content: "R1"}, {URL: "https://example.com/two", Title: "Two", Content: "R2"}}
	cases := []toolResultCase{
		{
			name: "results", tool: protocol.ServerWebSearch{MaxResults: 2}, calls: []string{query},
			searches: 1, want: []string{"Search results for", "https://example.com/two"}, declares: true,
		},
		{
			name: "step limit", tool: protocol.ServerWebSearch{MaxResults: 2, MaxUses: 1}, calls: []string{query, query},
			searches: 1, want: []string{"Search results for", "The web search step limit for this request was reached."},
		},
		{
			name: "no query", tool: protocol.ServerWebSearch{MaxResults: 2}, calls: []string{`{\"query\":\"  \"}`},
			want: []string{"The web search call did not name a query."}, declares: true,
		},
		{
			name: "query too long here", tool: protocol.ServerWebSearch{MaxResults: 2},
			calls: []string{`{\"query\":\"` + strings.Repeat("a", protocol.MaxWebSearchQueryBytes+1) + `\"}`},
			want:  []string{"The search query is longer than this search engine accepts."}, declares: true,
		},
		{
			name: "query too long for the backend", tool: protocol.ServerWebSearch{MaxResults: 2}, calls: []string{query},
			err: websearch.ErrQueryTooLong, searches: 1, want: []string{"The search query is longer than this search engine accepts."}, declares: true,
		},
		{
			name: "result total spent", tool: protocol.ServerWebSearch{MaxResults: 2, MaxTotalResults: 1}, calls: []string{query},
			searches: 1, want: []string{"https://example.com/one"}, notInNext: "https://example.com/two",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run(t, func(t *testing.T, ctx context.Context) { tc.check(t, ctx, found) })
		})
	}
}

// toolResultCase is one turn of TestServerLoopToolResults: the tool's caps, the calls, the backend's error, and what
// the next hop must carry.
type toolResultCase struct {
	name      string
	tool      protocol.ServerWebSearch
	calls     []string
	err       error
	searches  int
	want      []string // in the next hop's body
	declares  bool     // whether the next hop still declares the function
	notInNext string
}

// check runs the turn, then an answer, and reads the request the turn's results went back in.
func (tc toolResultCase) check(t *testing.T, ctx context.Context, found []websearch.Result) {
	t.Helper()
	searcher := &recordingSearcher{results: found, err: tc.err}
	tool := tc.tool
	tool.Engine = "exa"
	h := newHarness(t, ctx, options{
		client: protocol.Chat, request: []byte(searchTool),
		price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
		search: searcher, server: &tool,
		script: map[int][]scripted{1: {serve(beat{data: toolHop(tc.calls...)}), serve(beat{data: chatAnswer})}},
	})
	h.run()
	if h.w.status != http.StatusOK || !strings.Contains(h.w.body(), "Axiom is a gateway.") {
		t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
	}
	if len(searcher.queries) != tc.searches {
		t.Fatalf("searches = %+v, want %d", searcher.queries, tc.searches)
	}
	calls := h.up.endpoint(1).calls
	if len(calls) != 2 {
		t.Fatalf("the upstream got %d calls, want 2", len(calls))
	}
	next := string(calls[1].Body)
	for _, w := range tc.want {
		if !strings.Contains(next, w) {
			t.Errorf("the next hop lacks %q: %s", w, next)
		}
	}
	if tc.notInNext != "" && strings.Contains(next, tc.notInNext) {
		t.Errorf("the next hop has %q past the result cap: %s", tc.notInNext, next)
	}
	if declares := !strings.Contains(next, `"tools":[]`); declares != tc.declares {
		t.Errorf("the next hop declares the function: %t, want %t: %s", declares, tc.declares, next)
	}
}

// TestServerLoopClientLeavesADetachedHop: an endpoint that keeps reading after the client left ends its hop normally,
// and the loop must still stop there: a tool hop runs no search and sends no further sampling, and an answering hop is
// not written. A sampling after a search follows the client whatever the policy. Each way the sampling is cancelled and
// was never delivered.
func TestServerLoopClientLeavesADetachedHop(t *testing.T) {
	t.Parallel()
	slow := func(stream string) scripted { // the client leaves while the hop's first event waits for the rest
		first, rest, _ := strings.Cut(stream, "\n\n")
		return serve(beat{data: first + "\n\n"}, beat{wait: time.Second, data: rest})
	}
	cases := []struct {
		name     string
		script   []scripted
		searches int
	}{
		{name: "tool hop", script: []scripted{slow(chatToolCall)}},
		{name: "answering hop", script: []scripted{slow(chatAnswer)}},
		{name: "answering sampling after a search", script: []scripted{serve(beat{data: chatToolCall}), slow(chatAnswer)}, searches: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run(t, func(t *testing.T, ctx context.Context) {
				searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}}
				h := serverHarness(t, ctx, searcher, tc.script...)
				//nolint:contextcheck // the helper keeps the client context so the policy test can cancel it
				h.req = httptestRequest(t, h, suppliers.CallPolicy{Streams: true, Cancels: false})
				done := h.start()
				time.Sleep(time.Second / 2)
				h.client()
				<-done
				h.check()
				if len(searcher.queries) != tc.searches || len(h.up.endpoint(1).calls) != len(tc.script) {
					t.Fatalf("searches = %d, upstream calls = %d; want %d and %d",
						len(searcher.queries), len(h.up.endpoint(1).calls), tc.searches, len(tc.script))
				}
				attempts := h.settlements[0].outcome.Attempts
				last := attempts[len(attempts)-1]
				if len(attempts) != len(tc.script) || last.Finish != billing.FinishCancelled || last.Result != billing.FailedBeforeCommit {
					t.Fatalf("attempts = %+v, want %d with the last cancelled before commit", attempts, len(tc.script))
				}
			})
		})
	}
}
