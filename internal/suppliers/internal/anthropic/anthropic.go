// Package anthropic is Anthropic's own API, reached with pasted keys (docs/domains/suppliers.md「直连接口」).
package anthropic

import (
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/direct"
)

// Kind is the adapter's name in suppliers.kind.
const Kind adapter.Kind = "anthropic"

// New is the adapter.
func New() *direct.Adapter {
	return direct.New(direct.Provider{
		Kind: Kind, Family: adapter.FamilyAnthropic, Origin: "https://api.anthropic.com", Currency: "USD",
	})
}
