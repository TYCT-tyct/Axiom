package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

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
