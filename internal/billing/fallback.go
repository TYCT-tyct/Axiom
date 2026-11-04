package billing

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// fallbacks are a credit currency's prices for what a schedule cannot price (docs/domains/billing.md「价目表缺项」):
// each known per-use item's default unit price. Admission locks them on the reservation, as it locks the schedules.
//
// Rows also carried a markup on a supplier's actual cost, which priced a correction of the charge. M1 has no
// corrections (docs/domains/billing.md「用量来源与暂估」), so nothing applied it; migration 00040_fallbacks_no_markup.sql
// drops it from billing.fallbacks, and a reservation admitted before then still holds it, which parseLockedFallbacks
// ignores.
type fallbacks struct {
	currency platform.Currency
	perUse   map[catalog.Item]platform.UnitPrice
}

// fallbacksJSON is how billing.fallbacks stores them, canonical so the saved text is the row's: a currency and every
// known per-use item's price, and nothing else.
type fallbacksJSON struct {
	Currency string            `json:"currency"`
	PerUse   map[string]string `json:"per_use"`
}

// lockedFallbacksJSON is a reservation's locked fallbacks: the same, plus the markup rows admitted before #232 locked.
// It is read and ignored so those calls settle at the prices they were admitted at; billing.fallbacks and every save
// stay closed and refuse it.
type lockedFallbacksJSON struct {
	Currency string            `json:"currency"`
	PerUse   map[string]string `json:"per_use"`
	Markup   jsonx.Raw         `json:"markup"`
}

// maxPerUse is the bound a valid row keeps: a per-use price above one currency unit (catalog's sanity bound for
// per-use prices) is taken for a typing error, such as 12 for 1.2. A per-use price of 0 would make the item free.
var maxPerUse = mustUnitPrice("1")

func mustUnitPrice(s string) platform.UnitPrice {
	p, err := platform.ParseUnitPrice(s)
	if err != nil {
		panic(err)
	}
	return p
}

var errFallbacks = errors.New("billing: invalid fallback prices")

// parseFallbacks reads and checks a stored row: a currency, a price for every known per-use item and nothing else,
// each above 0 and at most maxPerUse.
func parseFallbacks(body string) (fallbacks, error) {
	var w fallbacksJSON
	if err := jsonx.UnmarshalKnown([]byte(body), &w); err != nil {
		return fallbacks{}, fmt.Errorf("%w: %w", errFallbacks, err)
	}
	return w.parse()
}

// parseLockedFallbacks reads the fallback text a reservation locked. A row admitted before #232 also locked a markup,
// which priced a correction billing no longer has; that one member is ignored so the call settles at the prices it
// locked. Any other member is still refused, as on the stored row.
func parseLockedFallbacks(body string) (fallbacks, error) {
	var w lockedFallbacksJSON
	if err := jsonx.UnmarshalKnown([]byte(body), &w); err != nil {
		return fallbacks{}, fmt.Errorf("%w: %w", errFallbacks, err)
	}
	return w.parse()
}

func (w fallbacksJSON) parse() (fallbacks, error) { return parseFallbacksBody(w.Currency, w.PerUse) }

func (w lockedFallbacksJSON) parse() (fallbacks, error) {
	return parseFallbacksBody(w.Currency, w.PerUse)
}

// parseFallbacksBody checks the members both stored forms share.
func parseFallbacksBody(currency string, perUse map[string]string) (f fallbacks, err error) {
	if f.currency, err = platform.ParseCurrency(currency); err != nil {
		return fallbacks{}, fmt.Errorf("%w: %w", errFallbacks, err)
	}
	f.perUse = map[catalog.Item]platform.UnitPrice{}
	for it := range catalog.NumItems {
		if !perUseItems.Has(it) {
			continue
		}
		text, ok := perUse[it.String()]
		p, err := platform.ParseUnitPrice(text)
		if !ok || err != nil || p.Cmp(platform.UnitPrice{}) == 0 || p.Cmp(maxPerUse) > 0 {
			return fallbacks{}, fmt.Errorf("%w: %s's default price %q is not a decimal above 0 and at most %s", errFallbacks, it, text, maxPerUse)
		}
		f.perUse[it] = p
	}
	if len(perUse) != len(f.perUse) {
		return fallbacks{}, fmt.Errorf("%w: only the known per-use items have default prices, not %v", errFallbacks, slices.Sorted(maps.Keys(perUse)))
	}
	return f, nil
}

func (f fallbacks) body() (string, error) {
	w := fallbacksJSON{Currency: f.currency.String(), PerUse: map[string]string{}}
	for it, p := range f.perUse {
		w.PerUse[it.String()] = p.String()
	}
	b, err := jsonx.MarshalCanonical(w)
	return string(b), err
}

