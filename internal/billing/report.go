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

// CurrencyAmount is an exact amount in billionths of one currency unit (docs/domains/billing.md「钱的表示」). It is
// decimal text because a window's sum can be wider than int64 (docs/domains/billing.md「汇总和统计用更宽的整数类型」).
type CurrencyAmount struct {
	Currency platform.Currency
	Amount   string
}

// Retention is the paying-account retention of one UTC month: how many accounts paid in the previous month, and how
// many of them paid again from the first instant of this month through the window's end.
type Retention struct {
	Month    time.Time
	Cohort   int64
	Retained int64
}

// PlatformReport is one site's calls, cash and paid top-ups over a window, in the terms the operations dashboard shows
// (README.md「运营看板」): what the calls were charged, what they cost the platform where that is known, how many costs
// are unknown, the cash that moved, the accounts that paid in the window and the credit their top-ups granted, and the
// paying-account retention of the month the window ends in.
type PlatformReport struct {
	Revenue        string           // accounting currency, billionths, in decimal text: the charged amount, after the platform-borne part
	Cost           string           // accounting currency, billionths, in decimal text; a call whose cost is unknown is not in it
	UnknownCost    int64            // calls in the window whose supplier cost is unknown
	Cash           []CurrencyAmount // net cash in per payment currency, by currency; never empty
	PaidUsers      int64            // distinct accounts with a paid top-up in the window
	MonthlyRevenue string           // accounting currency, billionths, in decimal text: the credit paid top-ups granted
	Retention      Retention        // the UTC month the window ends in, and its cohort and returners
}

// ReadPlatformReport reads the site's calls, cash and paid top-ups over [since, until) in tx, which must hold the
// platform scope (or the site's). Revenue is what the calls were charged and cost is what the supplier charged us where
// billing could convert it; a call whose cost is unknown is counted apart and never added as zero, so a profit computed
// from these two figures would be wrong while UnknownCost is above zero. The time axis is the call's start, as in the
// call log, and a correction restates the call's own window. Cash is the ledger's cash book: paid credit in, refunds
// out, in the payment's currency — what a user paid is not revenue, and the call log's charge is not cash. PaidUsers
// and MonthlyRevenue come from the paid credit batches on their own time axis; retention compares the previous UTC
// month's paying accounts with the month the window ends in, through the window's end.
func ReadPlatformReport(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, since, until time.Time) (PlatformReport, error) {
	if !until.After(since) {
		return PlatformReport{}, fmt.Errorf("billing: the report window ends before it starts")
	}
	calls, err := queries.PlatformCallMoney(ctx, tx, PlatformCallMoneyParams{SiteID: site, Since: since, Until: until})
	if err != nil {
		return PlatformReport{}, fmt.Errorf("billing: read the calls over the window: %w", err)
	}
	rows, err := queries.PlatformCash(ctx, tx, PlatformCashParams{SiteID: site, Since: since, Until: until})
	if err != nil {
		return PlatformReport{}, fmt.Errorf("billing: read the cash over the window: %w", err)
	}
	cash := make(map[string]string, len(rows))
	for _, r := range rows {
		cash[r.Currency] = r.Amount
	}
	amounts, err := cashAmounts(cash, currency)
	if err != nil {
		return PlatformReport{}, err
	}
	month := monthStart(until.Add(-time.Nanosecond))
	cohortStart := month.AddDate(0, -1, 0)
	scanSince := since
	if cohortStart.Before(scanSince) {
		scanSince = cohortStart
	}
	paid, err := queries.PlatformPaid(ctx, tx, PlatformPaidParams{
		SiteID: site, ScanSince: scanSince, Since: since, Until: until,
		MonthStart: month, CohortStart: cohortStart, CohortEnd: month,
	})
	if err != nil {
		return PlatformReport{}, fmt.Errorf("billing: read the paid top-ups over the window: %w", err)
	}
	return PlatformReport{
		Revenue: calls.Revenue, Cost: calls.Cost, UnknownCost: calls.UnknownCostCalls, Cash: amounts,
		PaidUsers: paid.PaidUsers, MonthlyRevenue: paid.MonthlyRevenue,
		Retention: Retention{Month: month, Cohort: paid.Cohort, Retained: paid.Retained},
	}, nil
}

// monthStart is the UTC first day of t's month. The retention cohort and the report's month both key on it.
func monthStart(t time.Time) time.Time {
	year, month, _ := t.UTC().Date()
	return time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
}

// cashAmounts turns the cash sums into a stable list, naming the accounting currency where the ledger leaves it NULL,
// and keeps one zero amount when nothing moved so the dashboard shows a figure rather than a gap.
func cashAmounts(cash map[string]string, accounting platform.Currency) ([]CurrencyAmount, error) {
	if len(cash) == 0 {
		return []CurrencyAmount{{Currency: accounting, Amount: "0"}}, nil
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
