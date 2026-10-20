package payments

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// ErrNoMerchant is a top-up no merchant account takes: every candidate is over a limit or none offers the method.
// The order is refused instead of falling back to all candidates as sub2api does (load_balancer.go:105-110).
var ErrNoMerchant = errors.New("payments: no merchant account takes this amount; try another amount or method")

// Strategy picks among the merchant accounts that can take an order.
type Strategy string

// The strategies, after sub2api's (backend/internal/payment/load_balancer.go:273-293 at 8f2cbb5).
const (
	RoundRobin Strategy = "round_robin"
	LeastUsed  Strategy = "least_used"
)

// pricing turns credits into what each merchant charges: the site's currency, and the locked rate to each other
// currency the site's merchants are in. A currency without a rate has no entry, and its merchants are not offered.
type pricing struct {
	site  platform.Currency
	rates map[string]billing.Rate
}

// pricing reads the top-up rate from the site's currency to each of currencies in the platform scope, where the
// reference rates are. Rate versions never change, so the order that locks one need not read it in its own
// transaction.
func (p *Payments) pricing(ctx context.Context, site uuid.UUID, siteCurrency platform.Currency, currencies []string) (pricing, error) {
	pr := pricing{site: siteCurrency, rates: map[string]billing.Rate{}}
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) error {
		for _, c := range currencies {
			if _, ok := pr.rates[c]; ok || c == siteCurrency.String() {
				continue
			}
			r, ok, err := topUpRate(ctx, tx, site, siteCurrency, c)
			if err != nil {
				return err
			}
			if ok {
				pr.rates[c] = r
			}
		}
		return nil
	})
	return pr, err
}

// topUpRate is billing.TopUpRate to currency; false when the pair has no rate or is not a supported one.
func topUpRate(ctx context.Context, tx pg.Tx, site uuid.UUID, from platform.Currency, currency string) (billing.Rate, bool, error) {
	to, err := platform.ParseCurrency(currency)
	if err != nil {
		return billing.Rate{}, false, err
	}
	r, err := billing.TopUpRate(ctx, tx, site, from, to)
	var unsupported *billing.ExchangeError
	if errors.As(err, &unsupported) || errors.Is(err, billing.ErrNoRate) {
		return r, false, nil
	}
	return r, err == nil, err
}

// quote is what credits cost through o; false when o's currency has no rate.
func (pr pricing) quote(credits int64, o OffersRow) (Quote, billing.Rate, bool, error) {
	currency, err := platform.ParseCurrency(o.Currency)
	if err != nil {
		return Quote{}, billing.Rate{}, false, err
	}
	var rate billing.Rate
	var applied *big.Rat
	if currency != pr.site {
		var ok bool
		if rate, ok = pr.rates[o.Currency]; !ok {
			return Quote{}, rate, false, nil
		}
		applied = rate.Applied
	}
	q, err := quote(credits, applied, currency, Fee{PPM: o.FeePpm, Fixed: o.FeeFixed})
	return q, rate, err == nil, err
}

// candidate is a merchant method that can take an order, with what it would charge.
type candidate struct {
	offer OffersRow
	quote Quote
	rate  billing.Rate // zero in the site's own currency
	used  int64        // the merchant's usage today as read before locking
}

// fits reports whether total is within the method's single-payment limits and what is left of its daily limit.
func (c candidate) fits() bool {
	t := c.quote.Total.Minor
	o := c.offer
	return (o.SingleMin == nil || t >= *o.SingleMin) && (o.SingleMax == nil || t <= *o.SingleMax) &&
		(o.DailyLimit == nil || t <= *o.DailyLimit-c.used)
}

// candidates quotes offers and keeps those that can take credits given their merchants' usage today.
func candidates(pr pricing, credits int64, offers []OffersRow, usage []UsageTodayRow) ([]candidate, error) {
	all := make([]candidate, 0, len(offers))
	for _, o := range offers {
		q, rate, ok, err := pr.quote(credits, o)
		if errors.Is(err, ErrOutOfRange) || err == nil && !ok {
			continue
		}
		if err != nil {
			return nil, err
		}
		c := candidate{offer: o, quote: q, rate: rate}
		if j := slices.IndexFunc(usage, func(u UsageTodayRow) bool { return u.MerchantID == o.MerchantID }); j >= 0 {
			c.used = usage[j].Used
		}
		if c.fits() {
			all = append(all, c)
		}
	}
	return all, nil
}

// usageOf reads today's usage of the offers' merchants. A failed read refuses the order instead of counting as no
// usage (sub2api load_balancer.go:199-201 lets it through).
func usageOf(ctx context.Context, tx pg.Tx, offers []OffersRow) ([]UsageTodayRow, error) {
	ids := make([]uuid.UUID, len(offers))
	for i, o := range offers {
		ids[i] = o.MerchantID
	}
	usage, err := queries.UsageToday(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("payments: read merchant usage: %w", err)
	}
	return usage, nil
}

// choose picks the merchant account for an order of credits paid by method, in the order's account-scoped
// transaction, and adds the order's total to that merchant's usage today. The usage row is the serialization point
// of the daily limit: AddUsage locks it, re-checks the limit against what the orders before added and adds, so the
// last of the quota goes only to the orders it fits; a candidate that no longer fits gives way to the next. The
// transaction's lock and statement timeouts bound the wait (README.md「事务与任务」).
func (p *Payments) choose(ctx context.Context, tx pg.Tx, site uuid.UUID, pr pricing, strategy Strategy, method Method, credits int64) (candidate, error) {
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '2s'; SET LOCAL statement_timeout = '5s'"); err != nil {
		return candidate{}, fmt.Errorf("payments: bound the order's waits: %w", err)
	}
	m := string(method)
	offers, err := queries.Offers(ctx, tx, OffersParams{SiteID: site, Method: &m})
	if err != nil {
		return candidate{}, fmt.Errorf("payments: read merchant methods: %w", err)
	}
	usage, err := usageOf(ctx, tx, offers)
	if err != nil {
		return candidate{}, err
	}
	cs, err := candidates(pr, credits, offers, usage)
	if err != nil {
		return candidate{}, err
	}
	if len(cs) == 0 {
		p.logger.Warn("payments: no merchant account takes a top-up", "site", site, "method", m)
		return candidate{}, ErrNoMerchant
	}
	if strategy == LeastUsed {
		slices.SortStableFunc(cs, func(a, b candidate) int { return cmp.Compare(a.used, b.used) })
	} else {
		start := p.turn.Add(1) % uint64(len(cs))
		cs = slices.Concat(cs[start:], cs[:start])
	}
	for _, c := range cs {
		added, err := queries.AddUsage(ctx, tx, AddUsageParams{MerchantID: c.offer.MerchantID, SiteID: site, Amount: c.quote.Total.Minor, DailyLimit: c.offer.DailyLimit})
		if err != nil {
			return candidate{}, fmt.Errorf("payments: add merchant usage: %w", err)
		}
		if len(added) > 0 {
			return c, nil
		}
	}
	return candidate{}, ErrNoMerchant
}
