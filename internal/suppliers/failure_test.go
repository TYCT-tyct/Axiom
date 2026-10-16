package suppliers

import (
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// TestFailureQuestionsGatewayAsks: gateway cannot name the adapter's types, so these two answers are all it knows of
// what the supplier did: whether the request ran, and whether it was told to slow down.
func TestFailureQuestionsGatewayAsks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                     string
		f                        adapter.Failure
		notExecuted, rateLimited bool
	}{
		{"the zero failure is unknown", adapter.Failure{}, false, false},
		{"refused before running", adapter.Failure{Executed: adapter.NotExecuted, Cause: adapter.CredentialRejected{}}, true, false},
		{"asked to wait", adapter.Failure{Executed: adapter.NotExecuted, Cause: adapter.RateLimited{RetryAfter: time.Second}}, true, true},
		{"a 5xx may have run", adapter.Failure{Cause: adapter.UpstreamError{Status: 502}}, false, false},
	}
	for _, c := range cases {
		if got := NotExecuted(&c.f); got != c.notExecuted {
			t.Errorf("%s: NotExecuted %v", c.name, got)
		}
		if got := RateLimited(&c.f); got != c.rateLimited {
			t.Errorf("%s: RateLimited %v", c.name, got)
		}
	}
}
