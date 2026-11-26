package suppliers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/pgtest"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// signedKind is a cloud platform made here, not taken from a platform's package, so that taking one of those out
// leaves this package building (just adapters-removable). It sends a call to <origin>/<region>/<model as the region
// names it>, with the key in a header, refuses to sign a body that says so, and marks the answers it gives.
type signedKind struct{ fakeKind }

func (signedKind) Kind() adapter.Kind        { return "signedapi" }
func (signedKind) Calls() adapter.CallQuirks { return signedQuirks{} }

type signedQuirks struct{ fakeQuirks }

func (signedQuirks) Sign(ctx context.Context, _ adapter.Doer, c adapter.SignedCall) (*http.Request, *adapter.Failure) {
	if string(c.Body) == "refuse" {
		return nil, &adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Origin+"/"+c.Region+"/"+c.Model, bytes.NewReader(c.Body))
	if err != nil {
		return nil, &adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	}
	req.Header.Set("X-Key", c.Key.Reveal())
	return req, nil
}

func (signedQuirks) Answer(resp *http.Response) *http.Response {
	resp.Header.Set("X-Answered", "signed")
	return resp
}

// TestCloudCallsAreSignedByTheirPlatform: a cloud credential's call is the request its platform's adapter makes, from
// the credential's region and the model as that region names it (call_names), and its answer is the one the adapter
// gives back; a count is refused unsent; a call the adapter refuses to sign for the credential's sake fails as the
// credential's, which rests it, and nothing is sent.
func TestCloudCallsAreSignedByTheirPlatform(t *testing.T) {
	t.Parallel()
	w := newWorldWith(t, 4, fakeKind{}, signedKind{})
	if err := pgtest.Run(t.Context(), w.database.Superuser,
		"UPDATE suppliers.suppliers SET mode = 'cloud', kind = 'signedapi', balance_native = NULL WHERE id = '"+w.supplier.String()+"';"+
			"UPDATE suppliers.credentials SET region = 'eu-central-1', call_names = '{\"gpt\":\"eu.gpt-v1:0\"}' WHERE id = '"+w.credential.String()+"'"); err != nil {
		t.Fatal(err)
	}
	w.restart()
	var (
		mu        sync.Mutex
		path, key string
		asked     int
	)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		asked++
		path, key = r.URL.Path, r.Header.Get("X-Key")
	}))
	t.Cleanup(server.Close)
	w.pointAt(server.URL)

	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	resp, f := a.Send(ctx, Call{Format: protocol.Messages, Body: []byte(`{}`)})
	if f != nil {
		t.Fatalf("a signed call: %+v", f)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	a.Done()
	mu.Lock()
	sentPath, sentKey := path, key
	mu.Unlock()
	if sentPath != "/eu-central-1/eu.gpt-v1:0" || sentKey != "sk-test-key" || resp.Header.Get("X-Answered") != "signed" {
		t.Errorf("sent to %s with key %q; the answer was marked %q", sentPath, sentKey, resp.Header.Get("X-Answered"))
	}
	sent := func() int {
		mu.Lock()
		defer mu.Unlock()
		return asked
	}
	checkCountUnsent(w, sent)
	checkRefusedSigning(w, sent)
}

// checkCountUnsent: a platform's own count API is not called; a count is refused unsent, as the request's failure.
func checkCountUnsent(w *world, asked func() int) {
	w.t.Helper()
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		w.t.Fatal(err)
	}
	defer a.Done()
	resp, f := a.Send(w.t.Context(), Call{Format: protocol.Messages, Count: true, Body: []byte(`{}`)})
	if resp != nil {
		if err := resp.Body.Close(); err != nil {
			w.t.Error(err)
		}
	}
	if f == nil || f.Executed != NotExecuted || f.Scope != ScopeRequest || asked() != 1 {
		w.t.Errorf("a count on a cloud platform: %+v, %d requests sent", f, asked())
	}
}

// checkRefusedSigning: a call the platform refused to sign for the credential's sake fails as the credential's,
// sends nothing, and rests the credential.
func checkRefusedSigning(w *world, asked func() int) {
	w.t.Helper()
	t := w.t
	_, f, _ := w.send(protocol.Messages, "refuse")
	if _, rejected := f.Cause.(adapter.CredentialRejected); f == nil || !rejected || f.Scope != ScopeCredential || f.Executed != NotExecuted || asked() != 1 {
		t.Errorf("a call the platform refused to sign for: %+v, %d requests sent", f, asked())
	}
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Errorf("a credential refused at signing is picked again at once: %v", err)
	}
}
