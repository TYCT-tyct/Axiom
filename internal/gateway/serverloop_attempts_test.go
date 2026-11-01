package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
	"axiom/internal/websearch"
)

// perCallCosts prices each supplier request once, as a schedule with a per-request price does. It records how many
// requests were priced, so a loop that reprices all segments under the last receipt is visible.
type perCallCosts struct{ calls int }

func (c *perCallCosts) Credits(_ suppliers.AttemptReceipt, _ []catalog.Usage, _ platform.Currency) (suppliers.AttemptCost, int64, bool, error) {
	c.calls++
	return suppliers.AttemptCost{Native: platform.AmountExact(1), Flag: suppliers.CostComplete}, 1, true, nil
}

// TestServerLoopChargesEverySupplierRequest: each hop is an attempt priced on its own receipt, so a per-request price is
// counted once per hop and the call's cost is the sum, never one pricing of all segments under the last receipt.
func TestServerLoopChargesEverySupplierRequest(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		costs := &perCallCosts{}
		upstream := protocol.Chat
		h := newHarness(t, ctx, options{
			client: protocol.Chat, upstream: &upstream, request: []byte(searchTool),
			price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			search: &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}},
			server: &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2},
			costs:  costs,
			script: map[int][]scripted{1: {serve(beat{data: chatToolCall}), serve(beat{data: chatAnswer})}},
		})
		h.run()
		if h.w.status != http.StatusOK {
			t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
		}
		attempts := h.settlements[0].outcome.Attempts
		if len(attempts) != 2 || costs.calls != 2 || costOf(h.settlements[0].outcome) != 2 {
			t.Fatalf("attempts = %+v, priced = %d; want two one-credit requests", attempts, costs.calls)
		}
		for i, at := range attempts {
			if at.Cost != 1 || at.Estimate.Native.Cmp(platform.AmountExact(1)) != 0 || !at.CostKnown {
				t.Fatalf("attempt %d = %+v, want one known credit", i, at)
			}
		}
	})
}

// failedLoop runs a two-hop loop whose second request fails before a Relay. executed says whether the supplier may
// have run it; otherwise it refused it outright.
func failedLoop(t *testing.T, ctx context.Context, executed bool) (*harness, *perCallCosts) {
	t.Helper()
	costs := &perCallCosts{}
	upstream := protocol.Chat
	h := newHarness(t, ctx, options{
		client: protocol.Chat, upstream: &upstream, request: []byte(searchTool),
		price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
		search: &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}},
		server: &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2},
		costs:  costs,
		script: map[int][]scripted{1: {serve(beat{data: chatToolCall}), failure(routing.ScopeEndpoint, executed)}},
	})
	h.run()
	if h.w.status != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s, want 502", h.w.status, h.w.body())
	}
	return h, costs
}

// TestServerLoopKeepsAnUnknownFailedRequest: the second hop can fail before it reaches a Relay, but the supplier may
// still have run and charged it. It is an attempt of its own, priced as any failed attempt whose outcome is unknown.
func TestServerLoopKeepsAnUnknownFailedRequest(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h, costs := failedLoop(t, ctx, false)
		attempts := h.settlements[0].outcome.Attempts
		if len(attempts) != 2 || attempts[1].Seq != 2 || !attempts[1].Continues || attempts[1].Result != billing.Unknown {
			t.Fatalf("attempts = %+v, want the failed request as a second, unknown attempt", attempts)
		}
		if costs.calls != 2 || attempts[1].CostKnown {
			t.Fatalf("priced = %d, second = %+v; want it priced and not known", costs.calls, attempts[1])
		}
		if h.up.endpoint(1).acquired != 2 || h.up.endpoint(1).done != 2 {
			t.Fatalf("acquired/done = %d/%d, want each request ended once", h.up.endpoint(1).acquired, h.up.endpoint(1).done)
		}
	})
}