// highest is the dearest per-use default, the price of a use of an item nothing prices.
func (f fallbacks) highest() (p platform.UnitPrice) {
	for _, q := range f.perUse {
		if q.Cmp(p) > 0 {
			p = q
		}
	}
	return p
}

// CheckFallbacks is the startup check (「价目表缺项」): every stored row valid and under its own currency, and the
// main site's, USD, present.
func CheckFallbacks(ctx context.Context, tx pg.Tx) error {
	rows, err := queries.AllFallbacks(ctx, tx)
	if err != nil {
		return fmt.Errorf("billing: read the fallback prices: %w", err)
	}
	usd := false
	for _, r := range rows {
		f, err := parseFallbacks(r.Body)
		if err != nil {
			return fmt.Errorf("%s: %w", r.Currency, err)
		}
		if f.currency.String() != r.Currency {
			return fmt.Errorf("%w: the %s row holds %s's prices", errFallbacks, r.Currency, f.currency)
		}
		usd = usd || r.Currency == "USD"
	}
	if !usd {
		return fmt.Errorf("%w: USD, the main site's currency, has none", errFallbacks)
	}
	return nil
}

// fallbackCurrencies is the currencies whose fallback prices the entries need, each once.
func fallbackCurrencies(q []Quotable) []string {
	currencies := make([]string, 0, 1)
	for _, e := range q {
		if c := e.Price.Currency().String(); !slices.Contains(currencies, c) {
			currencies = append(currencies, c)
		}
	}
	return currencies
}

func parseFallbackRows(rows []ReadFallbacksRow, found map[string]fallbacks, bodies map[string]string) error {
	for _, r := range rows {
		f, err := parseFallbacks(r.Body)
		if err != nil {
			return err
		}
		found[r.Currency], bodies[r.Currency] = f, r.Body
	}
	return nil
}

// fillFallbacks fills every entry's missing per-use prices from its currency's fallbacks and returns the bodies it
// locked beside the filled entries.
func fillFallbacks(q []Quotable, found map[string]fallbacks, bodies map[string]string) ([]Quotable, []string, error) {
	filled, locked := slices.Clone(q), make([]string, len(q))
	for i, e := range filled {
		c := e.Price.Currency().String()
		f, ok := found[c]
		if !ok {
			return nil, nil, fmt.Errorf("%w: no fallback prices in %s", ErrUnpriced, c)
		}
		filled[i].Price, locked[i] = e.Price.Fill(f.perUse), bodies[c]
	}
	return filled, locked, nil
}

// unknownCost is the fallback price of usage protocol could not map: like tokens, a whole count in a field the
// upstream names *tokens, at the model's highest token price at any service tier and band, which a waiver leaves
// out as model usage; anything else at the dearest per-use default for each use, a count rounded up, which a waiver
// keeps as an executed per-use item. A value that is no plain non-negative decimal, such as a service tier, is one use.
// A member of a cached-tokens intersection is neither: those tokens are already in the cached count, so it is recorded
// and costs nothing.
func unknownCost(s catalog.Schedule, f fallbacks, u catalog.UnknownUsage) (cost platform.Exact, tokens bool) {
	rate, count, tokens := unknownRate(s, f, u)
	return rate.Mul(count), tokens
}

func unknownRate(s catalog.Schedule, f fallbacks, u catalog.UnknownUsage) (platform.UnitPrice, int64, bool) {
	if strings.Contains(u.Field, intersectionPath) {
		return platform.UnitPrice{}, 0, true
	}
	n, err := strconv.ParseInt(u.Value, 10, 64)
	if err == nil && n >= 0 && n <= maxUnknownCount && strings.HasSuffix(u.Field, "tokens") {
		return tokenPrice(s), n, true
	}
	return f.highest(), uses(u.Value), false
}

// intersectionPath is where an OpenAI usage object breaks its cached tokens down by modality. Its members are subsets of
// cached_tokens, never usage of their own (protocol's openAICachedDetails).
const intersectionPath = ".cached_tokens_details."

// tokenPrice is one token at the highest price of any token item at any service tier and band: what a schedule that
// prices a token nowhere near the price of the tier a call was served at still charges, 0 if it prices no token at all.
func tokenPrice(s catalog.Schedule) platform.UnitPrice {
	price, _ := s.Highest(catalog.TokenItems, ^catalog.ServiceTierSet(0), math.MaxInt64)
	return price
}

