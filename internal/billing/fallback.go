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

	"go.opentelemetry.io/otel/metric"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// fallbacks are a credit currency's prices for what a schedule cannot price (docs/domains/billing.md「价目表缺项」):
// each known per-use item's default unit price. Admission locks them on the reservation, as it locks the schedules.
//
// The stored row also carries a markup on a supplier's actual cost, which priced a correction of the charge. M1 has no
// corrections (docs/domains/billing.md「用量来源与暂估」), so nothing applies it, and it stays in the row until a
// migration removes it from the rows written before: a row with a member this type does not know is refused.
type fallbacks struct {
	currency platform.Currency
	perUse   map[catalog.Item]platform.UnitPrice
	markup   platform.UnitPrice
}

// fallbacksJSON is how billing.fallbacks and the reservations store them, canonical so the locked text is the row's.
type fallbacksJSON struct {
	Currency string            `json:"currency"`
	Markup   string            `json:"markup"`
	PerUse   map[string]string `json:"per_use"`
}

// The bounds a valid row keeps. A markup below 1 sells below the supplier's cost, which no setting may do
// (「要求」); above 10, or a per-use price above one currency unit (catalog's sanity bound for per-use prices), is
// taken for a typing error, such as 12 for 1.2. A per-use price of 0 would make the item free.
var (
	minMarkup = mustUnitPrice("1")
	maxMarkup = mustUnitPrice("10")
	maxPerUse = mustUnitPrice("1")
)

func mustUnitPrice(s string) platform.UnitPrice {
	p, err := platform.ParseUnitPrice(s)
	if err != nil {
		panic(err)
	}
	return p
}

var errFallbacks = errors.New("billing: invalid fallback prices")

// parseFallbacks reads and checks a stored row: a currency, a price for every known per-use item and nothing else,
// each above 0 and at most maxPerUse, and a markup from minMarkup to maxMarkup.
func parseFallbacks(body string) (fallbacks, error) {
	var w fallbacksJSON
	if err := jsonx.UnmarshalKnown([]byte(body), &w); err != nil {
		return fallbacks{}, fmt.Errorf("%w: %w", errFallbacks, err)
	}
	return w.parse()
}

func (w fallbacksJSON) parse() (f fallbacks, err error) {
	if f.currency, err = platform.ParseCurrency(w.Currency); err != nil {
		return fallbacks{}, fmt.Errorf("%w: %w", errFallbacks, err)
	}
	f.markup, err = platform.ParseUnitPrice(w.Markup)
	if err != nil || f.markup.Cmp(minMarkup) < 0 || f.markup.Cmp(maxMarkup) > 0 {
		return fallbacks{}, fmt.Errorf("%w: the markup %q is not a decimal from %s to %s", errFallbacks, w.Markup, minMarkup, maxMarkup)
	}
	f.perUse = map[catalog.Item]platform.UnitPrice{}
	for it := range catalog.NumItems {
		if !perUseItems.Has(it) {
			continue
		}
		text, ok := w.PerUse[it.String()]
		p, err := platform.ParseUnitPrice(text)
		if !ok || err != nil || p.Cmp(platform.UnitPrice{}) == 0 || p.Cmp(maxPerUse) > 0 {
			return fallbacks{}, fmt.Errorf("%w: %s's default price %q is not a decimal above 0 and at most %s", errFallbacks, it, text, maxPerUse)
		}
		f.perUse[it] = p
	}
	if len(w.PerUse) != len(f.perUse) {
		return fallbacks{}, fmt.Errorf("%w: only the known per-use items have default prices, not %v", errFallbacks, slices.Sorted(maps.Keys(w.PerUse)))
	}
	return f, nil
}

