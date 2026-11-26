// Package moonshot is Moonshot's own API, reached with pasted keys (docs/domains/suppliers.md「直连接口」). Its official
// prices are in yuan.
package moonshot

import (
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/direct"
)

// Kind is the adapter's name in suppliers.kind.
const Kind adapter.Kind = "moonshot"

// New is the adapter.
func New() *direct.Adapter {
	return direct.New(direct.Provider{
		Kind: Kind, Family: adapter.FamilyOpenAI, Origin: "https://api.moonshot.cn", Currency: "CNY",
	})
}