// missingRate is the rate an item without a price is charged at. A per-use item with a site-wide default takes it. Any
// other item takes the dearest price the schedule has for the item itself over every tier and band, so a request is
// never charged another per-use item's price and a piece is never priced as a token; an item the schedule prices
// nowhere takes the dearest price of its unit, and a unit it prices nowhere the token price, as every unpriced item did
// before. The charge is estimated.
func missingRate(s catalog.Schedule, f fallbacks, it catalog.Item) platform.UnitPrice {
	if perUseItems.Has(it) {
		return f.perUse[it]
	}
	if it.Unit() == catalog.UnitUse {
		// A per-use item without a site default (a request or a file search) is never charged another tool's price: only
		// its own price is comparable, so without one it falls through to the token price like any unpriced item.
		if highest, priced := itemPrice(s, it); priced {
			return highest
		}
	} else if unit := it.Unit(); unit != catalog.UnitToken {
		// A counted item keeps the dearest price of its unit: the input and output images of a model are comparable, and
		// the item's own price in another service tier is not the cost of serving it in this one.
		if highest, priced := unitPrice(s, unit); priced {
			return highest
		}
	}
	return tokenPrice(s)
}

// itemPrice is the dearest price the schedule has for one item over every tier and band, and whether it prices it at
// all. Any price counts, including zero.
func itemPrice(s catalog.Schedule, it catalog.Item) (platform.UnitPrice, bool) {
	items := catalog.ItemSet(1) << it
	if !s.Priced(items) {
		return platform.UnitPrice{}, false
	}
	highest, _ := s.Highest(items, ^catalog.ServiceTierSet(0), math.MaxInt64)
	return highest, true
}

// unitPrice is the dearest price the schedule has for a unit, over every tier and band, and whether it prices the unit
// at all. Any price counts, including an explicit zero: a unit priced nowhere is the only reason to charge it as tokens,
// so existence is Priced's question, not Highest's missing, which only says every tier and band has a price.
func unitPrice(s catalog.Schedule, unit catalog.Unit) (platform.UnitPrice, bool) {
	items := itemsOfUnit(unit)
	if !s.Priced(items) {
		return platform.UnitPrice{}, false
	}
	highest, _ := s.Highest(items, ^catalog.ServiceTierSet(0), math.MaxInt64)
	return highest, true
}

// itemsOfUnit are the items billed in unit, for pricing one the schedule leaves out at the dearest price of its own
// unit.
func itemsOfUnit(unit catalog.Unit) (items catalog.ItemSet) {
	for it := range catalog.NumItems {
		if it.Unit() == unit {
			items.Add(it)
		}
	}
	return items
}

// uses is a count of uses from an unknown value: a plain decimal rounded up, at most maxUnknownCount, or else 1.
func uses(v string) int64 {
	whole, frac, point := strings.Cut(v, ".")
	if !isDigits(whole) || point && !isDigits(frac) {
		return 1
	}
	n, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || n >= maxUnknownCount { // only a range error is left: a run of digits too long for int64
		return maxUnknownCount
	}
	if strings.Trim(frac, "0") != "" {
		n++
	}
	return n
}

func isDigits(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }

// maxUnknownCount bounds a count of unknown usage as catalog bounds every known count.
const maxUnknownCount = 1e10

// SaveFallbacksRequest is an administrator's change to a currency's fallbacks: each known per-use item's default price,
// as decimal text.
type SaveFallbacksRequest struct {
	Currency string            `json:"currency"`
	PerUse   map[string]string `json:"per_use"`
	Reason   string            `json:"reason"`
	actor    audit.Actor
}

// SetActor records who asks; the command or the admin interface sets it, never the request's text.
func (r *SaveFallbacksRequest) SetActor(a audit.Actor) { r.actor = a }

// SaveFallbacks checks r as Start checks every row and writes it with its audit entry, in tx, which must be in the
// platform scope. A request that fails the check changes nothing. Reservations already admitted keep the fallbacks
// they locked.
func SaveFallbacks(ctx context.Context, tx pg.Tx, r SaveFallbacksRequest) error {
	f, err := fallbacksJSON{Currency: r.Currency, PerUse: r.PerUse}.parse()
	if err != nil {
		return err
	}
	if strings.TrimSpace(r.Reason) == "" {
		return errors.New("billing: a change to the fallback prices needs a reason")
	}
	body, err := f.body()
	if err != nil {
		return err
	}
	if err := queries.SaveFallbacks(ctx, tx, SaveFallbacksParams{Currency: r.Currency, Body: body}); err != nil {
		return fmt.Errorf("billing: save the fallback prices: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: r.actor, Action: "fallbacks.save", Target: audit.Target{Module: "billing", Kind: "fallbacks", ID: r.Currency},
		Reason: r.Reason, Detail: map[string]any{"fallbacks": body},
	})
}

// UnknownItem is a usage field a model's upstream reported that nothing prices, first seen on request at seen: the
// model takes no new requests until an administrator prices the field and clears it (routing's Endpoint.UnknownItem).
type UnknownItem struct {
	Model   catalog.ModelID `json:"model"`
	Field   string          `json:"field"`
	Request uuid.UUID       `json:"request"`
	Seen    time.Time       `json:"seen"`
}

