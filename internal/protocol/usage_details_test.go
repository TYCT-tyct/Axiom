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

// TestUsageTotals: Chat reports the input and output totals and BYOK, which is always false: we sell our own keys.
func TestUsageTotals(t *testing.T) {
	t.Parallel()
	usage := renderedUsage(t, protocol.Chat)
	if usage["prompt_tokens"] != 24.0 || usage["completion_tokens"] != 9.0 || usage["total_tokens"] != 33.0 {
		t.Errorf("totals: %v", usage)
	}
	if usage["is_byok"] != false {
		t.Errorf("is_byok: %v", usage["is_byok"])
	}
}

// TestUsageModalityAndPredictionDetails: the modality splits and the prediction tokens that sit inside the output total
// are echoed in the details objects.
func TestUsageModalityAndPredictionDetails(t *testing.T) {
	t.Parallel()
	usage := renderedUsage(t, protocol.Chat)
	in, _ := usage["prompt_tokens_details"].(map[string]any)
	if in["cached_tokens"] != 4.0 || in["cache_write_tokens"] != 2.0 || in["audio_tokens"] != 3.0 || in["video_tokens"] != 5.0 {
		t.Errorf("prompt details: %v", in)
	}
	details, _ := usage["completion_tokens_details"].(map[string]any)
	if details["reasoning_tokens"] != 1.0 || details["audio_tokens"] != 2.0 ||
		details["accepted_prediction_tokens"] != 2.0 || details["rejected_prediction_tokens"] != 1.0 {
		t.Errorf("completion details: %v", details)
	}
}

// TestUsageServerToolsAndCosts: the server tools the call ran and the price it paid for them, and what cache reads
// saved against the input price.
func TestUsageServerToolsAndCosts(t *testing.T) {
	t.Parallel()
	usage := renderedUsage(t, protocol.Chat)
	tools, _ := usage["server_tool_use_details"].(map[string]any)
	if tools["web_search_requests"] != 2.0 || tools["tool_calls_requested"] != 3.0 || tools["tool_calls_executed"] != 3.0 {
		t.Errorf("server tools: %v", tools)
	}
	if costs, _ := usage["cost_details"].(map[string]any); costs["server_tool_cost"] != 0.25 {
		t.Errorf("cost details: %v", usage)
	}
	if usage["cache_discount"] != 0.125 {
		t.Errorf("cache discount: %v", usage)
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
