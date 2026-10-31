package billing

import (
	"context"
	"fmt"
	"sort"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// CurrencyAmount is an exact amount in billionths of one currency unit (docs/domains/billing.md「钱的表示」).
type CurrencyAmount struct {
	Currency platform.Currency
	Amount   int64
}

// PlatformReport is one site's calls over a window, in the terms the operations dashboard shows
// (README.md「运营看板」): what the calls were charged, what they cost the platform where that is known, how many costs
// are unknown, and the cash that moved.
type PlatformReport struct {
	Revenue     int64            // accounting currency, billionths: the charged amount, after the platform-borne part
	Cost        int64            // accounting currency, billionths; a call whose cost is unknown is not in it
	UnknownCost int64            // calls in the window whose supplier cost is unknown
	Cash        []CurrencyAmount // net cash in per payment currency, by currency; never empty
}

// ReadPlatformReport reads the site's calls and cash over [since, until) in tx, which must hold the platform scope (or
// the site's). Revenue is what the calls were charged and cost is what the supplier charged us where billing could
// convert it; a call whose cost is unknown is counted apart and never added as zero, so a profit computed from these
// two figures would be wrong while UnknownCost is above zero. The time axis is the call's start, as in the call log, and
// a correction restates the call's own window. Cash is the ledger's cash book: paid credit in, refunds out, in the
// payment's currency — what a user paid is not revenue, and the call log's charge is not cash.
func ReadPlatformReport(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, since, until time.Time) (PlatformReport, error) {
	calls, err := queries.PlatformCallMoney(ctx, tx, PlatformCallMoneyParams{SiteID: site, Since: since, Until: until})
	if err != nil {
		return PlatformReport{}, fmt.Errorf("billing: read the calls over the window: %w", err)
	}
	rows, err := queries.PlatformCash(ctx, tx, PlatformCashParams{SiteID: site, Since: since, Until: until})
	if err != nil {
		return PlatformReport{}, fmt.Errorf("billing: read the cash over the window: %w", err)
	}
	cash := make(map[string]int64, len(rows))
	for _, r := range rows {
		cash[r.Currency] += r.Amount
	}
	amounts, err := cashAmounts(cash, currency)
	if err != nil {
		return PlatformReport{}, err
	}
	return PlatformReport{
		Revenue: calls.Revenue, Cost: calls.Cost, UnknownCost: calls.UnknownCostCalls, Cash: amounts,
	}, nil
}

// cashAmounts turns the cash sums into a stable list, naming the accounting currency where the ledger leaves it NULL,
// and keeps one zero amount when nothing moved so the dashboard shows a figure rather than a gap.
func cashAmounts(cash map[string]int64, accounting platform.Currency) ([]CurrencyAmount, error) {
	if len(cash) == 0 {
		return []CurrencyAmount{{Currency: accounting}}, nil
	}
	out := make([]CurrencyAmount, 0, len(cash))
	for code, amount := range cash {
		name := accounting
		if code != "" {
			parsed, err := platform.ParseCurrency(code)
			if err != nil {
				return nil, fmt.Errorf("billing: read a cash currency: %w", err)
			}
			name = parsed
		}
		out = append(out, CurrencyAmount{Currency: name, Amount: amount})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency.String() < out[j].Currency.String() })
	return out, nil
}
