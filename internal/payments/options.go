package payments

import (
	"context"
	"errors"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

var (
	// ErrClosed is a site that has not opened top-ups, the default.
	ErrClosed = errors.New("payments: top-ups are not open on this site")
	// ErrCreditsRange is an amount outside the site's top-up limits.
	ErrCreditsRange = errors.New("payments: the amount is outside this site's top-up limits")
)

// Option is a payment method on the top-up page: what the credits cost through it, fee included. Where several
// merchant accounts offer the method, it is the cheapest that can take the amount; the order shows the total of the
// account it is placed with.
type Option struct {
	Method Method
	Total  Money
	Fee    int64
}

// settings are a site's payment settings, the defaults in code where the site stored none.
type settings struct {
	open                   bool
	creditsMin, creditsMax *int64
	strategy               Strategy
}

func readSettings(ctx context.Context, tx pg.Tx, site uuid.UUID) (settings, error) {
	rows, err := queries.SiteSettings(ctx, tx, site)
	s := settings{strategy: RoundRobin}
	if err != nil || len(rows) == 0 {
		return s, err
	}
	r := rows[0]
	if r.Open != nil {
		s.open = *r.Open
	}
	if r.Strategy != nil {
		s.strategy = Strategy(*r.Strategy)
	}
	s.creditsMin, s.creditsMax = r.CreditsMin, r.CreditsMax
	return s, nil
}

// Options lists what credits cost through each payment method of the site, for the top-up page, in a scope that
// sees the site: the account's own on the console. A method whose merchants are all over a limit, or in a currency
// without an exchange rate, is not listed.
func (p *Payments) Options(ctx context.Context, s pg.Scope, site accounts.Site, credits int64) ([]Option, error) {
	id := uuid.UUID(site.ID)
	var set settings
	var offers []OffersRow
	var usage []UsageTodayRow
	err := p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) {
		if set, err = readSettings(ctx, tx, id); err != nil || !set.open {
			return err
		}
		if offers, err = queries.Offers(ctx, tx, OffersParams{SiteID: id}); err != nil {
			return err
		}
		usage, err = usageOf(ctx, tx, offers)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err = set.admits(credits); err != nil {
		return nil, err
	}
	currencies := make([]string, len(offers))
	for i, o := range offers {
		currencies[i] = o.Currency
	}
	pr, err := p.pricing(ctx, id, site.Currency, currencies)
	if err != nil {
		return nil, err
	}
	cs, err := candidates(pr, credits, offers, usage)
	return cheapest(cs), err
}

// admits refuses a top-up the site's settings do not take.
func (s settings) admits(credits int64) error {
	switch {
	case !s.open:
		return ErrClosed
	case credits <= 0 || s.creditsMin != nil && credits < *s.creditsMin || s.creditsMax != nil && credits > *s.creditsMax:
		return ErrCreditsRange
	}
	return nil
}

// cheapest is the cheapest candidate of each method; candidates come ordered by method.
func cheapest(cs []candidate) []Option {
	var options []Option
	for _, c := range cs {
		o := Option{Method(c.offer.Method), c.quote.Total, c.quote.Fee}
		switch n := len(options); {
		case n == 0 || options[n-1].Method != o.Method:
			options = append(options, o)
		case o.Total.Minor < options[n-1].Total.Minor:
			options[n-1] = o
		}
	}
	return options
}
