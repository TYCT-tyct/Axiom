package openai

import (
	"net/http"
	"testing"

	"axiom/internal/suppliers/internal/adapter"
)

// TestClassify: only a 401 is the key's fault; a 403 may be a region or a model the key may not use, so it must not
// mark a working key rejected.
//
//nolint:cyclop,gocognit // one table, one assertion per cause type
func TestClassify(t *testing.T) {
	t.Parallel()
	a := New()
	for _, tc := range []struct {
		status int
		body   string
		want   any
	}{
		{http.StatusUnauthorized, `{"error":{"code":"invalid_api_key"}}`, adapter.CredentialRejected{}},
		{http.StatusForbidden, `{"error":{"code":"country_not_supported"}}`, adapter.NotPermitted{}},
		{http.StatusTooManyRequests, `{}`, adapter.RateLimited{}},
		{http.StatusBadRequest, `{"error":{"code":"context_length_exceeded"}}`, adapter.ContextTooLong{}},
		{http.StatusBadRequest, `{}`, adapter.BadRequest{}},
		{http.StatusNotFound, `{"error":{"code":"model_not_found"}}`, adapter.ModelNotFound{}},
		{http.StatusInternalServerError, `{}`, adapter.UpstreamError{}},
	} {
		f := a.Calls().Classify(tc.status, http.Header{}, []byte(tc.body))
		switch tc.want.(type) {
		case adapter.CredentialRejected:
			if _, ok := f.Cause.(adapter.CredentialRejected); !ok {
				t.Errorf("%d: cause %T, want CredentialRejected", tc.status, f.Cause)
			}
		case adapter.NotPermitted:
			if _, ok := f.Cause.(adapter.NotPermitted); !ok {
				t.Errorf("%d: cause %T, want NotPermitted", tc.status, f.Cause)
			}
		case adapter.RateLimited:
			if _, ok := f.Cause.(adapter.RateLimited); !ok {
				t.Errorf("%d: cause %T, want RateLimited", tc.status, f.Cause)
			}
		case adapter.ContextTooLong:
			if _, ok := f.Cause.(adapter.ContextTooLong); !ok {
				t.Errorf("%d: cause %T, want ContextTooLong", tc.status, f.Cause)
			}
			if f.Scope != adapter.ScopeEndpoint {
				t.Errorf("%d: scope %v, want ScopeEndpoint so routing tries another endpoint", tc.status, f.Scope)
			}
		case adapter.BadRequest:
			if _, ok := f.Cause.(adapter.BadRequest); !ok {
				t.Errorf("%d: cause %T, want BadRequest", tc.status, f.Cause)
			}
		case adapter.ModelNotFound:
			if _, ok := f.Cause.(adapter.ModelNotFound); !ok {
				t.Errorf("%d: cause %T, want ModelNotFound", tc.status, f.Cause)
			}
		case adapter.UpstreamError:
			if _, ok := f.Cause.(adapter.UpstreamError); !ok {
				t.Errorf("%d: cause %T, want UpstreamError", tc.status, f.Cause)
			}
		}
	}
}