// UnknownItems lists the fields not yet cleared, for the routing snapshot and the administrator; tx must be in the
// platform scope, which alone sees every tenant's.
func UnknownItems(ctx context.Context, tx pg.Tx) ([]UnknownItem, error) {
	rows, err := queries.UnknownItems(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("billing: list the unknown items: %w", err)
	}
	items := make([]UnknownItem, len(rows))
	for i, r := range rows {
		items[i] = UnknownItem{Model: catalog.ModelID(r.ModelID), Field: r.Field, Request: r.RequestID, Seen: r.SeenAt}
	}
	return items, nil
}

// ClearUnknownItemRequest is an administrator's word that a model's unknown field is priced now.
type ClearUnknownItemRequest struct {
	Model  uuid.UUID `json:"model"`
	Field  string    `json:"field"`
	Reason string    `json:"reason"`
	actor  audit.Actor
}

// SetActor records who asks.
func (r *ClearUnknownItemRequest) SetActor(a audit.Actor) { r.actor = a }

// ErrNotFound is a request for something that is not there, or no longer in the state the request needs.
var ErrNotFound = errors.New("billing: not found")

// ClearUnknownItem lets the model take requests again, with its audit entry, in tx in the platform scope. The field
// is not blocked again when it next appears: the administrator has priced it, so calls that still report it are
// charged at its fallback price.
func ClearUnknownItem(ctx context.Context, tx pg.Tx, r ClearUnknownItemRequest) error {
	if strings.TrimSpace(r.Reason) == "" {
		return errors.New("billing: clearing an unknown item needs a reason")
	}
	n, err := queries.ClearUnknownItem(ctx, tx, ClearUnknownItemParams{ModelID: r.Model, Field: r.Field})
	if err != nil {
		return fmt.Errorf("billing: clear the unknown item: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: no uncleared unknown item %s on model %s", ErrNotFound, r.Field, r.Model)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: r.actor, Action: "unknown_item.clear", Target: audit.Target{Module: "billing", Kind: "model", ID: r.Model.String()},
		Reason: r.Reason, Detail: map[string]any{"field": r.Field},
	})
}

// noteAttempts notes the unknown fields of every attempt that ran, not only the charged one: a model whose upstream
// reported one leaves routing however its attempt ended. An attempt known not to have run used nothing.
func (c *locked) noteAttempts(ctx context.Context, tx pg.Tx, o Outcome) error {
	for _, at := range o.Attempts {
		if at.Result == NotExecuted {
			continue
		}
		if err := c.noteUnknown(ctx, tx, at.Entry, at.Segments); err != nil {
			return err
		}
	}
	return nil
}

// noteUnknown records each field of segments protocol could not map against the entry's model, whichever attempt
// reported it. The first time the model shows a field it leaves routing (「价目表缺项」), and seen collects it for the
// alert once the transaction commits.
func (c *locked) noteUnknown(ctx context.Context, tx pg.Tx, entry int, segments []Segment) error {
	if entry < 0 || entry >= len(c.row.Models) || entry >= len(c.row.Fallbacks) || c.row.Fallbacks[entry] == "" || entry >= len(c.quotes) {
		return ErrNoAttempt
	}
	tiers := c.quotes[entry].tiers
	for _, seg := range segments {
		if undeclaredTier(tiers, seg.Usage.ServiceTier()) {
			if err := c.noteField(ctx, tx, entry, "service_tier."+seg.Usage.ServiceTier().String()); err != nil {
				return err
			}
		}
		for _, u := range seg.Usage.Unknown() {
			if err := c.noteField(ctx, tx, entry, u.Field); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *locked) noteField(ctx context.Context, tx pg.Tx, entry int, field string) error {
	model := c.row.Models[entry]
	n, err := queries.NoteUnknownItem(ctx, tx, NoteUnknownItemParams{
		ModelID: model, Field: field, RequestID: c.id, SiteID: c.row.SiteID, AccountID: c.row.AccountID,
	})
	if err != nil {
		return fmt.Errorf("billing: note the unknown item %s: %w", field, err)
	}
	if n == 1 {
		c.seen = append(c.seen, UnknownItem{Model: catalog.ModelID(model), Field: field, Request: c.id})
	}
	return nil
}

// Standard is always quotable; zero means the caller supplied no model tier declaration. Pricing and quarantine
// must apply the same interpretation or an unexpected tier could keep serving under a narrowed reservation.
func undeclaredTier(tiers catalog.ServiceTierSet, tier catalog.ServiceTier) bool {
	return tiers != 0 && !(tiers | 1<<catalog.ServiceTierStandard).Has(tier)
}