func (f fallbacks) body() (string, error) {
	w := fallbacksJSON{Currency: f.currency.String(), Markup: f.markup.String(), PerUse: map[string]string{}}
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

// withFallbacks gives every entry its currency's fallbacks: its schedule filled with the per-use defaults, which the
// quote and every later charge price with, and the row's text to lock. A currency without a row has nothing to price
// an unknown item with, so its entries are unpriced, as a missing price is: the request is not quoted.
func withFallbacks(ctx context.Context, tx pg.Tx, q []Quotable) ([]Quotable, []string, error) {
	currencies := make([]string, 0, 1)
	for _, e := range q {
		if c := e.Price.Currency().String(); !slices.Contains(currencies, c) {
			currencies = append(currencies, c)
		}
	}
	rows, err := queries.ReadFallbacks(ctx, tx, currencies)
	if err != nil {
		return nil, nil, fmt.Errorf("billing: read the fallback prices: %w", err)
	}
	found := map[string]fallbacks{}
	bodies := map[string]string{}
	for _, r := range rows {
		if found[r.Currency], err = parseFallbacks(r.Body); err != nil {
			return nil, nil, err
		}
		bodies[r.Currency] = r.Body
	}
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
func unknownCost(s catalog.Schedule, f fallbacks, u catalog.UnknownUsage) (cost platform.Exact, tokens bool) {
	n, err := strconv.ParseInt(u.Value, 10, 64)
	if err == nil && n >= 0 && n <= maxUnknownCount && strings.HasSuffix(u.Field, "tokens") {
		price, _ := s.Highest(tokenItems, ^catalog.ServiceTierSet(0), math.MaxInt64)
		return price.Mul(n), true
	}
	return f.highest().Mul(uses(u.Value)), false
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

// SaveFallbacksRequest is an administrator's change to a currency's fallbacks: each known per-use item's default
// price and the markup, as decimal text.
type SaveFallbacksRequest struct {
	Currency string            `json:"currency"`
	PerUse   map[string]string `json:"per_use"`
	Markup   string            `json:"markup"`
	Reason   string            `json:"reason"`
	actor    audit.Actor
}

// SetActor records who asks; the command or the admin interface sets it, never the request's text.
func (r *SaveFallbacksRequest) SetActor(a audit.Actor) { r.actor = a }

// SaveFallbacks checks r as Start checks every row and writes it with its audit entry, in tx, which must be in the
// platform scope. A request that fails the check changes nothing. Reservations already admitted keep the fallbacks
// they locked.
func SaveFallbacks(ctx context.Context, tx pg.Tx, r SaveFallbacksRequest) error {
	f, err := fallbacksJSON{Currency: r.Currency, Markup: r.Markup, PerUse: r.PerUse}.parse()
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

// FallbackHold is an estimate that holds part of its reservation and rests on usage only a fallback price covers.
type FallbackHold struct {
	Request, Site, Account uuid.UUID
	Held                   int64 // billionths of a credit
	By                     time.Time
}

// FallbackHoldsPage is the most holds FallbackHolds returns at once (docs/performance.md「后台与数据」: every list
// pages by cursor, with a bound on the page).
const FallbackHoldsPage = 500

// FallbackHolds lists them by window, oldest first, a page of at most limit (1 to FallbackHoldsPage) after the hold
// after, or from the first when after is nil; a page shorter than limit is the last. tx must be in the platform scope
// to see every tenant's.
func FallbackHolds(ctx context.Context, tx pg.Tx, after *FallbackHold, limit int) ([]FallbackHold, error) {
	params := FallbackHoldsParams{PageSize: int32(min(max(limit, 1), FallbackHoldsPage))}
	if after != nil {
		params.AfterBy, params.AfterID = after.By, after.Request
	}
	rows, err := queries.FallbackHolds(ctx, tx, params)
	if err != nil {
		return nil, fmt.Errorf("billing: list the fallback holds: %w", err)
	}
	holds := make([]FallbackHold, len(rows))
	for i, r := range rows {
		holds[i] = FallbackHold{Request: r.RequestID, Site: r.SiteID, Account: r.AccountID, Held: r.Held, By: r.CorrectBy}
	}
	return holds, nil
}

// RegisterMetrics reports the fallback holds on m as two gauges, their number and what they hold in billionths, read
// from db in the platform scope at each collection. cmd/axiom calls it once per process.
func RegisterMetrics(m metric.Meter, db *pg.DB, scope pg.Scope) error {
	count, err := m.Int64ObservableGauge("axiom.billing.fallback_holds",
		metric.WithDescription("Estimates holding part of their reservation on usage only a fallback price covers."))
	if err != nil {
		return err
	}
	held, err := m.Int64ObservableGauge("axiom.billing.fallback_holds.held",
		metric.WithDescription("What those estimates hold, in billionths of a credit."))
	if err != nil {
		return err
	}
	_, err = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		var totals FallbackHoldTotalsRow
		if err := db.ReadTx(ctx, scope, func(tx pg.Tx) (err error) {
			totals, err = queries.FallbackHoldTotals(ctx, tx)
			return err
		}); err != nil {
			return fmt.Errorf("billing: count the fallback holds: %w", err)
		}
		o.ObserveInt64(count, totals.Holds)
		o.ObserveInt64(held, totals.Held)
		return nil
	}, count, held)
	return err
}
