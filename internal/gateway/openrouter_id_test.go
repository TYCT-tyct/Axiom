package gateway

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// TestOpenRouterVersionedModel: a model the catalog keeps under an internal version slug ("acme/versioned@20250929") is
// discovered and fetched under its public standard ID, which the catalog only lists it by. The internal name is not a
// name the API answers: a client that read it could neither fetch the model nor call it.
func TestOpenRouterVersionedModel(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := r.listed("acme/versioned@20250929", basicSpec, cheap)
	if err := r.catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.attachEndpoint(id)
	a := r.newAccount(1e9)
	if ids := r.orIDs(a); !slices.Contains(ids, "acme/versioned") || slices.Contains(ids, "acme/versioned@20250929") {
		t.Fatalf("the list: %v; want acme/versioned and no internal slug", ids)
	}
	m := r.orModel(a, "acme/versioned")
	want := []string{"acme/versioned", "versioned", orListPath + "/acme/versioned"}
	if got := []string{m.CanonicalSlug, m.Name, m.Links.Details}; !slices.Equal(got, want) {
		t.Errorf("the entry: %+v", m)
	}
	resp, body := r.do(http.MethodGet, m.Links.Details, a.key, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"id":"acme/versioned"`) {
		t.Errorf("the detail: %d %s", resp.StatusCode, body)
	}
	if resp, body := r.message(a, m.ID); resp.StatusCode != http.StatusOK {
		t.Errorf("a call to the discovered id: %d %s", resp.StatusCode, body)
	}
	resp, body = r.do(http.MethodGet, orListPath+"/acme/versioned@20250929", a.key, nil)
	if code, _ := errorOf(t, body); resp.StatusCode != http.StatusNotFound || code != "model_not_found" {
		t.Errorf("the internal slug as a detail: %d %s", resp.StatusCode, body)
	}
	if resp, body := r.message(a, "acme/versioned@20250929"); resp.StatusCode == http.StatusOK {
		t.Errorf("a call to the internal slug was served: %d %s", resp.StatusCode, body)
	}
}
