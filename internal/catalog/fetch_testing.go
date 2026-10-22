package catalog

import (
	"testing"

	"axiom/internal/platform/egress"
)

// NewTestFetcher lets node integration tests use local datasets without exposing a production source override.
func NewTestFetcher(t testing.TB, client *egress.Client, urls map[PriceSource]string) *Fetcher {
	if !testing.Testing() {
		panic("catalog: dataset overrides require a test binary")
	}
	t.Helper()
	f := NewFetcher(client)
	f.urls = urls
	return f
}
