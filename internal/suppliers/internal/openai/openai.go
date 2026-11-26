// Package openai is OpenAI's own API: a platform supplier reached with pasted keys, and an account's own key (BYOK),
// which is the same call side (docs/domains/suppliers.md「直连接口」, docs/domains/accounts.md「自带的供应商 key」).
package openai

import (
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/direct"
)

// Kind is the adapter's name in suppliers.kind.
const Kind adapter.Kind = "openai"

// New is the adapter. OpenAI's API alone serves Responses and legacy completions besides Chat.
func New() *direct.Adapter {
	return direct.New(direct.Provider{
		Kind: Kind, Family: adapter.FamilyOpenAI, Origin: "https://api.openai.com", Currency: "USD", Responses: true,
	})
}
