package protocol_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// errorRows are docs/domains/gateway.md「错误」's status table, row by row: the status before a stream starts and the
// Messages type.
var errorRows = []struct {
	t        protocol.ErrorType
	name     string
	status   int
	messages string
}{
	{protocol.Authentication, "authentication", 401, "authentication_error"},
	{protocol.CreditsInsufficient, "credits_insufficient", 402, "billing_error"},
	{protocol.BillingUnavailable, "billing_unavailable", 503, "api_error"},
	{protocol.RateLimited, "rate_limited", 429, "rate_limit_error"},
	{protocol.Overloaded, "overloaded", 503, "overloaded_error"},
	{protocol.ModelNotFound, "model_not_found", 404, "not_found_error"},
	{protocol.NoEndpoint, "no_endpoint", 404, "not_found_error"},
	{protocol.NoEndpointForNow, "no_endpoint", 503, "overloaded_error"},
	{protocol.InvalidRequest, "invalid_request", 400, "invalid_request_error"},
	{protocol.RequestTooLarge, "request_too_large", 413, "request_too_large"},
	{protocol.UpstreamFailed, "upstream_error", 502, "api_error"},
	{protocol.Timeout, "timeout", 408, "timeout_error"},
}

// TestRenderError: each type answers with its status in each format's shell, the unified type where gateway.md puts
// it, the details next to it, and with a settlement the usage and cost in an axiom object.
func TestRenderError(t *testing.T) {
	u := protocol.Usage{Segments: []protocol.Segment{{Usage: must(catalog.NewUsage(map[catalog.Item]int64{
		catalog.ItemInput: 7, catalog.ItemOutput: 2,
	}, catalog.ServiceTierStandard, nil))}}}
	fin := settled(u)
	for _, row := range errorRows {
		for _, f := range []protocol.Format{protocol.Messages, protocol.Responses, protocol.Chat} {
			t.Run(fmt.Sprintf("%s %d", row.name, f), func(t *testing.T) {
				status, body := protocol.RenderError(f, protocol.Error{Type: row.t, Details: `,"limit_source":"balance"`}, &fin)
				var got map[string]any
				err := jsonx.Unmarshal(body, &got)
				axiom, _ := got["axiom"].(map[string]any)
				switch diff := cmp.Diff(wantError(f, row.name, row.messages, row.status), typedError(got)); {
				case err != nil || status != row.status:
					t.Errorf("status %d, want %d; %v:\n%s", status, row.status, err, body)
				case diff != "":
					t.Errorf("error (-want +got):\n%s\n%s", diff, body)
				case !charged(axiom):
					t.Errorf("axiom %v", axiom)
				}
			})
		}
	}
}

// charged reports whether an axiom object carries the settlement's input of 7 and its cost.
func charged(axiom map[string]any) bool {
	usage, _ := axiom["usage"].(map[string]any)
	return usage["cost_currency"] == "USD" && (usage["input_tokens"] == float64(7) || usage["prompt_tokens"] == float64(7))
}

// wantError is what typedError should find in an error of type name in format f.
func wantError(f protocol.Format, name, messages string, status int) map[string]any {
	want := map[string]any{"error_type": name, "limit_source": "balance", "has_message": true}
	if f == protocol.Messages {
		want["type"] = messages
	} else {
		want["code"] = float64(status)
	}
	return want
}

// typedError gathers what an error body says of its type: Messages' error object, or Chat's metadata with its code.
func typedError(body map[string]any) map[string]any {
	e, _ := body["error"].(map[string]any)
	got := map[string]any{"has_message": e["message"] != nil && e["message"] != ""}
	src := e
	if meta, ok := e["metadata"].(map[string]any); ok {
		src, got["code"] = meta, e["code"]
	}
	for k, v := range src {
		if k != "message" {
			got[k] = v
		}
	}
	return got
}

// TestRenderErrorCharges: no settlement, no axiom object; a pending settlement says so and gives no cost.
func TestRenderErrorCharges(t *testing.T) {
	_, body := protocol.RenderError(protocol.Chat, protocol.Error{Type: protocol.Timeout}, nil)
	if strings.Contains(string(body), "axiom") {
		t.Errorf("an axiom object without a settlement: %s", body)
	}
	_, body = protocol.RenderError(protocol.Messages, protocol.Error{Type: protocol.Timeout}, &protocol.Final{Pending: true})
	if !strings.Contains(string(body), `"billing_pending":true`) || strings.Contains(string(body), "cost") {
		t.Errorf("pending: %s", body)
	}
}

// newAPIDisableKeywords are the phrases that make new-api disable the channel an error came through
// (QuantumNous/new-api setting/operation_setting/operation_setting.go, AutomaticDisableKeywords, commit ebe4c36,
// 2026-09-08); new-api lowercases them and looks for them in the message.
var newAPIDisableKeywords = []string{
	"Your credit balance is too low",
	"This organization has been disabled.",
	"You exceeded your current quota",
	"Permission denied",
	"The security token included in the request is invalid",
	"Operation not allowed",
	"Your account is not authorized",
}

// TestErrorMessages: no error we write, before or during a stream, says any of them, so a relay that fronts us with
// new-api does not take our errors for a dead upstream (docs/domains/gateway.md「错误」).
func TestErrorMessages(t *testing.T) {
	for _, row := range errorRows {
		for _, f := range []protocol.Format{protocol.Messages, protocol.Responses, protocol.Chat} {
			_, body := protocol.RenderError(f, protocol.Error{Type: row.t}, nil)
			o := options
			o.Memory = protocol.NewMemory(0)
			ending := protocol.NewRelay(f, f, strings.NewReader(""), protocol.UsageContext{}, o).Fail(protocol.Error{Type: row.t}, nil)
			for _, text := range []string{string(body), string(ending)} {
				for _, k := range newAPIDisableKeywords {
					if strings.Contains(strings.ToLower(text), strings.ToLower(k)) {
						t.Errorf("%s in format %d says %q:\n%s", row.name, f, k, text)
					}
				}
			}
		}
	}
}
