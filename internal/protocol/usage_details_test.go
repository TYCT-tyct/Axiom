package protocol_test

import (
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// usageFinal is a settlement whose usage carries every part the Chat usage reports.
func usageFinal(t *testing.T) protocol.Final {
	t.Helper()
	u, err := catalog.NewUsage(map[catalog.Item]int64{
		catalog.ItemInput: 10, catalog.ItemCacheRead: 4, catalog.ItemCacheWrite: 2, catalog.ItemAudioIn: 3,
		catalog.ItemImageIn: 5, catalog.ItemOutput: 6, catalog.ItemReasoning: 1, catalog.ItemAudioOut: 2,
		catalog.ItemWebSearch: 2, catalog.ItemCodeExecution: 1,
	}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err = u.WithNativeTokens(catalog.NativeTokens{VideoInput: 5, AcceptedPrediction: 2, RejectedPrediction: 1})
	if err != nil {
		t.Fatal(err)
	}
	tool := must(platform.ParseExact("0.25"))
	return protocol.Final{
		Usage: protocol.Usage{Segments: []protocol.Segment{{Usage: u}}}, Cost: must(platform.ParseExact("1.5")),
		Currency: usd, ServerToolCost: &tool, CacheDiscount: must(platform.ParseExact("0.125")),
	}
}

// renderedUsage is the axiom.usage object of an error ending in the format.
func renderedUsage(t *testing.T, format protocol.Format) map[string]any {
	t.Helper()
	fin := usageFinal(t)
	_, body := protocol.RenderError(format, protocol.Error{Type: protocol.Timeout}, &fin)
	var out struct {
		Axiom struct {
			Usage map[string]any `json:"usage"`
		} `json:"axiom"`
	}
	if err := jsonx.Unmarshal(body, &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return out.Axiom.Usage
}

// TestUsageDetails: Chat reports totals, modality and prediction splits, server tools and costs.
func TestUsageDetails(t *testing.T) {
	t.Parallel()
	usage := renderedUsage(t, protocol.Chat)
	for _, tc := range []struct {
		group, member string
		want          any
	}{
		{"", "prompt_tokens", 24.0},
		{"", "completion_tokens", 9.0},
		{"", "total_tokens", 33.0},
		{"", "is_byok", false},
		{"", "cache_discount", 0.125},
		{"prompt_tokens_details", "cached_tokens", 4.0},
		{"prompt_tokens_details", "cache_write_tokens", 2.0},
		{"prompt_tokens_details", "audio_tokens", 3.0},
		{"prompt_tokens_details", "video_tokens", 5.0},
		{"completion_tokens_details", "reasoning_tokens", 1.0},
		{"completion_tokens_details", "audio_tokens", 2.0},
		{"completion_tokens_details", "accepted_prediction_tokens", 2.0},
		{"completion_tokens_details", "rejected_prediction_tokens", 1.0},
		{"server_tool_use_details", "web_search_requests", 2.0},
		{"server_tool_use_details", "tool_calls_requested", 3.0},
		{"server_tool_use_details", "tool_calls_executed", 3.0},
		{"cost_details", "server_tool_cost", 0.25},
	} {
		t.Run(tc.group+"/"+tc.member, func(t *testing.T) {
			fields := usage
			if tc.group != "" {
				fields, _ = usage[tc.group].(map[string]any)
			}
			if got := fields[tc.member]; got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMessagesUsageKeepsItsOwnMembers: Messages reports its own server_tool_use and none of Chat's members.
func TestMessagesUsageKeepsItsOwnMembers(t *testing.T) {
	t.Parallel()
	fin := usageFinal(t)
	_, body := protocol.RenderError(protocol.Messages, protocol.Error{Type: protocol.Timeout}, &fin)
	for _, member := range []string{"is_byok", "cost_details", "cache_discount", "video_tokens", "accepted_prediction_tokens"} {
		if strings.Contains(string(body), member) {
			t.Errorf("Messages shows %q: %s", member, body)
		}
	}
	if !strings.Contains(string(body), `"server_tool_use":{"web_search_requests":2,"code_execution_requests":1}`) {
		t.Errorf("Messages server_tool_use: %s", body)
	}
}

// TestRelayActualModel: a reply names the model that served it, not the alias the client asked for, so a models
// fallback is visible.
func TestRelayActualModel(t *testing.T) {
	t.Parallel()
	o := options
	o.ActualModel = "acme/alpha-2026-01-01"
	r := protocol.NewRelay(protocol.Chat, protocol.Chat, strings.NewReader(chatRole+chatHi+chatStop+chatDone), protocol.UsageContext{}, o)
	var out []byte
	for {
		step, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, step.Out...)
		if step.Kind == protocol.StepUpstreamDone {
			out = append(out, r.Finish(settled(r.Usage()))...)
			break
		}
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasPrefix(line, "data: {") && strings.Contains(line, `"model"`) &&
			!strings.Contains(line, `"model":"acme/alpha-2026-01-01"`) {
			t.Errorf("not the served model: %s", line)
		}
	}
}
