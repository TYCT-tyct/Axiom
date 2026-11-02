package gateway

import (
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// specFastPlain is specAll without the reasoning ability and with a fastest output speed: a model with nothing to hide
// in its stream.
const specFastPlain = `{"items":["input","output","cache_read","cache_write"],"tiers":["standard"],` +
	`"context_length":200000,"max_output_tokens":8192,"streams":true,` +
	`"abilities":["tools","images","documents","prompt_cache","structured_output","web_search","code_execution"],` +
	`"output_speed":100}`

// specFast is specAll plus the two model facts segmenting needs: a fastest output speed, and the formats whose streams
// show reasoning progress.
const specFast = `{"items":["input","output","cache_read","cache_write"],"tiers":["standard"],` +
	`"context_length":200000,"max_output_tokens":8192,"streams":true,` +
	`"abilities":["tools","images","documents","reasoning","prompt_cache","structured_output","web_search","code_execution"],` +
	`"output_speed":100,"reasoning_visible":["messages"]}`

// TestRouteBuildCarriesWhatSegmentingNeeds: the spec's fastest output speed and the formats whose streams show reasoning
// reach the snapshot, and they are what makes an entry segmentable: the segment plus the allowance the speed sets aside
// must fit under the output limit, and the format the endpoint is called in must be one the spec lists, since that is
// the stream the gateway meters. A model that does not reason has nothing to miss, so its speed alone segments it; a
// model that says nothing of its speed is reserved in full, as checkEntry finds the ones whose spec says nothing.
func TestRouteBuildCarriesWhatSegmentingNeeds(t *testing.T) {
	f := newRouteFixture(t)
	fast, plain := f.seed("acme/fast", specFast), f.seed("acme/plain", specFastPlain)
	prices, err := f.fixturePrices() // the models were seeded after the fixture's sale prices
	if err != nil {
		t.Fatal(err)
	}
	f.prices = prices
	for _, c := range []struct {
		name   string
		model  catalog.ModelID
		native []protocol.Format // what the endpoint speaks itself
		format protocol.Format   // what the client sends
		want   bool
	}{
		// The spec says a Messages stream shows reasoning: the call is made in Messages and metered there.
		{"a Messages stream that shows reasoning", fast, []protocol.Format{protocol.Messages, protocol.Chat}, protocol.Messages, true},
		// It says nothing of a Chat one, and this call is made in Chat.
		{"a Chat call whose stream shows no reasoning", fast, []protocol.Format{protocol.Messages, protocol.Chat}, protocol.Chat, false},
		// The client's Messages stream shows reasoning, but the endpoint speaks only Chat and is called in it: what the
		// gateway reads is the Chat stream, which shows none (docs/domains/gateway.md「转发规则」).
		{"a Messages call converted to an endpoint showing reasoning only in Messages", fast, []protocol.Format{protocol.Chat}, protocol.Messages, false},
		// Nothing to hide: the model does not reason, so its known speed is enough.
		{"a model that does not reason", plain, []protocol.Format{protocol.Messages}, protocol.Messages, true},
	} {
		f.build([]suppliers.Routable{routable(c.model, c.native...)})
		plan, _, err := f.router.Plan(call(c.model, func(r *protocol.Request) { r.Format = c.format }))
		if err != nil {
			t.Fatalf("%s: a %v call was not planned: %v", c.name, c.format, err)
		}
		if got := plan.Entries[0].Segmentable; got != c.want {
			t.Errorf("%s: segmentable=%t, want %t", c.name, got, c.want)
		}
		if got := plan.Segments; got != (billing.Segments{Output: 1000, Overshoot: 2 * time.Second}) {
			t.Errorf("%s: the plan froze %+v, want the router's segment policy", c.name, got)
		}
	}
}
