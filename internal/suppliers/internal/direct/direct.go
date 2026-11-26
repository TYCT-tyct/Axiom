// Package direct is what every provider's own API shares (docs/domains/suppliers-direct.md「直连接口」): one is reached
// with keys a person pastes in, at an address and in call formats that are the provider's, and has no login, offer or
// charge list to read. The providers are described by a Provider each: a row of the vendors table, or a package of its
// own (anthropic, compatible) listed in the registry, so that removing one is deleting its row or its package and its
// line; this package is not an adapter.
package direct

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// Provider is what is particular to one provider's API.
type Provider struct {
	// Type describes it; New fills in the mode, and the default key form when it names none.
	Type adapter.Type
	// Root is the path its OpenAI formats are served under in place of /v1, as Google serves them under
	// /v1beta/openai; empty is /v1.
	Root string
	// Anthropic is its Anthropic-compatible API under the same origin, for a provider of OpenAI's family that has one;
	// nil for none.
	Anthropic *Anthropic
	// Responses says it serves OpenAI's Responses and legacy completions besides Chat. An API that only claims the
	// OpenAI family is sent Chat, which every one of them serves, and the gateway converts.
	Responses bool
	// Models is the path its model list is read at; empty is the root's /models.
	Models string
}

// Anthropic is a provider's Anthropic-compatible API: Messages at Root + /v1/messages, for the models it names. A name
// is matched whole; "*" is every model, only for a provider that says its Anthropic API serves them all.
type Anthropic struct {
	Root   string
	Models []string
}

// serves says the Anthropic-compatible API serves the model listed as name.
func (a *Anthropic) serves(name string) bool {
	return a != nil && (slices.Contains(a.Models, "*") || slices.Contains(a.Models, name))
}

// Keys is the form of a provider whose credential is its keys, pasted one a line.
var Keys = adapter.Form{Name: "api_key", Fields: []adapter.Field{{Name: "keys", Required: true}}}

// Adapter is one provider's adapter. It holds nothing: the key comes from the credential the call picked.
type Adapter struct {
	p Provider
}

var (
	_ adapter.Adapter = (*Adapter)(nil)
	_ adapter.Direct  = (*Adapter)(nil)
)

// New is the adapter of the provider.
func New(p Provider) *Adapter {
	p.Type.Mode = adapter.ModeDirect
	if len(p.Type.Forms) == 0 {
		p.Type.Forms = []adapter.Form{Keys}
	}
	if p.Root == "" {
		p.Root = "/v1"
	}
	if p.Models == "" {
		p.Models = p.Root + "/models"
	}
	return &Adapter{p: p}
}

// Kind implements adapter.Adapter.
func (a *Adapter) Kind() adapter.Kind { return a.p.Type.Kind }

// Type implements adapter.Described.
func (a *Adapter) Type() adapter.Type { return a.p.Type }

// Buckets implements adapter.Adapter. A key is checked once when it is pasted, and the provider limits each key by its
// own account, which the credential's concurrency and per-minute ceiling stand for: there is no window of the provider's
// that Axiom's requests share.
func (*Adapter) Buckets() []adapter.BucketSpec { return nil }

// Detect implements adapter.Adapter. A provider's API is chosen by name, never found by probing an address.
func (*Adapter) Detect(context.Context, adapter.Client) (adapter.Detection, error) {
	return adapter.Detection{}, adapter.ErrNotRecognised
}

// errNoManagement is Open's error: there is no management side to log in to, and the sync never asks for one.
var errNoManagement = errors.New("direct: a provider's own API has no management side to open")

// Open implements adapter.Adapter.
func (*Adapter) Open(context.Context, adapter.Client, adapter.LoginStore) (adapter.Manager, error) {
	return nil, errNoManagement
}

// Formats implements adapter.Direct, from the model's own billable items as a provider key's endpoint is
// (docs/domains/accounts.md「自带的供应商 key」): output means it generates, input alone means it embeds. Nothing is guessed
// from the name; only whether the Anthropic-compatible API serves the model is read from the name it is listed by.
func (a *Adapter) Formats(f adapter.Family, m catalog.Model, name string) []protocol.Format {
	_, generates := m.Serving()
	generates = generates && m.Reportable().Items.Has(catalog.ItemOutput)
	switch f {
	case adapter.FamilyAnthropic:
		if generates {
			return []protocol.Format{protocol.Messages}
		}
	case adapter.FamilyOpenAI:
		formats := a.openAIFormats(m, generates)
		if generates && a.p.Anthropic.serves(name) {
			formats = append(formats, protocol.Messages)
		}
		return formats
	case adapter.FamilyGemini:
	}
	return nil
}

// openAIFormats is Formats for an API of OpenAI's family.
func (a *Adapter) openAIFormats(m catalog.Model, generates bool) []protocol.Format {
	items := m.Reportable().Items
	_, direct := m.ServingDirect()
	switch {
	case generates && a.p.Responses:
		return []protocol.Format{protocol.Chat, protocol.Responses}
	case generates:
		return []protocol.Format{protocol.Chat}
	case items.Has(catalog.ItemOutput) && direct && a.p.Responses:
		return []protocol.Format{protocol.Completions}
	case !items.Has(catalog.ItemOutput) && items.Has(catalog.ItemInput) && direct:
		return []protocol.Format{protocol.Embeddings}
	}
	return nil
}

// Calls implements adapter.Adapter.
func (a *Adapter) Calls() adapter.CallQuirks { return quirks{a.p} }

type quirks struct{ p Provider }

var _ adapter.Wire = quirks{}

// Policy implements adapter.CallQuirks. A provider streams and stops generating when the client disconnects.
func (quirks) Policy() adapter.CallPolicy { return adapter.CallPolicy{Streams: true, Cancels: true} }

// Formats implements adapter.CallQuirks. A direct API's endpoints are written from their models' abilities when the
// keys are pasted (adapter.Direct), not inferred from a group's listing.
func (quirks) Formats(adapter.GroupFacts) []protocol.Format { return nil }

// ChargeRef implements adapter.CallQuirks. A provider's charges are never read, so there is no record to match a
// request id with, and an attempt that kept one would wait for a charge that never comes and be reported unmatched.
func (quirks) ChargeRef(http.Header) (adapter.ChargeRef, bool) { return "", false }

// Path implements adapter.Wire: Messages under the Anthropic-compatible root of a provider that has one, as they are for
// one of Anthropic's family, and the OpenAI formats under the provider's root.
func (q quirks) Path(f protocol.Format, path string) string {
	if f == protocol.Messages {
		if q.p.Anthropic != nil {
			return q.p.Anthropic.Root + path
		}
		return path
	}
	return q.p.Root + path[len("/v1"):]
}

// Authorize implements adapter.Wire. Anthropic's Messages take the key in x-api-key, as every API that speaks them
// accepts it; the OpenAI formats take it as a bearer token. A setting the type sends in a header goes with every call.
func (q quirks) Authorize(h http.Header, f protocol.Format, key string, s adapter.Settings) {
	if f == protocol.Messages {
		h.Set("X-Api-Key", key)
	} else {
		h.Set("Authorization", "Bearer "+key)
	}
	for _, field := range q.p.Type.Fields {
		if v := s[field.Name]; field.Header != "" && v != "" {
			h.Set(field.Header, v)
		}
	}
}
