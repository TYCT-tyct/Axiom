package payments

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sync/atomic"
	"uuid"

	"axiom/internal/accounts"
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

// menu is what quoting and choosing need of a site's merchant accounts, read before the order's transaction: the
// site's settings, the offers of merchants whose configuration opens, their usage today as read, and the rates.
type menu struct {
	site     accounts.Site
	settings settings
	offers   []OffersRow // ordered by method, then the merchants' sort
	usage    []UsageTodayRow
	pricing  pricing
}

// menu reads the site's offers of method (nil for all) in scope s, which sees the site but not its merchants'
// configurations; then, in the platform scope, it drops the merchants whose configuration does not open (usable)
// and reads the rates.
func (p *Payments) menu(ctx context.Context, s pg.Scope, site accounts.Site, method *string) (menu, error) {
	m := menu{site: site}
	id := uuid.UUID(site.ID)
	err := p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) {
		if m.settings, err = readSettings(ctx, tx, id); err != nil || !m.settings.open {
			return err
		}
		if m.offers, err = queries.Offers(ctx, tx, OffersParams{SiteID: id, Method: method}); err != nil {
			return err
		}
		m.usage, err = usageOf(ctx, tx, m.offers)
		return err
	})
	if err != nil || !m.settings.open {
		return m, err
	}
	err = p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) (err error) {
		if m.offers, err = p.usable(ctx, tx, m.offers); err != nil {
			return err
		}
		m.pricing, err = readPricing(ctx, tx, id, site.Currency, m.offers)
		return err
	})
	return m, err
}

// pricing turns credits into what each merchant charges: the site's currency, and the locked rate to each other
// currency the site's merchants are in. A currency without a rate has no entry, and its merchants are not offered.
type pricing struct {
	site  platform.Currency
	rates map[string]billing.Rate
}

// readPricing reads the top-up rate from the site's currency to each currency of offers, in the platform scope where
// the reference rates are. Rate versions never change, so the order that locks one need not read it in its own
// transaction.
func readPricing(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, offers []OffersRow) (pricing, error) {
	pr := pricing{site: currency, rates: map[string]billing.Rate{}}
	for _, o := range offers {
		if _, ok := pr.rates[o.Currency]; ok || o.Currency == currency.String() {
			continue
		}
		r, ok, err := topUpRate(ctx, tx, site, currency, o.Currency)
		if err != nil {
			return pr, err
		}
		if ok {
			pr.rates[o.Currency] = r
		}
	}
	return pr, nil
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

// inSite is an amount of a merchant's currency in the site's, at the reference rate without the buffer, exactly: what
// amounts in different currencies are compared by.
func (pr pricing) inSite(m Money) *big.Rat {
	r := new(big.Rat).SetFrac(big.NewInt(m.Minor), minorScale(m.Currency))
	if m.Currency != pr.site {
		reference, _ := pr.rates[m.Currency.String()].Reference.Between(pr.site, m.Currency) // quoted, so it has a rate
		r.Quo(r, reference)
	}
	return r
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
func (pr pricing) candidates(credits int64, offers []OffersRow, usage []UsageTodayRow) ([]candidate, error) {
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

// turnKey is one round-robin rotation: each site's method has its own, so the orders of one never shift another's.
type turnKey struct {
	site   accounts.SiteID
	method string
}

// ranked is the selection rule Options and choose share: the candidates that can take credits through method, in the
// order the site's strategy picks them. Options shows the first without moving the rotation; choose moves it and tries
// them in turn, so the order goes to the merchant the page showed unless another order came between.
//
// Round robin takes position n of the candidates that can take the amount now, n counting this site's orders of this
// method, so a candidate that drops out shifts the rest by one. Least used compares today's usage in the site's
// currency, since the merchants may be in different ones.
func (p *Payments) ranked(m menu, method string, offers []OffersRow, usage []UsageTodayRow, credits int64, advance bool) ([]candidate, error) {
	if err := m.settings.admits(credits); err != nil {
		return nil, err
	}
	cs, err := m.pricing.candidates(credits, offers, usage)
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	if m.settings.strategy == LeastUsed {
		slices.SortStableFunc(cs, func(a, b candidate) int {
			return m.pricing.inSite(Money{a.quote.Total.Currency, a.used}).Cmp(m.pricing.inSite(Money{b.quote.Total.Currency, b.used}))
		})
		return cs, nil
	}
	v, _ := p.turns.LoadOrStore(turnKey{m.site.ID, method}, new(atomic.Uint64))
	turn := v.(*atomic.Uint64) //nolint:forcetypeassert // the map holds only these
	n := turn.Load() + 1
	if advance {
		n = turn.Add(1)
	}
	start := n % uint64(len(cs))
	return slices.Concat(cs[start:], cs[:start]), nil
}

// choose picks the merchant account for an order of credits paid by method, in the order's account-scoped
// transaction, and adds the order's total to that merchant's usage today. The usage row is the serialization point
// of the daily limit: AddUsage locks it, re-checks the limit against what the orders before added and adds, so the
// last of the quota goes only to the orders it fits; a candidate that no longer fits gives way to the next. The
// transaction's lock and statement timeouts bound the wait (README.md「事务与任务」).
func (p *Payments) choose(ctx context.Context, tx pg.Tx, m menu, method Method, credits int64) (candidate, error) {
	if err := boundOrderTx(ctx, tx); err != nil {
		return candidate{}, err
	}
	offers := slices.DeleteFunc(slices.Clone(m.offers), func(o OffersRow) bool { return o.Method != string(method) })
	usage, err := usageOf(ctx, tx, offers)
	if err != nil {
		return candidate{}, err
	}
	cs, err := p.ranked(m, string(method), offers, usage, credits, true)
	if err != nil {
		return candidate{}, err
	}
	for _, c := range cs {
		if err := holdOffer(ctx, tx, c.offer); err != nil {
			return candidate{}, err
		}
		added, err := queries.AddUsage(ctx, tx, AddUsageParams{MerchantID: c.offer.MerchantID, SiteID: uuid.UUID(m.site.ID), Amount: c.quote.Total.Minor, DailyLimit: c.offer.DailyLimit})
		if err != nil {
			return candidate{}, fmt.Errorf("payments: add merchant usage: %w", err)
		}
		if len(added) > 0 {
			return c, nil
		}
	}
	p.logger.Warn("payments: no merchant account takes a top-up", "site", m.site.ID.TypeID(), "method", string(method))
	return candidate{}, ErrNoMerchant
}

// holdOffer shares the identity lock until the order commits. Saves take it exclusively before checking open orders;
// rereading the revision here rejects a menu built before a concurrent save. Row locks would require UPDATE rights
// that an account scope deliberately lacks on merchant configuration.
func holdOffer(ctx context.Context, tx pg.Tx, o OffersRow) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(hashtextextended($1, 0))", "payments.merchant:"+MerchantID(o.MerchantID).TypeID()); err != nil {
		return fmt.Errorf("payments: hold merchant identity: %w", err)
	}
	var current bool
	if err := tx.QueryRow(ctx, "SELECT rev = $2 AND enabled FROM payments.merchants WHERE id = $1", o.MerchantID, o.Rev).Scan(&current); err != nil {
		return fmt.Errorf("payments: check merchant revision: %w", err)
	}
	if !current {
		return ErrStale
	}
	return nil
}
