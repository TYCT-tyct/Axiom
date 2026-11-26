package direct

import (
	"bytes"
	"net/http"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// errorBody reads both error shapes: OpenAI's {"error":{"code","type"}} and Anthropic's
// {"type":"error","error":{"type"}}. Only the code and type are read: messages differ by provider and language and can
// quote the request.
type errorBody struct {
	Error struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"error"`
}

// invalidKey is how Google says a key is not valid: a 400 whose details name the reason, not a 401.
var invalidKey = []byte(`"API_KEY_INVALID"`)

// Classify implements adapter.CallQuirks. A 401 is the provider refusing the key, and so is Google's 400 that says so;
// a 403 is read as the endpoint's problem (a model the key may not use, or a region the provider does not serve),
// because treating it as the key's would mark a working key when the host's country is blocked. A key whose own
// allowance is used up (OpenAI's insufficient_quota) rests that key alone: each pasted key is an account of its own.
func (quirks) Classify(status int, h http.Header, body []byte) adapter.Failure {
	if adapter.IsHTML(body) { // a page is what a firewall or an anti-bot check answers, not the provider
		return adapter.Failure{Scope: adapter.ScopeEgress, Cause: adapter.Unknown{Status: status, SuspectedEgressBlock: true}}
	}
	var e errorBody
	_ = jsonx.Unmarshal(body, &e) //nolint:errcheck // a malformed error body still classifies by status
	if f, ok := keyFailure(status, body, e); ok {
		return f
	}
	switch {
	case status == http.StatusTooManyRequests:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.RateLimited{RetryAfter: adapter.RetryAfter(h)}}
	case status == http.StatusBadRequest && e.Error.Code == "context_length_exceeded":
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.ContextTooLong{}}
	case status == http.StatusBadRequest, status == http.StatusRequestEntityTooLarge:
		return adapter.Failure{Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	case status == http.StatusNotFound, e.Error.Code == "model_not_found":
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.ModelNotFound{}}
	case status == http.StatusForbidden:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.NotPermitted{}}
	case status == 529, e.Error.Type == "overloaded_error": // Anthropic's own status for no capacity
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Overloaded{}}
	case status >= 500:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.UpstreamError{Status: status}}
	}
	return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Unknown{Status: status}}
}

// keyFailure is a refusal that is the key's alone: not valid (a 401, or Google's 400 that says so), or its own
// allowance used up (OpenAI's insufficient_quota). The provider refused both before running the request.
func keyFailure(status int, body []byte, e errorBody) (adapter.Failure, bool) {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusBadRequest && bytes.Contains(body, invalidKey):
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}, true
	case status == http.StatusTooManyRequests && e.Error.Code == "insufficient_quota":
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialQuotaExhausted{}}, true
	}
	return adapter.Failure{}, false
}
