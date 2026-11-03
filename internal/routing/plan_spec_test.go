package routing_test

import (
	"testing"

	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// TestPlanUsesTheSelectedFormatSpec: Plan judges each request by the spec of the format it actually calls the upstream
// in. Chat's lack of tools and shorter limit do not remove Responses' tools or shorten its context, and Responses'
// smaller context does not make a Chat request fail.
func TestPlanUsesTheSelectedFormatSpec(t *testing.T) {
	t.Parallel()
	e := endpoint(t, 1, func(e *routing.Endpoint) {
		e.Native, e.Served = routing.Of(protocol.Chat, protocol.Responses), routing.Of(protocol.Chat, protocol.Responses)
		e.Caps, e.Parameters, e.Context, e.MaxOutput = ^protocol.Capabilities(0), ^protocol.ParameterSet(0), 32_000, 4096
		e.Specs = map[protocol.Format]routing.Spec{
			protocol.Chat:      {Caps: 0, Parameters: ^protocol.ParameterSet(0), Context: 128_000, MaxOutput: 8192},
			protocol.Responses: {Caps: protocol.Tools, Parameters: ^protocol.ParameterSet(0), Context: 32_000, MaxOutput: 4096},
		}
	})
	h := newHarness(t, routing.Config{}, openPreset(), e)

	chat := input()
	chat.Req.Format, chat.Req.TextBytes = protocol.Chat, 64_000*4
	if got := first(h.plan(chat)); got != e.ID {
		t.Errorf("a 64000-token Chat request planned to %s, want %s", got, e.ID)
	}

	chatTools := input()
	chatTools.Req.Format, chatTools.Req.Needs = protocol.Chat, protocol.Tools
	if got := h.excluded(chatTools); len(got.Reasons) != 1 || got.Reasons[0].Reason != routing.MissingCapability {
		t.Errorf("Chat with tools: reason %v, want MissingCapability", got.Reasons[0].Reason)
	}

	responsesTools := input()
	responsesTools.Req.Format, responsesTools.Req.Needs = protocol.Responses, protocol.Tools
	if got := first(h.plan(responsesTools)); got != e.ID {
		t.Errorf("Responses with tools planned to %s, want %s", got, e.ID)
	}

	responses := input()
	responses.Req.Format, responses.Req.TextBytes = protocol.Responses, 64_000*4
	if got := h.excluded(responses); len(got.Reasons) != 1 || got.Reasons[0].Reason != routing.ContextTooSmall {
		t.Errorf("a 64000-token Responses request: reason %v, want ContextTooSmall", got.Reasons[0].Reason)
	}
}

// TestEndpointSurfaceIsCommonToEveryFormat: the endpoint object public discovery can show has one capability set and
// one set of limits, so Surface keeps only what every native format has and the smaller limits. A format-specific tool
// support or a longer Chat window must not be claimed for the endpoint as a whole.
func TestEndpointSurfaceIsCommonToEveryFormat(t *testing.T) {
	t.Parallel()
	e := routing.Endpoint{
		Native: routing.Of(protocol.Chat, protocol.Responses),
		Specs: map[protocol.Format]routing.Spec{
			protocol.Chat:      {Caps: protocol.Tools | protocol.Images, Parameters: 0b11, Context: 128_000, MaxOutput: 8192},
			protocol.Responses: {Caps: protocol.Tools, Parameters: 0b01, Context: 32_000, MaxOutput: 4096},
		},
	}
	got := e.Surface()
	if got.Caps != protocol.Tools || got.Parameters != 0b01 || got.Context != 32_000 || got.MaxOutput != 4096 {
		t.Fatalf("surface = %+v, want the common tools, parameter, 32000 context and 4096 output", got)
	}
}
