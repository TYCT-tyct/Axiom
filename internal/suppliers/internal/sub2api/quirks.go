package sub2api

import (
	"bytes"
	"cmp"
	"net/http"
	"regexp"
	"strings"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// quirks is the call half: what is particular to sub2api when a request is sent to it.
type quirks struct{}

// Formats lists what a group serves. M1 sells only standard groups without peak pricing on a platform it speaks:
// subscription groups draw on windows, not the wallet, and a peak multiplier changes the price by the hour, neither of
// which a cost plan can express yet. Facts that cannot be read serve nothing.
func (quirks) Formats(g adapter.GroupFacts) []protocol.Format {
	var f facts
	if err := jsonx.Unmarshal(g, &f); err != nil || f.Status != "active" || f.Subscription != "standard" || f.Peak {
		return nil
	}
	switch f.Platform {
	case "anthropic":
		return []protocol.Format{protocol.Messages}
	case "openai":
		return []protocol.Format{protocol.Responses, protocol.Chat}
	default:
		return nil
	}
}

// chargeRefPattern is what sub2api puts in X-Client-Request-Id: a UUID. Anything else is not taken for a reference.
var chargeRefPattern = regexp.MustCompile(`^[0-9A-Za-z-]{8,64}$`)

// ChargeRef reads the request id the relay returns. The charge record of that request names it with a "client:"
// prefix (checked against an instance, 0.2.16): the relay mints the id itself and ignores one sent to it.
func (quirks) ChargeRef(h http.Header) (adapter.ChargeRef, bool) {
	v := h.Get("X-Client-Request-Id")
	if !chargeRefPattern.MatchString(v) {
		return "", false
	}
	return adapter.ChargeRef("client:" + v), true
}

// Policy: sub2api streams, and reads the upstream answer to its end when our client leaves, charging for all of it
// (checked against an instance, 0.2.16).
func (quirks) Policy() adapter.CallPolicy { return adapter.CallPolicy{Streams: true, Cancels: false} }

// Classify turns an error answer into a Failure. It reads codes, never text, and keeps none of it.
func (quirks) Classify(status int, h http.Header, body []byte) adapter.Failure {
	if isPage(body) {
		return adapter.Failure{Scope: adapter.ScopeEgress, Cause: adapter.Unknown{Status: status, SuspectedEgressBlock: true}}
	}
	if f, ok := refusal(status, errorCode(body), nestedCode(body), h); ok {
		return f
	}
	switch {
	case status == http.StatusServiceUnavailable:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Overloaded{}}
	case status >= http.StatusInternalServerError:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.UpstreamError{Status: status}}
	default:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Unknown{Status: status}}
	}
}

// refusal recognises the answers by which the relay says it refused before running the request, hence NotExecuted.
func refusal(status int, code, nested string, h http.Header) (adapter.Failure, bool) {
	refused := func(scope adapter.Scope, cause adapter.Cause) (adapter.Failure, bool) {
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: scope, Cause: cause}, true
	}
	switch {
	case status == http.StatusForbidden && code == "INSUFFICIENT_BALANCE":
		return refused(adapter.ScopeSupplier, adapter.SupplierBalanceLow{})
	case status == http.StatusUnauthorized && code == "INVALID_API_KEY":
		return refused(adapter.ScopeCredential, adapter.CredentialRejected{})
	case status == http.StatusTooManyRequests:
		scope, cause := tooMany(code, h)
		return refused(scope, cause)
	case nested == "context_length_exceeded":
		return refused(adapter.ScopeRequest, adapter.ContextTooLong{})
	case nested == "model_not_found":
		return refused(adapter.ScopeEndpoint, adapter.ModelNotFound{})
	case status == http.StatusBadRequest:
		return refused(adapter.ScopeRequest, adapter.BadRequest{})
	}
	return adapter.Failure{}, false
}

// tooMany tells a used-up allowance of a key from a rate limit. rate_limit_exceeded is also what a used-up daily
// window answers, so it is the key that is held back; the module reads the key's state to tell the two apart.
func tooMany(code string, h http.Header) (adapter.Scope, adapter.Cause) {
	switch code {
	case "API_KEY_QUOTA_EXHAUSTED", "USAGE_LIMIT_EXCEEDED":
		return adapter.ScopeCredential, adapter.CredentialQuotaExhausted{}
	case "rate_limit_exceeded":
		return adapter.ScopeCredential, adapter.RateLimited{RetryAfter: retryAfter(h)}
	default:
		return adapter.ScopeEndpoint, adapter.RateLimited{RetryAfter: retryAfter(h)}
	}
}

// isPage reports a body that is a web page, which is what a firewall or an anti-bot check answers with.
func isPage(body []byte) bool {
	head := bytes.ToLower(bytes.TrimSpace(body[:min(len(body), 256)]))
	return bytes.HasPrefix(head, []byte("<!doctype")) || bytes.HasPrefix(head, []byte("<html"))
}

// nestedCode is the code of an upstream-style error, {"error":{"code":..}} or {"error":{"type":..}}, which the relay
// passes through.
func nestedCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if jsonx.Unmarshal(body, &e) != nil {
		return ""
	}
	return strings.ToLower(cmp.Or(e.Error.Code, e.Error.Type))
}
