package adapter

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"axiom/internal/catalog"
	"axiom/internal/platform"
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
// person pastes in (docs/domains/suppliers.md「直连接口」). There is no login, offer or charge list to read: what can be
// asked is whether a key works, which models it lists and, for a type that says so, its balance (Balancer). The sync
// never opens such an adapter, so its Open is never reached.
type Direct interface {
	// Type describes the provider's API: its price entries, the families it speaks and the fields a key comes with. A
	// type without price entries takes its address, family and currency from the person connecting it (compatible).
	Described
	// CheckKey asks the API, with one request the provider does not charge for, whether key works and which models it
	// lists. f is the family the API speaks; s are the key's settings.
	CheckKey(ctx context.Context, c Client, f Family, key secret.Secret, s Settings) KeyCheck
	// Formats is what an endpoint of the catalog model m, listed as name, serves at an API of family f, from the
	// model's own abilities; none when the API cannot serve it.
	Formats(f Family, m catalog.Model, name string) []protocol.Format
}

// Balancer is the half of a direct type whose description says Balance: a key's own balance, which is its own wallet
// (docs/domains/suppliers-direct.md「模型重读与余额」), read with a request the provider does not charge for. currency is
// the currency of the price entry the key is connected at, for an answer that names none or more than one.
type Balancer interface {
	ReadBalance(ctx context.Context, c Client, key secret.Secret, s Settings, currency platform.Currency) (Money, error)
}

// Aggregator is the half of a direct type that sells many authors' models and bills each call at whichever of its
// providers served it (docs/domains/suppliers-direct.md「OpenRouter 的成本」): what a name may cost at most, and what one
// generation did cost, both read with requests it does not charge for. Aggregates is false for every other direct type,
// which is never asked.
type Aggregator interface {
	Aggregates() bool
	// ReadCeiling is the highest price of each item across every provider of name and every one of their price tiers.
	ReadCeiling(ctx context.Context, c Client, key secret.Secret, s Settings, name string) Ceiling
	// ReadGeneration is the aggregator's own record of the generation id names, Found false while it has none.
	ReadGeneration(ctx context.Context, c Client, key secret.Secret, s Settings, id string) (Generation, error)
}

// CeilingOutcome is what a ceiling's read found.
type CeilingOutcome uint8

// The outcomes. CeilingInconclusive says nothing (a timeout, a 5xx): the ceiling read before stands.
const (
	CeilingInconclusive CeilingOutcome = iota
	CeilingRead
	CeilingNone          // the aggregator has no provider of the name
	CeilingUnexpressible // a provider prices an item the schedule cannot express
)

// Ceiling is the answer to a ceiling's read: the schedule, in the price entry's currency, when it is CeilingRead.
type Ceiling struct {
	Outcome  CeilingOutcome
	Schedule catalog.Schedule
}

// Generation is an aggregator's record of one generation: what it charged, in its credits, and whether it ran on a
// provider key of the account's, when that is only its fee.
type Generation struct {
	Found bool
	Cost  platform.Exact
	BYOK  bool
}

// Money is an amount in a currency, as a provider reports a balance.
type Money struct {
	Amount   platform.Exact
	Currency platform.Currency
}

// ParseMoney reads an amount in decimal text and its currency's code.
func ParseMoney(amount, currency string) (Money, error) {
	a, err := platform.ParseExact(amount)
	if err != nil {
		return Money{}, fmt.Errorf("the amount: %w", err)
	}
	c, err := platform.ParseCurrency(currency)
	if err != nil {
		return Money{}, fmt.Errorf("the currency: %w", err)
	}
	return Money{Amount: a, Currency: c}, nil
}

// FamilyOf is the family an API of the type speaks: its own, or for a type whose family a person chooses, chosen,
// which must be one of the choices. False when chosen is given for a type with its own, or is not a choice.
func FamilyOf(t Type, chosen Family) (Family, bool) {
	if len(t.Entries) > 0 {
		return t.Families[0], chosen == ""
	}
	return chosen, chosen != "" && slices.Contains(t.Families, chosen)
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
	// CauseRegionUnavailable is a cloud region that does not serve the account's models.
	CauseRegionUnavailable KeyCause = "region_unavailable"
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
	// Authorize puts the key, and the settings the type sends with it, on a call in format f.
	Authorize(h http.Header, f protocol.Format, key string, s Settings)
}
