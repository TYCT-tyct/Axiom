package adapter

import (
	"context"
	"net/http"

	"axiom/internal/catalog"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
)

// Family is the call formats an API speaks, as the admin API names it (ApiFormat): OpenAI's, Anthropic's or Gemini's.
type Family string

// The families.
const (
	FamilyOpenAI    Family = "openai"
	FamilyAnthropic Family = "anthropic"
	FamilyGemini    Family = "gemini"
)

// Direct is the management half of a provider's own API, or one that speaks the same formats, reached with keys a
// person pastes in (docs/domains/suppliers.md「直连接口」). There is no login, balance, offer or charge list to read:
// what can be asked is whether a key works and which models it lists. The sync never opens such an adapter, so its Open
// is never reached.
type Direct interface {
	// Family is the formats the provider speaks; empty for an API whose family a person gives.
	Family() Family
	// Origin is the provider's own address; empty for an API whose address a person gives.
	Origin() string
	// Currency is the currency of the provider's official price list; empty when a person says it.
	Currency() string
	// CheckKey asks the API, with one request the provider does not charge for, whether key works and which models it
	// lists. f is the family the API speaks.
	CheckKey(ctx context.Context, c Client, f Family, key secret.Secret) KeyCheck
	// Formats is what an endpoint of the catalog model m serves at an API of family f, from the model's own abilities;
	// none when the API cannot serve it.
	Formats(f Family, m catalog.Model) []protocol.Format
}

// KeyOutcome is what a check found of a key.
type KeyOutcome uint8

// The outcomes. Inconclusive is a check that says nothing about the key (a timeout, a rate limit, a 5xx, a 403 that may
// be a region block): the key is not kept, and can be sent again.
const (
	KeyInconclusive KeyOutcome = iota
	KeyPassed
	KeyFailed
)

// KeyCause is why a check did not pass, in the admin API's words (CredentialCheckCause).
type KeyCause string

// The causes a direct API's check reports.
const (
	CauseRejected      KeyCause = "credential_rejected"
	CauseNotPermitted  KeyCause = "not_permitted"
	CauseRateLimited   KeyCause = "rate_limited"
	CauseTimeout       KeyCause = "timeout"
	CauseTransport     KeyCause = "transport"
	CauseUpstreamError KeyCause = "upstream_error"
	CauseUnknown       KeyCause = "unknown"
)

// KeyCheck is the answer to one key's check. Status is the provider's HTTP status, 0 when it answered nothing. Models
// are the names the provider listed for the key, as a call must send them; only a passed check has them.
type KeyCheck struct {
	Outcome KeyOutcome
	Cause   KeyCause
	Status  int
	Models  []string
}

// Wire is the optional call half of a provider that does not take a key and paths the way every relay does (Bearer,
// at /v1/…): its own API. The module's shared call code asks it for both instead of its defaults.
type Wire interface {
	// Path is where a call in format f goes, given the path every relay serves it at.
	Path(f protocol.Format, path string) string
	// Authorize puts the key on a call in format f.
	Authorize(h http.Header, f protocol.Format, key string)
}