// TestServerLoopDoesNotPriceARefusedRequest: an explicit refusal is not an unknown execution, so the failed hop is
// recorded as not executed and costs nothing.
func TestServerLoopDoesNotPriceARefusedRequest(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h, costs := failedLoop(t, ctx, true)
		attempts := h.settlements[0].outcome.Attempts
		if len(attempts) != 2 || attempts[1].Result != billing.NotExecuted || attempts[1].Cost != 0 ||
			attempts[1].Estimate.Conversion != suppliers.ConversionNotRun {
			t.Fatalf("attempts = %+v, want a zero-cost not-executed second attempt", attempts)
		}
		if !attempts[1].CostKnown || costs.calls != 1 {
			t.Fatalf("a refused request was priced (calls = %d): %+v", costs.calls, attempts[1])
		}
	})
}

// TestServerLoopStepsDoNotSpendTheFallbacks: each step of a loop is an attempt, but not a routing one. A loop that ran
// no search (its calls named no query) and then got an empty reply still falls back to the next entry, after more
// samplings than platform.MaxAttempts; the fallback answers alone and continues nothing.
func TestServerLoopStepsDoNotSpendTheFallbacks(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		noQuery := strings.Replace(chatToolCall, `{\"query\":\"axiom\"}`, `{}`, 1)
		searcher := &recordingSearcher{}
		upstream := protocol.Chat
		h := newHarness(t, ctx, options{
			client: protocol.Chat, upstream: &upstream, entries: 2, request: []byte(searchTool),
			price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			search: searcher, server: &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2},
			script: map[int][]scripted{
				1: {serve(beat{data: noQuery}), serve(beat{data: noQuery}), serve(beat{data: noQuery}), serve(beat{})},
				2: {serve(beat{data: chatAnswer})},
			},
		})
		h.run()
		if h.w.status != http.StatusOK || !strings.Contains(h.w.body(), "Axiom is a gateway.") || len(searcher.queries) != 0 {
			t.Fatalf("status = %d, searches = %d, body = %s", h.w.status, len(searcher.queries), h.w.body())
		}
		attempts := h.settlements[0].outcome.Attempts
		if len(attempts) != 5 || attempts[4].Seq != 5 || attempts[4].Continues || !attempts[3].Continues {
			t.Fatalf("attempts = %+v, want four samplings of the loop, then a fallback of its own", attempts)
		}
	})
}

// chatEmptyAnswer is a well-formed reply with no content: a role, a stop and usage.
const chatEmptyAnswer = `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":0,"total_tokens":20}}

data: [DONE]

`

// TestServerLoopEmptyAnswerIsNotDelivered: in a call that does not stream, the tool hop commits in the relay's sense.
// That must not make an empty final reply a delivered one: it is a failure, the model usage is waived and only the
// search is charged.
func TestServerLoopEmptyAnswerIsNotDelivered(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}}
		h := newHarness(t, ctx, options{
			client: protocol.Chat, request: []byte(strings.Replace(searchTool, `"stream":true`, `"stream":false`, 1)), search: searcher,
			price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			server: &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2},
			script: map[int][]scripted{1: {serve(beat{data: chatToolCall}), serve(beat{data: chatEmptyAnswer})}},
		})
		h.run()
		if h.w.status != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s, want 502", h.w.status, h.w.body())
		}
		attempts := h.settlements[0].outcome.Attempts
		if len(attempts) != 2 || attempts[0].Result != billing.FailedBeforeCommit || attempts[1].Result != billing.FailedBeforeCommit {
			t.Fatalf("attempts = %+v, want a loop that delivered nothing", attempts)
		}
		if got := attempts[0].Segments[0].Usage.Count(catalog.ItemWebSearch); got != 1 {
			t.Fatalf("searches charged = %d, want the one that ran", got)
		}
	})
}

// chatTwoSearches is one tool turn that asks for two searches.
const chatTwoSearches = `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"openrouter_web_search","arguments":"{\"query\":\"axiom\"}"}},{"index":1,"id":"call_2","type":"function","function":{"name":"openrouter_web_search","arguments":"{\"query\":\"gateway\"}"}}]},"finish_reason":null}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18}}

data: [DONE]

`

// cutSearcher is a backend during whose first search the call is cut: cut acts, then the search waits until its context
// ends, and it either returns its results (it had finished) or the context's error.
type cutSearcher struct {
	calls    int
	cut      func()
	finished bool
	waited   time.Duration // how long the search waited for its context to end
}

