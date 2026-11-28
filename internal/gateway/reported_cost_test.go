package gateway

import (
	"testing"
	"testing/synctest"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

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
