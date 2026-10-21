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

// Option is a payment method on the top-up page: what the credits cost through it, fee included, at the merchant
// account an order would be placed with now (ranked).
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

// Options lists what credits cost through each payment method of the site, for the top-up page, in a scope that
// sees the site: the account's own on the console (pg.AccountScopeIn). A method whose merchants are all over a limit,
// unusable, or in a currency without an exchange rate is not listed.
func (p *Payments) Options(ctx context.Context, s pg.Scope, site accounts.Site, credits int64) ([]Option, error) {
	m, err := p.menu(ctx, s, site, nil)
	if err != nil {
		return nil, err
	}
	if err = m.settings.admits(credits); err != nil {
		return nil, err
	}
	var options []Option
	for i := 0; i < len(m.offers); {
		j := i
		for j < len(m.offers) && m.offers[j].Method == m.offers[i].Method {
			j++
		}
		cs, err := p.ranked(m, m.offers[i].Method, m.offers[i:j], m.usage, credits, false)
		if err != nil {
			return nil, err
		}
		if len(cs) > 0 {
			options = append(options, Option{Method(m.offers[i].Method), cs[0].quote.Total, cs[0].quote.Fee})
		}
		i = j
	}
	return options, nil
}
