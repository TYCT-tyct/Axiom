package gateway

import (
	"testing"

	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// TestRoutingScopeMatchesByName: guards against two cases of routingScope written the wrong way round; a scope left out
// is stopped by the exhaustive lint and the panic. Each scope an adapter classifies a failure with becomes routing's scope of the same
// name. The two types are numbered in their own order, so a match by number would send a failure the wrong way.
func TestRoutingScopeMatchesByName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		in   suppliers.Scope
		want routing.Scope
	}{
		{"request", suppliers.ScopeRequest, routing.ScopeRequest},
		{"egress", suppliers.ScopeEgress, routing.ScopeEgress},
		{"endpoint", suppliers.ScopeEndpoint, routing.ScopeEndpoint},
		{"credential", suppliers.ScopeCredential, routing.ScopeCredential},
		{"group", suppliers.ScopeGroup, routing.ScopeGroup},
		{"supplier", suppliers.ScopeSupplier, routing.ScopeSupplier},
	} {
		if got := routingScope(c.in); got != c.want {
			t.Errorf("%s: %v, want routing's %v", c.name, got, c.want)
		}
	}
}
