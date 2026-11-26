// Package google is Google's Gemini API, reached with pasted keys (docs/domains/suppliers.md「直连接口」). It is called in
// the OpenAI formats Google serves under /v1beta/openai: the gateway does not speak Gemini's own generateContent.
package google

import (
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/direct"
)

// Kind is the adapter's name in suppliers.kind.
const Kind adapter.Kind = "google"

// New is the adapter.
func New() *direct.Adapter {
	return direct.New(direct.Provider{
		Kind: Kind, Family: adapter.FamilyOpenAI, Origin: "https://generativelanguage.googleapis.com", Currency: "USD",
		Root: "/v1beta/openai",
	})
}
