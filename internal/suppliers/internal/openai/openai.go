// Package openai is the call side of OpenAI's own API, used by an account's own key (BYOK) and by nothing else. A BYOK
// supplier is made by the account, never detected or synced, so this adapter has no management half: Detect never
// recognises anything and Open is never reached (docs/domains/accounts.md「自带的供应商 key」).
package openai

import (
	"context"
	"errors"
	"net/http"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// Kind is the adapter's name in suppliers.kind.
const Kind adapter.Kind = "openai"

// Adapter is the OpenAI adapter. It holds nothing: the key comes from the credential the call picked.
type Adapter struct{}

var _ adapter.Adapter = (*Adapter)(nil)

// New is the adapter.
func New() *Adapter { return &Adapter{} }

// Kind implements adapter.Adapter.
func (*Adapter) Kind() adapter.Kind { return Kind }

// Buckets implements adapter.Adapter. The account's own key is bound to the account, whose limits the provider sets;
// the module's own concurrency bound is the credential's, so there is no shared rate window to declare here.
func (*Adapter) Buckets() []adapter.BucketSpec { return nil }

// Detect implements adapter.Adapter. An account's key is never found by probing an address: it is typed in.
func (*Adapter) Detect(context.Context, adapter.Client) (adapter.Detection, error) {
	return adapter.Detection{}, adapter.ErrNotRecognised
}

// Open implements adapter.Adapter. There is no management half to open.
func (*Adapter) Open(context.Context, adapter.Client, adapter.LoginStore) (adapter.Manager, error) {
	return nil, errors.New("openai: BYOK suppliers have no management side")
}

// Calls implements adapter.Adapter.
func (*Adapter) Calls() adapter.CallQuirks { return quirks{} }

type quirks struct{}

// Policy implements adapter.CallQuirks. OpenAI streams and stops generating when the client disconnects.
func (quirks) Policy() adapter.CallPolicy { return adapter.CallPolicy{Streams: true, Cancels: true} }

// Formats implements adapter.CallQuirks. A BYOK endpoint's formats are written from its model's own abilities when the
// key is made, not inferred from a group's listing, so there is nothing to report here.
func (quirks) Formats(adapter.GroupFacts) []protocol.Format { return nil }

// ChargeRef implements adapter.CallQuirks: OpenAI's request id. A BYOK call has no supplier charge to match, but the
// id is still the attempt's reconciliation key and costs nothing to keep.
func (quirks) ChargeRef(h http.Header) (adapter.ChargeRef, bool) {
	v := h.Get("X-Request-ID")
	if v == "" || len(v) > 128 {
		return "", false
	}
	return adapter.ChargeRef(v), true
}

// errorBody is OpenAI's error shape: {"error":{"message","type","param","code"}}. Only the code and type are read:
// messages differ by language and can quote the request.
type errorBody struct {
	Error struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"error"`
}

// Classify implements adapter.CallQuirks. A 401 is the provider refusing the key; a 403 is read as the endpoint's
// problem (a model the key may not use, or a region the provider does not serve), because treating it as a credential
// failure would mark a working key when the host's country is blocked. Only a 401 marks the key.
func (quirks) Classify(status int, h http.Header, body []byte) adapter.Failure {
	if status == http.StatusUnauthorized {
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}
	}
	var e errorBody
	_ = jsonx.Unmarshal(body, &e) //nolint:errcheck // a malformed error body still classifies by status
	switch {
	case status == http.StatusTooManyRequests:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.RateLimited{RetryAfter: adapter.RetryAfter(h)}}
	case status == http.StatusBadRequest && e.Error.Code == "context_length_exceeded":
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.ContextTooLong{}}
	case status == http.StatusBadRequest:
		return adapter.Failure{Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	case status == http.StatusNotFound, e.Error.Code == "model_not_found":
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.ModelNotFound{}}
	case status == http.StatusForbidden:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.NotPermitted{}}
	case status >= 500:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.UpstreamError{Status: status}}
	}
	return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Unknown{Status: status}}
}
