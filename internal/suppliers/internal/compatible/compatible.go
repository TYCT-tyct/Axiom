// Package compatible is any API that speaks OpenAI's or Anthropic's formats at an address a person gives, reached with
// pasted keys (docs/domains/suppliers-direct.md「直连接口」). Its family, address and currency are the supplier's, not
// the adapter's: it has no price entries, and the family is one of its choices.
package compatible

import (
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/direct"
)

// New is the adapter.
func New() *direct.Adapter {
	return direct.New(direct.Provider{Type: adapter.Type{
		Kind: "compatible", Name: "OpenAI / Anthropic compatible", Group: adapter.GroupOfficial,
		Families: []adapter.Family{adapter.FamilyOpenAI, adapter.FamilyAnthropic}, Lists: true,
	}})
}
