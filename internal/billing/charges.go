package billing

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"uuid"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// ChargeDetails explains the user's sale charge. Decimal amounts are in Currency, before any display rounding.
type ChargeDetails struct {
	Currency          string       `json:"currency"`
	Items             []ChargeItem `json:"items"`
	Subtotal          string       `json:"subtotal"`
	Rounding          string       `json:"rounding"`
	BalanceAdjustment string       `json:"balance_adjustment"`
	Charged           string       `json:"charged"`
	// CacheDiscount is what cache reads saved against the input price, empty when nothing was saved.
	CacheDiscount string `json:"cache_discount,omitzero"`
	Estimated     bool   `json:"estimated"`
}

// ChargeItem is one sampling's billed quantity at its effective sale price; it contains no supplier identity or cost.
type ChargeItem struct {
	Item         string `json:"item"`
	Quantity     int64  `json:"quantity"`
	Unit         string `json:"unit"`
	UnitPrice    string `json:"unit_price"`
	Amount       string `json:"amount"`
	Sampling     int    `json:"sampling"`
	ServiceTier  string `json:"service_tier"`
	ContextBand  int    `json:"context_band"`
	PriceVersion string `json:"price_version,omitempty"`
	Estimated    bool   `json:"estimated"`
	Fallback     bool   `json:"fallback"`
}

// ChargeReader keeps failures of optional historical pricing separate from availability of the call records.
type ChargeReader struct {
	logger *slog.Logger
	failed metric.Int64Counter
}

// NewChargeReader binds the node's diagnostics; nil meter disables metrics, as for the settlement counters.
func NewChargeReader(logger *slog.Logger, meter metric.Meter) *ChargeReader {
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("billing")
	}
	return &ChargeReader{
		logger: cmp.Or(logger, slog.Default()),
		failed: counter(meter, "axiom.billing.charge_details.unavailable", "Calls whose optional charge details could not be reconstructed."),
	}
}

// ReadCharges reads frozen sale inputs in the account scope. Old or incompatible rows omit their optional details;
// a reconstruction failure is logged and counted, while database query failures remain errors.
func (reader *ChargeReader) ReadCharges(ctx context.Context, tx pg.Tx, ids []uuid.UUID) (map[uuid.UUID]ChargeDetails, error) {
	rows, err := queries.ReadCharges(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("billing: read itemized charges: %w", err)
	}
	out := make(map[uuid.UUID]ChargeDetails, len(rows))
	for _, r := range rows {
		d, err := chargeDetails(r)
		if err != nil {
			reader.logger.ErrorContext(ctx, "billing: charge details unavailable", "request_id", r.RequestID.String(), "err", err)
			reader.failed.Add(ctx, 1)
			continue
		}
		out[r.RequestID] = d
	}
	return out, nil
}

func chargeDetails(r ReadChargesRow) (ChargeDetails, error) {
	s, err := catalog.ParseSchedule([]byte(r.Schedule))
	if err != nil {
		return ChargeDetails{}, err
	}
	f, err := parseFallbacks(r.Fallback)
	if err != nil {
		return ChargeDetails{}, err
	}
	segments, err := decodeUsage(r.FinalUsage)
	if err != nil {
		return ChargeDetails{}, err
	}
	d := ChargeDetails{Currency: s.Currency().String(), Items: []ChargeItem{}, Estimated: r.Estimated}
	if r.Tiers != -1 {
		tiers := catalog.ServiceTierSet(r.Tiers) //nolint:gosec // stored CHECK bounds tiers
		p, err := priceSegments(s, f, segments, r.Waived, tiers)
		if err != nil {
			return ChargeDetails{}, err
		}
		if p.cacheDiscount.Cmp(platform.Exact{}) > 0 {
			d.CacheDiscount = chargeDecimal(p.cacheDiscount)
		}
		for i, seg := range segments {
			items := chargeItems(s, f, seg, r.Waived, tiers)
			for j := range items {
				items[j].Sampling, items[j].PriceVersion = i+1, r.PriceVersion
			}
			d.Items = append(d.Items, items...)
		}
		if len(segments) > 0 {
			if rate, band, ok := requestRate(s, f, segments[len(segments)-1], tiers); ok {
				// One request per attempt, charged whether or not its model usage was waived, and reported by the
				// schedule rather than the upstream: the charge details name it once, with no sampling.
				last := segments[len(segments)-1]
				fallback := requestFallback(s, last, tiers)
				d.Items = append(d.Items, ChargeItem{
					Item: catalog.ItemRequest.String(), Quantity: 1, Unit: catalog.ItemRequest.Unit().String(),
					UnitPrice: rate.String(), Amount: chargeDecimal(rate.Mul(1)), ServiceTier: last.Usage.ServiceTier().String(),
					ContextBand: band, PriceVersion: r.PriceVersion, Estimated: fallback, Fallback: fallback,
				})
			}
		}
	}
	return reconcileCharges(d, r.Price, r.Charged)
}

