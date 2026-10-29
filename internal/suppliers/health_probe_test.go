package suppliers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"uuid"

	"axiom/internal/protocol"
)

func TestHealthProbeRotatesCredentials(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	second := w.anotherKey(4)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusInternalServerError) }))
	t.Cleanup(server.Close)
	w.pointAt(server.URL)
	var previous uuid.UUID
	for i := range 3 {
		result, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat, AvoidCredential: previous})
		if err != nil {
			t.Fatal(err)
		}
		selected := result.CredentialID()
		if selected == previous {
			t.Fatalf("probe %d reused its previous key", i)
		}
		if selected != w.credential && selected != second {
			t.Fatalf("unknown key %v", selected)
		}
		previous = selected
	}
}
