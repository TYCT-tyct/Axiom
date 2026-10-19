package newapi

import (
	"net/http"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// Calls implements adapter.Adapter.
func (*Adapter) Calls() adapter.CallQuirks { return quirks{} }

type quirks struct{}

// Policy implements adapter.CallQuirks. new-api streams, and passes a client's disconnect on to the channel.
func (quirks) Policy() adapter.CallPolicy { return adapter.CallPolicy{Streams: true, Cancels: true} }

// Formats implements adapter.CallQuirks from the endpoint types the group's facts list; chat, which every new-api
// serves, when they list none.
func (quirks) Formats(g adapter.GroupFacts) []protocol.Format {
	var facts struct {
		Endpoints []string `json:"endpoint_types"`
	}
	if jsonx.Unmarshal(g, &facts) != nil {
		return formatsOf(nil)
	}
	return formatsOf(facts.Endpoints)
}

// ChargeRef implements adapter.CallQuirks: new-api's request id, the one its consumption log records. It is printable
// ASCII of sane length or it is not taken for a request id.
func (quirks) ChargeRef(h http.Header) (adapter.ChargeRef, bool) {
	v := h.Get(requestIDHeader)
	if v == "" || len(v) > 128 {
		return "", false
	}
	for _, r := range v {
		if r <= ' ' || r > '~' {
			return "", false
		}
	}
	return adapter.ChargeRef(v), true
}

// errorBody is new-api's relay error: {"error":{"code","message","type"}}. Only the code and type are read, because
// messages differ by language and may echo what was sent.
type errorBody struct {
	Error struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"error"`
}

// Classify implements adapter.CallQuirks, from the answers recorded in testdata/. new-api's own errors have the type
// new_api_error; the same status without it is the channel's upstream passing through, which says nothing about our
// key or balance, so it stays unknown.
func (quirks) Classify(status int, h http.Header, body []byte) adapter.Failure {
	if isHTML(body) {
		return adapter.Failure{Scope: adapter.ScopeEgress, Cause: adapter.Unknown{Status: status, SuspectedEgressBlock: true}}
	}
	if status == http.StatusTooManyRequests {
		return rateLimited(h)
	}
	var e errorBody
	if jsonx.Unmarshal(body, &e) == nil && e.Error.Type == "new_api_error" {
		if f, ok := ownFailure(status, e.Error.Code); ok {
			return f
		}
	}
	switch {
	case status == http.StatusBadRequest:
		return adapter.Failure{Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	case status >= 500:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.UpstreamError{Status: status}}
	}
	return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Unknown{Status: status}}
}

// ownFailure classifies an error new-api made itself, by status and code. It reports false for a code it does not know.
func ownFailure(status int, code string) (adapter.Failure, bool) {
	switch {
	case status == http.StatusForbidden && code == "insufficient_user_quota":
		return refused(adapter.ScopeSupplier, adapter.SupplierBalanceLow{}), true
	case status == http.StatusForbidden && code == "pre_consume_token_quota_failed":
		return refused(adapter.ScopeCredential, adapter.CredentialQuotaExhausted{}), true
	case status == http.StatusUnauthorized && code == "":
		return refused(adapter.ScopeCredential, adapter.CredentialRejected{}), true
	case code == "model_not_found" && status < 500:
		return refused(adapter.ScopeEndpoint, adapter.ModelNotFound{}), true
	case code == "model_not_found":
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Overloaded{}}, true
	}
	return adapter.Failure{}, false
}

// rateLimited is a 429, with the wait it states. Whether the request ran is not known: new-api's own limiter answers
// with an empty body (the recorded answer), but an empty body is also what a body that was not read looks like, and the
// channel's upstream or new-api's status mapping of a channel error may answer 429 after the request was run, so it
// stays unknown and the cost is estimated and reconciled.
func rateLimited(h http.Header) adapter.Failure {
	return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.RateLimited{RetryAfter: adapter.RetryAfter(h)}}
}

// refused is a failure by which new-api says it refused before running the request.
func refused(scope adapter.Scope, cause adapter.Cause) adapter.Failure {
	return adapter.Failure{Executed: adapter.NotExecuted, Scope: scope, Cause: cause}
}
