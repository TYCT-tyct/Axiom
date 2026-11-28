package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
	"axiom/internal/websearch"
)

// TestEachSamplingOfASearchLoopIsPricedWithItsOwnFigure: every sampling of the openrouter:web_search loop is a supplier
// request of its own, settled before the next is sent, so each is priced with the figure its own answer gave, an
// explicit zero among them.
func TestEachSamplingOfASearchLoopIsPricedWithItsOwnFigure(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		costs := &reportedCosts{}
		upstream := protocol.Chat
		h := newHarness(t, ctx, options{
			client: protocol.Chat, upstream: &upstream, request: []byte(searchTool),
			price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			search: &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}},
			server: &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2},
			costs:  costs,
			script: map[int][]scripted{1: {
				serve(beat{data: strings.Replace(chatToolCall, `"total_tokens":14`, `"total_tokens":14,"cost":0`, 1)}),
				serve(beat{data: strings.Replace(chatAnswer, `"total_tokens":25`, `"total_tokens":25,"cost":0.0003`, 1)}),
			}},
		})
		h.run()
		if h.w.status != http.StatusOK {
			t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
		}
		var got []string
		for _, r := range costs.reported {
			if r == nil {
				got = append(got, "none")
			} else {
				got = append(got, r.Amount.Rat().FloatString(4))
			}
		}
		if strings.Join(got, ",") != "0.0000,0.0003" {
			t.Errorf("the samplings were priced with %v, want 0 and 0.0003", got)
		}
	})
}

// reportedCosts records the supplier's figure each attempt is priced with.
type reportedCosts struct{ reported []*protocol.ReportedCost }

func (c *reportedCosts) Credits(_ suppliers.AttemptReceipt, _ []catalog.Usage, reported *protocol.ReportedCost, _ platform.Currency) (suppliers.AttemptCost, int64, bool, error) {
	c.reported = append(c.reported, reported)
	return suppliers.AttemptCost{Flag: suppliers.CostComplete}, 0, true, nil
}

// TestTheSuppliersFigureReachesItsCost: the usage.cost an upstream's answer ends with is what the attempt is priced
// with (suppliers decides whether to take it, docs/domains/suppliers-direct.md「OpenRouter 的成本」); an answer cut before
// its usage has none, which is never a figure of zero.
func TestTheSuppliersFigureReachesItsCost(t *testing.T) {
	t.Parallel()
	const head = `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":`
	answer := head + `[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}` + "\n\n" +
		head + `[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	for _, c := range []struct {
		name  string
		beats []beat
		want  string // the figure's amount, "" for none
	}{
		{"an answer with its usage", []beat{{data: answer + head + `[],"usage":{"prompt_tokens":3,"completion_tokens":1,"cost":0.0007}}` + "\n\ndata: [DONE]\n\n"}}, "0.0007"},
		{"an answer cut short", []beat{{data: head + `[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}` + "\n\n"}, {hangup: true}}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				costs := &reportedCosts{}
				upstream := protocol.Chat
				h := newHarness(t, t.Context(), options{
					client: protocol.Chat, upstream: &upstream, costs: costs, script: map[int][]scripted{1: {serve(c.beats...)}},
				})
				h.run()
				if len(costs.reported) != 1 {
					t.Fatalf("%d attempts priced", len(costs.reported))
				}
				got := ""
				if r := costs.reported[0]; r != nil {
					got = r.Amount.Rat().FloatString(4)
				}
				if got != c.want {
					t.Errorf("the attempt was priced with the figure %q, want %q", got, c.want)
				}
			})
		})
	}
}
