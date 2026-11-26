// Package compatible is any API that speaks OpenAI's or Anthropic's formats at an address a person gives, reached with
// pasted keys (docs/domains/suppliers.md「直连接口」). Its family, address and currency are the supplier's, not the
// adapter's.
package compatible

import (
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/direct"
)

// Kind is the adapter's name in suppliers.kind.
const Kind adapter.Kind = "compatible"

// New is the adapter.
func New() *direct.Adapter { return direct.New(direct.Provider{Kind: Kind}) }
