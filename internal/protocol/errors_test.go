package protocol_test

import (
	"fmt"
	"maps"
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
				switch diff := cmp.Diff(wantError(f, row.name, row.messages), typedError(got)); {
				case err != nil || status != row.status:
					t.Errorf("status %d, want %d; %v:\n%s", status, row.status, err, body)
				case diff != "":
					t.Errorf("error (-want +got):\n%s\n%s", diff, body)
				case !cmp.Equal(chargedUsage(f), axiom["usage"]):
					t.Errorf("usage (-want +got):\n%s", cmp.Diff(chargedUsage(f), axiom["usage"]))
				}
			})
		}
	}
}

// chargedUsage is the usage object the settlement of 7 input and 2 output tokens makes in format f: its own fields in
// full, and no other format's.
func chargedUsage(f protocol.Format) map[string]any {
	cost := map[string]any{"cost": 3.703701, "cost_currency": "USD"}
	var u map[string]any
	switch f {
	case protocol.Messages:
		u = map[string]any{
			"input_tokens": 7.0, "cache_creation_input_tokens": 0.0, "cache_read_input_tokens": 0.0,
			"cache_creation":  map[string]any{"ephemeral_5m_input_tokens": 0.0, "ephemeral_1h_input_tokens": 0.0},
			"output_tokens":   2.0,
			"server_tool_use": map[string]any{"web_search_requests": 0.0},
		}
	case protocol.Responses:
		u = map[string]any{
			"input_tokens": 7.0, "input_tokens_details": map[string]any{"cached_tokens": 0.0, "cache_write_tokens": 0.0},
			"output_tokens": 2.0, "output_tokens_details": map[string]any{"reasoning_tokens": 0.0}, "total_tokens": 9.0,
		}
	case protocol.Chat:
		u = map[string]any{
			"prompt_tokens": 7.0, "prompt_tokens_details": map[string]any{"cached_tokens": 0.0, "cache_write_tokens": 0.0},
			"completion_tokens": 2.0, "completion_tokens_details": map[string]any{"reasoning_tokens": 0.0}, "total_tokens": 9.0,
		}
	}
	maps.Copy(u, cost)
	return u
}

// wantError is what typedError should find in an error of type name in format f.
func wantError(f protocol.Format, name, messages string) map[string]any {
	want := map[string]any{"error_type": name, "limit_source": "balance", "has_message": true, "type": messages}
	if f != protocol.Messages {
		want["code"], want["param"] = name, ""
	}
	return want
}

// typedError gathers what an error body says of its type: Messages' error object, or Chat's type, param and code with
// the members of its metadata.
func typedError(body map[string]any) map[string]any {
	e, _ := body["error"].(map[string]any)
	got := map[string]any{"has_message": e["message"] != nil && e["message"] != ""}
	src := e
	if meta, ok := e["metadata"].(map[string]any); ok {
		src, got["type"], got["code"], got["param"] = meta, e["type"], e["code"], e["param"]
	}
	for k, v := range src {
		if k != "message" {
			got[k] = v
		}
	}
	return got
}

// TestRenderRejection: what the upstream said of a request it refused reaches the client in its own words, with the
// type, param and code it gave (null when it gave no code), in the shells that have them; Messages keeps its own type.
func TestRenderRejection(t *testing.T) {
	openAI := &protocol.Rejection{
		Message: "Invalid value for 'max_tokens': expected a value above 0.", Type: "invalid_request_error", Param: "max_tokens", Code: "invalid_value",
	}
	claude := &protocol.Rejection{Message: "max_tokens: Input should be greater than 0", Type: "invalid_request_error"}
	for _, c := range []struct {
		name   string
		f      protocol.Format
		r      *protocol.Rejection
		wantIn []string
	}{
		{"OpenAI's error in Chat", protocol.Chat, openAI, []string{
			`"message":"Invalid value for 'max_tokens': expected a value above 0."`, `"type":"invalid_request_error"`,
			`"param":"max_tokens"`, `"code":"invalid_value"`, `"error_type":"invalid_request"`,
		}},
		{"OpenAI's error before a Responses stream", protocol.Responses, openAI, []string{`"param":"max_tokens"`, `"code":"invalid_value"`}},
		{"Anthropic's error in Chat", protocol.Chat, claude, []string{
			`"message":"max_tokens: Input should be greater than 0"`, `"type":"invalid_request_error"`, `"param":""`, `"code":null`,
		}},
		{"OpenAI's error in Messages", protocol.Messages, openAI, []string{
			`"message":"Invalid value for 'max_tokens': expected a value above 0."`, `"type":"invalid_request_error"`, `"error_type":"invalid_request"`,
		}},
		{"a type of the upstream's own in Chat", protocol.Chat, &protocol.Rejection{Message: "m", Type: "image_parse_error", Code: "bad_image"}, []string{
			`"type":"image_parse_error"`, `"code":"bad_image"`,
		}},
		{"a type of the upstream's own in Messages", protocol.Messages, &protocol.Rejection{Message: "m", Type: "weird"}, []string{`"type":"invalid_request_error"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			status, body := protocol.RenderError(c.f, protocol.Error{Type: protocol.InvalidRequest, Rejection: c.r}, nil)
			if status != 400 {
				t.Errorf("status %d", status)
			}
			for _, want := range c.wantIn {
				if !strings.Contains(string(body), want) {
					t.Errorf("no %s in %s", want, body)
				}
			}
			if c.f == protocol.Messages && strings.Contains(string(body), "param") {
				t.Errorf("a param in Anthropic's shell: %s", body)
			}
		})
	}
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
