package billing

import (
	"context"
	"fmt"
	"strings"
	"uuid"

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
	Estimated         bool         `json:"estimated"`
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

// ReadCharges reads the immutable settlement's sale inputs in the caller's account scope. Missing entries (including
// old writers without a pricing decision) are unavailable, never repriced using today's catalogue or fallbacks.
func ReadCharges(ctx context.Context, tx pg.Tx, ids []uuid.UUID) (map[uuid.UUID]ChargeDetails, error) {
	rows, err := queries.ReadCharges(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("billing: read itemized charges: %w", err)
	}
	out := make(map[uuid.UUID]ChargeDetails, len(rows))
	for _, r := range rows {
		d, err := chargeDetails(r)
		if err != nil {
			return nil, fmt.Errorf("billing: itemize %s: %w", r.RequestID, err)
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
		for i, seg := range segments {
			items := chargeItems(s, f, seg, r.Waived, catalog.ServiceTierSet(r.Tiers)) //nolint:gosec // stored CHECK bounds tiers
			for j := range items {
				items[j].Sampling, items[j].PriceVersion = i+1, r.PriceVersion
			}
			d.Items = append(d.Items, items...)
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
	for it := range catalog.NumItems {
		count := seg.Usage.Count(it)
		if count == 0 || waived && !perUseItems.Has(it) {
			continue
		}
		items = append(items, knownChargeItem(s, f, seg, tiers, it))
	}
	for _, u := range seg.Usage.Unknown() {
		rate, count, tokens := unknownRate(s, f, u)
		if count == 0 || waived && tokens {
			continue
		}
		items = append(items, unknownChargeItem(seg.Usage.ServiceTier(), rate, count, tokens))
	}
	return items
}

func knownChargeItem(s catalog.Schedule, f fallbacks, seg Segment, tiers catalog.ServiceTierSet, it catalog.Item) ChargeItem {
	rate, band, ok := s.Rate(seg.Usage, it)
	ok = ok && !undeclaredTier(tiers, seg.Usage.ServiceTier())
	if !ok {
		rate = tokenPrice(s)
		if perUseItems.Has(it) {
			rate = f.perUse[it]
		}
	}
	unit := "token"
	if perUseItems.Has(it) {
		unit = "use"
	}
	count := seg.Usage.Count(it)
	return ChargeItem{
		Item: it.String(), Quantity: count, Unit: unit, UnitPrice: rate.String(), Amount: chargeDecimal(rate.Mul(count)),
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
