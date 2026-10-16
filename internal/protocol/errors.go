package protocol

import (
	"fmt"

	"axiom/internal/catalog"
)

// ErrorType is the unified error type a client sees in every error, whatever its format (docs/domains/gateway.md
// 「错误」).
type ErrorType uint8

// The error types, one per row of the status table. NoEndpoint and NoEndpointForNow share a name: no endpoint can
// serve the request, for good (the filters left none) or for now (every endpoint is broken or out of budget).
const (
	Authentication ErrorType = iota
	CreditsInsufficient
	BillingUnavailable
	RateLimited
	Overloaded
	ModelNotFound
	NoEndpoint
	NoEndpointForNow
	InvalidRequest
	RequestTooLarge
	UpstreamFailed
	Timeout
)

// errorTypes are each type's name, HTTP status, Messages type and the message it says when the caller gives none.
// The messages use our own words, never an upstream's, and none of the phrases that make new-api disable a channel
// (TestErrorMessages).
var errorTypes = [...]struct {
	name, messages, message string
	status                  int
}{
	Authentication:      {"authentication", "authentication_error", "The API key is invalid, expired or disabled, or may not be used from here.", 401},
	CreditsInsufficient: {"credits_insufficient", "billing_error", "The balance or a spending limit cannot cover this request.", 402},
	BillingUnavailable:  {"billing_unavailable", "api_error", "Billing is unavailable for now, so the response was cut short.", 503},
	RateLimited:         {"rate_limited", "rate_limit_error", "Too many requests at once; retry after the time Retry-After gives.", 429},
	Overloaded:          {"overloaded", "overloaded_error", "The service is busy; retry shortly.", 503},
	ModelNotFound:       {"model_not_found", "not_found_error", "The model does not exist, or this key may not use it.", 404},
	NoEndpoint:          {"no_endpoint", "not_found_error", "No endpoint can serve this request.", 404},
	NoEndpointForNow:    {"no_endpoint", "overloaded_error", "No endpoint can serve this request right now; retry later.", 503},
	InvalidRequest:      {"invalid_request", "invalid_request_error", "The request is not valid.", 400},
	RequestTooLarge:     {"request_too_large", "request_too_large", "The request is too large.", 413},
	UpstreamFailed:      {"upstream_error", "api_error", "The model provider failed to complete the request.", 502},
	Timeout:             {"timeout", "timeout_error", "The request ran out of time.", 408},
}

// Error is an error to answer or to end a stream with. Details are members of a JSON object that say more, such as
// "limit_source":"balance" for a 402, each led by a comma; they go next to the error type.
type Error struct {
	Type    ErrorType
	Message string
	Details string
}

// message is what the error says: the caller's message, or the type's own.
func (e Error) message() string {
	if e.Message != "" {
		return e.Message
	}
	return errorTypes[e.Type].message
}

// appendErrorType appends the error type member and the details.
func appendErrorType(dst []byte, e Error) []byte {
	return append(append(dst, `"error_type":"`+errorTypes[e.Type].name+`"`...), e.Details...)
}

// RenderError returns the status and the body of an error answered before any stream starts, in format f's own
// shell: Chat's for Chat and Responses, whose error responses use it, and Messages' with its own type. With fin, the
// body carries what was charged anyway, such as a web search already run, in an axiom object: the usage in f's
// fields, and the cost unless the settlement is pending (docs/domains/gateway.md「用量与费用」「错误」).
func RenderError(f Format, e Error, fin *Final) (status int, body []byte) {
	t := errorTypes[e.Type]
	if f == Messages {
		body = fmt.Appendf(nil, `{"type":"error","error":{"type":%q,"message":`, t.messages)
		body = append(appendErrorType(append(appendQuoted(body, e.message()), ','), e), '}')
	} else {
		body = fmt.Appendf(nil, `{"error":{"code":%d,"message":`, t.status)
		body = append(appendErrorType(append(appendQuoted(body, e.message()), `,"metadata":{`...), e), "}}"...)
	}
	if fin != nil {
		body = append(appendCharged(append(body, `,"axiom":{"usage":`...), f, fin), '}')
	}
	return t.status, append(body, '}')
}

// appendCharged appends the reply's usage of fin in format f's fields, with its cost, and the pending flag when the
// settlement was not written.
func appendCharged(dst []byte, f Format, fin *Final) []byte {
	u := reply(fin.Usage)
	switch f {
	case Messages:
		dst = appendMessagesUsage(dst, u, cost(fin))
	case Responses:
		dst = appendOpenAIUsage(dst, responsesNames, u, cost(fin))
	case Chat:
		dst = appendOpenAIUsage(dst, chatNames, u, cost(fin))
	}
	if fin.Pending {
		dst = append(dst, `,"billing_pending":true`...)
	}
	return dst
}

// appendMessagesUsage appends u in Messages' usage fields, with cost, which is empty or members each led by a comma.
func appendMessagesUsage(dst []byte, u catalog.Usage, cost string) []byte {
	return fmt.Appendf(dst, `{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,`+
		`"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":%d},"output_tokens":%d,`+
		`"server_tool_use":{"web_search_requests":%d}%s}`,
		u.Count(catalog.ItemInput), u.Count(catalog.ItemCacheWrite)+u.Count(catalog.ItemCacheWrite1h),
		u.Count(catalog.ItemCacheRead), u.Count(catalog.ItemCacheWrite), u.Count(catalog.ItemCacheWrite1h),
		u.OutputTotal(), u.Count(catalog.ItemWebSearch), cost)
}