func (s *cutSearcher) Search(ctx context.Context, _ websearch.Query) ([]websearch.Result, error) {
	s.calls++
	s.cut()
	start := time.Now()
	<-ctx.Done()
	s.waited = time.Since(start)
	if s.finished {
		return []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}, nil
	}
	return nil, ctx.Err()
}

// TestServerLoopSearchStopsWithTheAttempt: a search runs inside its attempt. When the node shuts down or the call runs
// out of time during the first of two searches, the second is never started and nothing more is sent upstream; a
// search that had finished stays charged, one that was cut is not.
func TestServerLoopSearchStopsWithTheAttempt(t *testing.T) {
	for name, tc := range map[string]struct {
		finished bool
		charged  int64
		cut      func(h *harness)
		short    bool // the call's deadline passes while the search waits
	}{
		"shutdown after the search finished": {finished: true, charged: 1, cut: func(h *harness) { h.exec.Outstanding.abort() }},
		"deadline during the search":         {short: true, cut: func(*harness) {}},
	} {
		t.Run(name, func(t *testing.T) {
			run(t, func(t *testing.T, ctx context.Context) {
				searcher := &cutSearcher{finished: tc.finished}
				h := serverHarness(t, ctx, searcher, serve(beat{data: chatTwoSearches}), serve(beat{data: chatAnswer}))
				searcher.cut = func() { tc.cut(h) }
				if tc.short {
					h.exec.Limits.cfg.MaxCall = websearch.Timeout / 3
				}
				h.run()
				if h.w.status != http.StatusRequestTimeout || searcher.calls != 1 || len(h.up.endpoint(1).calls) != 1 {
					t.Fatalf("status = %d, searches = %d, upstream calls = %d; want a timeout after one search and one sampling",
						h.w.status, searcher.calls, len(h.up.endpoint(1).calls))
				}
				if searcher.waited >= websearch.Timeout {
					t.Fatalf("the search ran %s, to its own timeout: the attempt's end did not reach it", searcher.waited)
				}
				attempts := h.settlements[0].outcome.Attempts
				if len(attempts) != 1 || attempts[0].Segments[0].Usage.Count(catalog.ItemWebSearch) != tc.charged {
					t.Fatalf("attempts = %+v, want %d search charged", attempts, tc.charged)
				}
			})
		})
	}
}

// slowSearcher answers after a wait.
type slowSearcher struct{ wait time.Duration }

func (s slowSearcher) Search(context.Context, websearch.Query) ([]websearch.Result, error) {
	time.Sleep(s.wait)
	return []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}, nil
}

// TestServerLoopSearchOutlastsTheIdleWait: the idle timer watches an upstream that is sending. Once the sampling is
// over, a search slower than the idle wait is the gateway's own work and must not cut the attempt.
func TestServerLoopSearchOutlastsTheIdleWait(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := serverHarness(t, ctx, slowSearcher{wait: 2 * time.Second}, serve(beat{data: chatToolCall}), serve(beat{data: chatAnswer}))
		h.exec.Limits.cfg.IdleTimeout = time.Second
		h.run()
		if h.w.status != http.StatusOK || !strings.Contains(h.w.body(), "Axiom is a gateway.") {
			t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
		}
	})
}

// TestHeldEventsAreChargedByCapacity: the events a hop holds are charged as the buffer's capacity, before it grows; a
// tool hop's drop gives the charge back with the buffer, so the next hop does not wait on memory nothing counts.
func TestHeldEventsAreChargedByCapacity(t *testing.T) {
	const budget = 1 << 10
	mem := protocol.NewMemory(budget)
	s := &session{x: &Executor{Memory: mem}}
	for _, n := range []int{10, 100, 300} {
		if !s.holdOut(make([]byte, n)) {
			t.Fatalf("holding %d more bytes was refused", n)
		}
		if s.outHeld != int64(cap(s.out)) {
			t.Fatalf("held %d bytes in a buffer of %d, charged %d", len(s.out), cap(s.out), s.outHeld)
		}
	}
	if s.holdOut(make([]byte, budget)) {
		t.Fatal("events past the budget were held")
	}
	s.dropOut()
	if s.out != nil || s.outHeld != 0 || !mem.Charge(budget) {
		t.Fatalf("after the drop: buffer %d, charged %d; want both gone and the budget whole", cap(s.out), s.outHeld)
	}
}