func reconcileCharges(d ChargeDetails, price, charged int64) (ChargeDetails, error) {
	var total platform.Exact
	for _, item := range d.Items {
		amount, err := platform.ParseExact(item.Amount)
		if err != nil {
			return ChargeDetails{}, err
		}
		if total, err = total.Add(amount); err != nil {
			return ChargeDetails{}, err
		}
	}
	if ceilAmount(total) != price {
		return ChargeDetails{}, fmt.Errorf("item subtotal differs from the settled price")
	}
	rounded := platform.AmountExact(price)
	rounding, err := rounded.Add(total.Neg())
	if err != nil {
		return ChargeDetails{}, err
	}
	adjustment, err := platform.AmountExact(charged).Add(rounded.Neg())
	if err != nil {
		return ChargeDetails{}, err
	}
	d.Subtotal, d.Rounding = chargeDecimal(total), chargeDecimal(rounding)
	d.BalanceAdjustment, d.Charged = chargeDecimal(adjustment), chargeDecimal(platform.AmountExact(charged))
	return d, nil
}

func chargeItems(s catalog.Schedule, f fallbacks, seg Segment, waived bool, tiers catalog.ServiceTierSet) []ChargeItem {
	var items []ChargeItem
	charged := chargedItems(waived)
	for it := range catalog.NumItems {
		count := seg.Usage.Count(it)
		if count == 0 || !charged.Has(it) {
			continue
		}
		items = append(items, knownChargeItem(s, f, seg, tiers, it))
	}
	for _, u := range seg.Usage.Unknown() {
		rate, count, tokens := unknownRate(s, f, u)
		if count == 0 || !unknownCharged(tokens, charged) {
			continue
		}
		items = append(items, unknownChargeItem(seg.Usage.ServiceTier(), rate, count, tokens))
	}
	return items
}

// requestRate is the price of one generation request, or false when the schedule prices none. It is charged once per
// attempt, not per sampling, and is not usage the upstream reports.
func requestRate(s catalog.Schedule, f fallbacks, seg Segment, tiers catalog.ServiceTierSet) (platform.UnitPrice, int, bool) {
	if !s.Priced(1 << catalog.ItemRequest) {
		return platform.UnitPrice{}, 0, false
	}
	_, band, _ := s.Rate(seg.Usage, catalog.ItemRequest)
	return itemRate(s, f, seg, tiers, catalog.ItemRequest), band, true
}

// requestFallback reports whether the served tier has no request price and the charge uses the highest one instead.
func requestFallback(s catalog.Schedule, seg Segment, tiers catalog.ServiceTierSet) bool {
	_, _, ok := s.Rate(seg.Usage, catalog.ItemRequest)
	return !ok || undeclaredTier(tiers, seg.Usage.ServiceTier())
}

func knownChargeItem(s catalog.Schedule, f fallbacks, seg Segment, tiers catalog.ServiceTierSet, it catalog.Item) ChargeItem {
	_, band, ok := s.Rate(seg.Usage, it)
	ok = ok && !undeclaredTier(tiers, seg.Usage.ServiceTier())
	rate := itemRate(s, f, seg, tiers, it)
	count := seg.Usage.Count(it)
	return ChargeItem{
		Item: it.String(), Quantity: count, Unit: it.Unit().String(), UnitPrice: rate.String(), Amount: chargeDecimal(rate.Mul(count)),
		ServiceTier: seg.Usage.ServiceTier().String(), ContextBand: band, Estimated: !ok || !seg.Reported.Has(it), Fallback: !ok,
	}
}

func unknownChargeItem(tier catalog.ServiceTier, rate platform.UnitPrice, count int64, tokens bool) ChargeItem {
	name, unit := "unknown_uses", "use"
	if tokens {
		name, unit = "unknown_tokens", "token"
	}
	return ChargeItem{
		Item: name, Quantity: count, Unit: unit, UnitPrice: rate.String(), Amount: chargeDecimal(rate.Mul(count)),
		ServiceTier: tier.String(), Estimated: true, Fallback: true,
	}
}

func chargeDecimal(e platform.Exact) string {
	return strings.TrimRight(strings.TrimRight(e.Rat().FloatString(15), "0"), ".")
}

// ChargedCalls returns the current user charge of the named settled calls, after corrections, keyed by request. A
// request the scope cannot see, or one not settled, is absent and the caller keeps its recorded snapshot.
func ChargedCalls(ctx context.Context, tx pg.Tx, ids []uuid.UUID) (map[uuid.UUID]int64, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]int64{}, nil
	}
	rows, err := queries.ChargedCalls(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("billing: read current charges: %w", err)
	}
	out := make(map[uuid.UUID]int64, len(rows))
	for _, r := range rows {
		out[r.RequestID] = r.Charged
	}
	return out, nil
}
