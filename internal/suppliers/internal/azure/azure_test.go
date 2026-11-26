package azure

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// TestCheckRegion: the check is one request the resource answers on the version calls use, with the key in api-key,
// so a resource that answers no retired version passes; it lists no models (a person names the deployments), and a key
// the resource refuses fails. The fake answers as a current resource does: the models it can deploy, and not found on
// a path or version it does not serve.
func TestCheckRegion(t *testing.T) {
	t.Parallel()
	asked := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		switch {
		case r.Header.Get("Api-Key") != "az-good-key-0042":
			w.WriteHeader(http.StatusUnauthorized)
		case r.Method != http.MethodGet || r.URL.Path != "/openai/models" || r.URL.Query().Get("api-version") != apiVersion:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"404","message":"Resource not found"}}`) //nolint:errcheck // a test server
		default:
			_, _ = io.WriteString(w, `{"data":[{"id":"gpt-4o-2024-08-06","status":"succeeded"}],"object":"list"}`) //nolint:errcheck // a test server
		}
	}))
	t.Cleanup(server.Close)
	at := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	got := New().CheckRegion(t.Context(), server.Client(), server.URL, "", secret.New("az-good-key-0042"), at)
	if got.Outcome != adapter.KeyPassed || got.Models != nil || asked != 1 {
		t.Errorf("the check is %+v after %d requests", got, asked)
	}
	refused := New().CheckRegion(t.Context(), server.Client(), server.URL, "", secret.New("az-bad-key-0001"), at)
	if refused.Outcome != adapter.KeyFailed || refused.Cause != adapter.CauseRejected {
		t.Errorf("a refused key: %+v", refused)
	}
}

// TestSign: Chat goes to the deployment's path with the api-version, as it is, the key in api-key.
func TestSign(t *testing.T) {
	t.Parallel()
	req, f := quirks{}.Sign(t.Context(), nil, adapter.SignedCall{
		Format: protocol.Chat, Origin: "https://harbor.openai.azure.com", Model: "prod-4o", Body: []byte(`{"model":"prod-4o"}`), Key: secret.New("az-good-key-0042"),
	})
	if f != nil {
		t.Fatal(f)
	}
	if req.URL.String() != "https://harbor.openai.azure.com/openai/deployments/prod-4o/chat/completions?api-version="+apiVersion || req.Header.Get("Api-Key") != "az-good-key-0042" {
		t.Errorf("sent %s with %v", req.URL, req.Header)
	}
	if _, f := (quirks{}).Sign(t.Context(), nil, adapter.SignedCall{Format: protocol.Messages}); f == nil {
		t.Error("Messages was signed for Azure OpenAI")
	}
}

// TestClassify: the resource's content filter is the content's, a context too long is the endpoint's, a 401 the key's.
func TestClassify(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status int
		body   string
		want   adapter.Failure
	}{
		{400, `{"error":{"code":"content_filter"}}`, adapter.Failure{Scope: adapter.ScopeRequest, Cause: adapter.ContentRejected{}}},
		{400, `{"error":{"code":"context_length_exceeded"}}`, adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.ContextTooLong{}}},
		{401, `{"error":{"code":"401"}}`, adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}},
	} {
		if got := (quirks{}).Classify(tc.status, http.Header{}, []byte(tc.body)); got != tc.want {
			t.Errorf("%d %s: %+v", tc.status, tc.body, got)
		}
	}
}
