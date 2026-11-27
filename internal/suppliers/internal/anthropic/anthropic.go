// Package anthropic is Anthropic's own API, reached with pasted keys (docs/domains/suppliers-direct.md「直连接口」): its
// own Messages protocol, so a package of its own rather than a row of the vendors table.
package anthropic

import (
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/direct"
)

// New is the adapter.
func New() *direct.Adapter {
	return direct.New(direct.Provider{Type: adapter.Type{
		Kind: "anthropic", Name: "Anthropic", Vendor: "anthropic", Serves: []string{"anthropic"}, Group: adapter.GroupOfficial,
		Entries:  []adapter.PriceEntry{{ID: "global", Origin: "https://api.anthropic.com", Currency: "USD"}},
		Families: []adapter.Family{adapter.FamilyAnthropic}, Lists: true,
	}})
}
