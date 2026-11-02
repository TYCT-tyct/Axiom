package main

import (
	"testing"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/suppliers"
)

// TestPriceCostsSkipsByok: a private key's zero cost is not evidence for a shared sale price. Including it would make
// every published price fail its cost floor the moment any account added a key.
func TestPriceCostsSkipsByok(t *testing.T) {
	t.Parallel()
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	normal := suppliers.Routable{ID: uuid.NewV7(), Model: uuid.NewV7(), Currency: currency}
	own := suppliers.Routable{ID: uuid.NewV7(), Model: uuid.NewV7(), Currency: currency, Byok: true}
	costs := priceCosts([]suppliers.Routable{normal, own}, nil, currency)
	if len(costs) != 1 || costs[0].Endpoint != normal.ID {
		t.Fatalf("the costs are %+v, want only the platform endpoint %s", costs, normal.ID)
	}
}
