package protocol_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// TestFaultOf: a stream that fails says why, by the class its error names (the code before the class when Chat names
// both), so that quality can tell the endpoint's failures from the request's; the error is ErrUpstream either way.
func TestFaultOf(t *testing.T) {
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
		{"Anthropic invalid request", protocol.Messages, anthropicError(`{"type":"invalid_request_error"}`), protocol.FaultBadRequest},
		{"Anthropic unknown class", protocol.Messages, anthropicError(`{"type":"billing_error"}`), protocol.FaultUnrecognised},
		{"Anthropic class not a string", protocol.Messages, anthropicError(`{"type":5}`), protocol.FaultUnrecognised},
		{"Anthropic error not an object", protocol.Messages, anthropicError(`"overloaded_error"`), protocol.FaultUnrecognised},
		{"Anthropic stream cut short", protocol.Messages, messageStart(`{"input_tokens":3}`) + textBlock, protocol.FaultBroken},
		{"Chat server error", protocol.Chat, chatError(`{"type":"server_error","code":null}`), protocol.FaultServer},
		{"Chat code over type", protocol.Chat, chatError(`{"type":"invalid_request_error","code":"context_length_exceeded"}`), protocol.FaultContextTooLong},
		{"Chat moderation", protocol.Chat, chatError(`{"code":"content_filter"}`), protocol.FaultModeration},
		{"Chat no class", protocol.Chat, chatError(`{"message":"x"}`), protocol.FaultUnrecognised},
		{"Chat malformed", protocol.Chat, "data: {\"choices\":\n\n", protocol.FaultBroken},
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
