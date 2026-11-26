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

// TestCheckRegion: the resource's deployments that are up are its models, named by their deployments and known by
// the models they deploy, asked for with the key in api-key; a key the resource refuses fails.
func TestCheckRegion(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Api-Key") != "az-good-key-0042" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/openai/deployments" || r.URL.Query().Get("api-version") != listVersion {
			t.Errorf("asked %s", r.URL)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"prod-4o","model":"gpt-4o","status":"succeeded"},{"id":"new","model":"gpt-5","status":"creating"}],"object":"list"}`) //nolint:errcheck // a test server
	}))
	t.Cleanup(server.Close)
	at := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	got := New().CheckRegion(t.Context(), server.Client(), server.URL, "", secret.New("az-good-key-0042"), at)
	if got.Outcome != adapter.KeyPassed || len(got.Models) != 1 || got.Models[0] != (adapter.CloudModel{Name: "prod-4o", Known: "gpt-4o"}) {
		t.Errorf("the check is %+v", got)
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
