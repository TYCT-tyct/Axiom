package protocol_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/protocol"
)

// TestFaultOf: a stream that fails says why, by the class its error names (the code before the class when Chat names
// both), so that quality can tell the endpoint's failures from the request's; the error is ErrUpstream either way.
func TestFaultOf(t *testing.T) {
	t.Parallel()
	anthropicError := func(class string) string {
		return messageStart(`{"input_tokens":3}`) + "event: error\ndata: {\"type\":\"error\",\"error\":" + class + "}\n\n"
	}
	chatError := func(object string) string { return "data: {\"error\":" + object + "}\n\n" }
	for _, c := range []struct {
		name   string
		f      protocol.Format
		stream string
		want   protocol.Fault
	}{
		{"Anthropic overloaded", protocol.Messages, anthropicError(`{"type":"overloaded_error","message":"x"}`), protocol.FaultServer},
		{"Anthropic api_error", protocol.Messages, anthropicError(`{"type":"api_error"}`), protocol.FaultServer},
		{"Anthropic rate limit", protocol.Messages, anthropicError(`{"type":"rate_limit_error"}`), protocol.FaultRateLimited},
		{"Anthropic escaped invalid request", protocol.Messages, anthropicError(`{"type":"invalid_\u0072equest_error"}`), protocol.FaultBadRequest},
		{"Chat escaped code", protocol.Chat, chatError(`{"type":"invalid_request_error","code":"context_\u006cength_exceeded"}`), protocol.FaultContextTooLong},
		{"Responses type fallback", protocol.Responses, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"type\":\"invalid_request_error\"}}}\n\n", protocol.FaultBadRequest},
		{"Anthropic invalid request", protocol.Messages, anthropicError(`{"type":"invalid_request_error"}`), protocol.FaultBadRequest},
		{"Anthropic unknown class", protocol.Messages, anthropicError(`{"type":"billing_error"}`), protocol.FaultUnrecognised},
		{"Anthropic class not a string", protocol.Messages, anthropicError(`{"type":5}`), protocol.FaultUnrecognised},
		{"Anthropic error not an object", protocol.Messages, anthropicError(`"overloaded_error"`), protocol.FaultUnrecognised},
		{"Anthropic stream cut short", protocol.Messages, messageStart(`{"input_tokens":3}`) + textBlock, protocol.FaultBroken},
		{"Chat server error", protocol.Chat, chatError(`{"type":"server_error","code":null}`), protocol.FaultServer},
		{"Chat code over type", protocol.Chat, chatError(`{"type":"invalid_request_error","code":"context_length_exceeded"}`), protocol.FaultContextTooLong},
		{"Chat moderation", protocol.Chat, chatError(`{"code":"content_filter"}`), protocol.FaultModeration},
		{"Chat permission", protocol.Chat, chatError(`{"code":"permission_denied"}`), protocol.FaultBadRequest},
		{"native upstream permission is not the client's", protocol.Chat, chatError(`{"type":"permission_error"}`), protocol.FaultUnrecognised},
		{"Chat payload", protocol.Chat, chatError(`{"code":"payload_too_large"}`), protocol.FaultBadRequest},
		{"Chat numeric request code", protocol.Chat, chatError(`{"code":400,"message":"bad input"}`), protocol.FaultBadRequest},
		{"Chat numeric rate limit code", protocol.Chat, chatError(`{"code":429}`), protocol.FaultRateLimited},
		{"Chat no class", protocol.Chat, chatError(`{"message":"x"}`), protocol.FaultUnrecognised},
		{"Chat malformed", protocol.Chat, "data: {\"choices\":\n\n", protocol.FaultBroken},
		{"Responses parameter error", protocol.Responses, `data: {"type":"error","code":"invalid_value","message":"bad parameter","param":"input"}` + "\n\n", protocol.FaultBadRequest},
		{"Responses relay error envelope", protocol.Responses, `data: {"type":"error","error":{"type":"invalid_request_error","message":"bad"}}` + "\n\n", protocol.FaultBadRequest},
		{"Responses error event", protocol.Responses, "event: error\ndata: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\"}\n\n", protocol.FaultRateLimited},
		{"Responses failed", protocol.Responses, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\"}}}\n\n", protocol.FaultServer},
		{"Responses prompt refused", protocol.Responses, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"invalid_prompt\"}}}\n\n", protocol.FaultBadRequest},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := relay(t, c.f, strings.NewReader(c.stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if !errors.Is(got.err, protocol.ErrUpstream) || protocol.FaultOf(got.err) != c.want {
				t.Errorf("error %v, fault %d, want ErrUpstream with fault %d", got.err, protocol.FaultOf(got.err), c.want)
			}
		})
	}
	t.Run("a tool input that is not JSON", func(t *testing.T) {
		stream := messageStart(`{"input_tokens":3}`) + startBlock(0, `{"type":"tool_use","id":"t","name":"f"}`) + input(0, `{\"a\":`) + stopBlock(0)
		got := aggregate(t, protocol.Messages, protocol.Messages, strings.NewReader(stream), protocol.NewMemory(0), unsettled)
		if !errors.Is(got.err, protocol.ErrUpstream) || protocol.FaultOf(got.err) != protocol.FaultToolInput {
			t.Errorf("error %v, fault %d", got.err, protocol.FaultOf(got.err))
		}
	})
	for err, want := range map[error]protocol.Fault{nil: protocol.FaultNone, protocol.ErrMemory: protocol.FaultNone, io.ErrUnexpectedEOF: protocol.FaultBroken} {
		if got := protocol.FaultOf(err); got != want {
			t.Errorf("FaultOf(%v) = %d, want %d", err, got, want)
		}
	}
}

// TestRejectionOf: an error event's words and the members a client branches on are the stream's to give, and a failure
// that named none of them gives none: a stream that broke off says nothing of the request, and a member of another type
// is no words. What is given still holds the supplier's identity, which the supplier takes out
// (docs/domains/gateway.md「错误」).
func TestRejectionOf(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		f      protocol.Format
		stream string
		want   *protocol.Rejection
	}{
		{
			"Messages", protocol.Messages, messageStart(`{"input_tokens":3}`) +
				"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"x\"}}\n\n",
			&protocol.Rejection{Message: "x", Type: "invalid_request_error"},
		},
		{
			"an error event without words", protocol.Messages, messageStart(`{"input_tokens":3}`) +
				"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\"}}\n\n",
			&protocol.Rejection{Type: "api_error"},
		},
		{"a stream that broke off", protocol.Messages, messageStart(`{"input_tokens":3}`) + textBlock, nil},
		{"a member of another type", protocol.Chat, "data: {\"error\":{\"message\":5,\"code\":\"x\"}}\n\n", &protocol.Rejection{Code: "x"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := relay(t, c.f, strings.NewReader(c.stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if diff := cmp.Diff(c.want, protocol.RejectionOf(got.err)); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

// Oversized fields are omitted whole so sanitization cannot miss a truncated credential.
func TestRejectionOfIsBounded(t *testing.T) {
	t.Parallel()
	for _, value := range []string{strings.Repeat("é", 3000), strings.Repeat(`\uD83D\uDE00`, 600)} {
		got := relay(t, protocol.Chat, strings.NewReader(`data: {"error":{"message":"`+value+`","code":"invalid_value"}}`+"\n\n"),
			protocol.UsageContext{}, protocol.NewMemory(0), settled)
		r := protocol.RejectionOf(got.err)
		if r == nil || r.Message != "" || r.Code != "invalid_value" {
			t.Errorf("rejection %v, want oversized message omitted", r)
		}
	}
}
