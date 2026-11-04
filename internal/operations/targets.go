package operations

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/typeid"
)

// Metric is one figure of the operations dashboard that takes a business target. Calls, errors and paid users are
// counts; revenue, monthly revenue and cost are money in the site's accounting currency; gross margin and retention
// are basis points, hundredths of a percent.
type Metric string

const (
	// Calls is the number of calls that started in the month.
	Calls Metric = "calls"
	// Errors is the number of those calls that ended in an error.
	Errors Metric = "errors"
	// Revenue is what settled calls were charged, in the site's accounting currency.
	Revenue Metric = "revenue"
	// Cost is the supplier cost of those calls, in the site's accounting currency.
	Cost Metric = "cost"
	// PaidUsers is the number of accounts with a paid top-up in the month.
	PaidUsers Metric = "paid_users"
	// MonthlyRevenue is the credit paid top-ups granted in the month, in the site's accounting currency.
	MonthlyRevenue Metric = "monthly_revenue"
	// GrossMargin is (call revenue - supplier cost) / call revenue, in basis points.
	GrossMargin Metric = "gross_margin"
	// Retention is the share of the previous month's paying accounts that paid again this month, in basis points.
	Retention Metric = "retention"
)

// dashboardMetrics is the fixed order every write and every audit entry uses for the first-release metrics, so
// concurrent writers take the same locks in the same order and an entry's fields do not depend on map iteration.
var dashboardMetrics = [...]Metric{Calls, Errors, Revenue, Cost}

// commercialMetrics is the same fixed order for the metrics added after the first release. They are stored apart so a
// node from before them keeps reading operations.targets without meeting a metric it does not know.
var commercialMetrics = [...]Metric{PaidUsers, MonthlyRevenue, GrossMargin, Retention}

// Metrics lists every metric in the order the editor and the audit use. The API returns it so a page can tell a node
// that stores the commercial metrics from one that predates them.
func Metrics() []Metric {
	metrics := make([]Metric, 0, len(dashboardMetrics)+len(commercialMetrics))
	metrics = append(metrics, dashboardMetrics[:]...)
	return append(metrics, commercialMetrics[:]...)
}

// Money is an exact amount in billionths of one currency unit, as decimal text (docs/domains/billing.md「钱的表示」).
// The amount is text so a target beyond a JSON number's precision keeps every digit.
type Money struct {
	Amount   string
	Currency platform.Currency
}

// Targets is one site's targets for one UTC calendar month. A nil field is a metric with no target for that month.
// Revision is 0 before the first write to the month and moves on every successful write, including a clear; an editor
// sends back the revision it read so a form cannot restore values another administrator has already replaced. Gross
// margin and retention are basis points, hundredths of a percent.
type Targets struct {
	Month          time.Time
	Revision       int64
	Calls          *int64
	Errors         *int64
	Revenue        *Money
	Cost           *Money
	PaidUsers      *int64
	MonthlyRevenue *Money
	GrossMargin    *int64
	Retention      *int64
}

// Expected is the part of a window's targets the dashboard can compare: each count and money target is the monthly one
// prorated to the window in the metric's own unit, and a nil field is a metric with no target in any month the window
// touches. Gross margin and retention are rates: their target is the one set for the UTC month the window ends in, not
// prorated, because a rate is a level rather than a quantity. Both are basis points.
type Expected struct {
	Calls          *int64
	Errors         *int64
	Revenue        *Money
	Cost           *Money
	PaidUsers      *int64
	MonthlyRevenue *Money
	GrossMargin    *int64
	Retention      *int64
}

// SetOptions carries the one difference between the full set an editor of this release writes and the first-release
// four an older editor knows.
type SetOptions struct {
	// KeepCommercial leaves paid users, monthly revenue, gross margin and retention as stored. An editor from a node
	// before those metrics cannot show or clear them, so its write must not erase what it never saw.
	KeepCommercial bool
}

// sitePrefix is the accounts module's site ID prefix. The audit target is the site whose targets changed, and every
// module writes a site under its TypeID; the prefix is part of the public contract, not a dependency on accounts.
var sitePrefix = typeid.MustPrefix("site")

// ErrStaleRevision is a write read from a revision that another write has since moved. The transaction has not
// changed anything when it is returned.
var ErrStaleRevision = errors.New("operations: the targets changed since they were read")

// MaxMoneyDigits is the width of a money target's amount, matching operations.targets.amount numeric(38, 0). Counts
// still fit int64; a money target is a planning figure, not a ledger entry, so it keeps the database's wider range.
const MaxMoneyDigits = 38

// Read reads the site's targets for one month; a month with no target rows is a Targets with only its Month and
// Revision set. The month is its UTC first day, whatever day the caller names.
func Read(ctx context.Context, tx pg.Tx, site uuid.UUID, month time.Time) (Targets, error) {
	month = monthStart(month)
	q := New()
	rows, err := q.ReadTargets(ctx, tx, ReadTargetsParams{
		SiteID: site, FromMonth: month, ToMonth: month,
	})
	if err != nil {
		return Targets{}, fmt.Errorf("operations: read the targets: %w", err)
	}
	commercial, err := q.ReadCommercialTargets(ctx, tx, ReadCommercialTargetsParams{
		SiteID: site, FromMonth: month, ToMonth: month,
	})
	if err != nil {
		return Targets{}, fmt.Errorf("operations: read the commercial targets: %w", err)
	}
	revision, err := q.ReadTargetRevision(ctx, tx, ReadTargetRevisionParams{SiteID: site, Month: month})
	if err != nil {
		return Targets{}, fmt.Errorf("operations: read the targets' revision: %w", err)
	}
	targets, err := targetsFrom(append(targetRows(rows), commercialRows(commercial)...), month)
	if err != nil {
		return Targets{}, err
	}
	targets.Revision = revision
	return targets, nil
}

// ExpectedFor reads the targets of every month the window touches and prorates each count and money target to the part
// of the window inside that month. A money target set in another currency than the site's current accounting currency
// is left out: comparing it with the window's actual amount would add two currencies as if they were one. Gross margin
// and retention take the target of the UTC month the window ends in and are not prorated. Callers check the call log's
// coverage before they show a count comparison, because archived calls would make the actual side incomplete.
func ExpectedFor(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, since, until time.Time) (Expected, error) {
	if !until.After(since) {
		return Expected{}, nil
	}
	q := New()
	from, to := monthStart(since), monthStart(until)
	rows, err := q.ReadTargets(ctx, tx, ReadTargetsParams{SiteID: site, FromMonth: from, ToMonth: to})
	if err != nil {
		return Expected{}, fmt.Errorf("operations: read the targets over the window: %w", err)
	}
	commercial, err := q.ReadCommercialTargets(ctx, tx, ReadCommercialTargetsParams{
		SiteID: site, FromMonth: from, ToMonth: to,
	})
	if err != nil {
		return Expected{}, fmt.Errorf("operations: read the commercial targets over the window: %w", err)
	}
	return expectedFromStored(append(targetRows(rows), commercialRows(commercial)...), currency, since, until)
}

// Set replaces the site's targets for set.Month in one transaction: a metric with a nil value loses its row, a set one
// is written. It records the change in the audit log with the values it replaced, in the same transaction, so an
// unrecorded change cannot stand. A writer of the same site and month takes an advisory lock first, so two
// administrators' changes have one order and each audit entry names the values that were really there. expectedRev
// is the revision the caller read; a write that finds a different revision refuses with ErrStaleRevision instead of
// restoring the form's older values. A successful write, including a clear, moves the revision by one. With
// opts.KeepCommercial, the commercial metrics keep their stored values and the audit records them unchanged.
func Set(ctx context.Context, tx pg.Tx, actor audit.Actor, site uuid.UUID, set Targets, expectedRev int64, reason string, opts SetOptions) error {
	if err := set.Check(); err != nil {
		return err
	}
	month := monthStart(set.Month)
	q := New()
	if err := q.LockTargets(ctx, tx, site.String()+":"+month.Format("2006-01")); err != nil {
		return fmt.Errorf("operations: lock the targets: %w", err)
	}
	before, err := Read(ctx, tx, site, month)
	if err != nil {
		return err
	}
	if before.Revision != expectedRev {
		return fmt.Errorf("%w: at revision %d, the request read %d", ErrStaleRevision, before.Revision, expectedRev)
	}
	after := before
	after.Calls, after.Errors, after.Revenue, after.Cost = set.Calls, set.Errors, set.Revenue, set.Cost
	if !opts.KeepCommercial {
		after.PaidUsers, after.MonthlyRevenue = set.PaidUsers, set.MonthlyRevenue
		after.GrossMargin, after.Retention = set.GrossMargin, set.Retention
	}
	for _, metric := range dashboardMetrics {
		if err := writeTarget(ctx, tx, q, site, month, metric, after); err != nil {
			return err
		}
	}
	if !opts.KeepCommercial {
		for _, metric := range commercialMetrics {
			if err := writeCommercialTarget(ctx, tx, q, site, month, metric, after); err != nil {
				return err
			}
		}
	}
	if err := q.UpsertTargetRevision(ctx, tx, UpsertTargetRevisionParams{
		SiteID: site, Month: month, Revision: before.Revision + 1,
	}); err != nil {
		return fmt.Errorf("operations: move the targets' revision: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "targets.update", Reason: reason,
		Target: audit.Target{Module: "operations", Kind: "site", ID: sitePrefix.Encode(site), Site: site},
		Detail: map[string]any{"month": month.Format("2006-01"), "before": before.detail(), "after": after.detail()},
	})
}

// Check refuses a target that the table would not accept as it stands, before a request reaches the database.
func (t *Targets) Check() error {
	if t.Month.IsZero() || !t.Month.Equal(monthStart(t.Month)) {
		return fmt.Errorf("operations: a target's month must be the first day of a month, got %s", t.Month)
	}
	for name, count := range map[string]*int64{"calls": t.Calls, "errors": t.Errors, "paid users": t.PaidUsers} {
		if count != nil && *count < 0 {
			return fmt.Errorf("operations: a %s target cannot be negative", name)
		}
	}
	for name, money := range map[string]*Money{"revenue": t.Revenue, "cost": t.Cost, "monthly revenue": t.MonthlyRevenue} {
		if err := checkMoney(name, money); err != nil {
			return err
		}
	}
	for name, rate := range map[string]*int64{"gross margin": t.GrossMargin, "retention": t.Retention} {
		if rate != nil && (*rate < 0 || *rate > 10000) {
			return fmt.Errorf("operations: a %s target must be between 0 and 10000 basis points", name)
		}
	}
	return nil
}

// checkMoney applies the table's rules for one money target; a nil target is absent.
func checkMoney(name string, money *Money) error {
	if money == nil {
		return nil
	}
	amount, ok := new(big.Int).SetString(money.Amount, 10)
	if !ok || amount.Sign() < 0 {
		return fmt.Errorf("operations: a %s target must be a non-negative whole number of billionths", name)
	}
	if len(amount.String()) > MaxMoneyDigits {
		return fmt.Errorf("operations: a %s target must fit %d digits", name, MaxMoneyDigits)
	}
	if _, err := platform.ParseCurrency(money.Currency.String()); err != nil {
		return fmt.Errorf("operations: a %s target: %w", name, err)
	}
	return nil
}

// writeTarget writes one first-release metric of set, or deletes it when it has no value.
func writeTarget(ctx context.Context, tx pg.Tx, queries *Queries, site uuid.UUID, month time.Time, metric Metric, set Targets) error {
	var amount, currency *string
	switch metric {
	case Calls:
		amount = countAmount(set.Calls)
	case Errors:
		amount = countAmount(set.Errors)
	case Revenue:
		amount, currency = moneyAmount(set.Revenue)
	case Cost:
		amount, currency = moneyAmount(set.Cost)
	default:
		return fmt.Errorf("operations: unknown metric %q", metric)
	}
	if amount == nil {
		if err := queries.DeleteTarget(ctx, tx, DeleteTargetParams{SiteID: site, Metric: string(metric), Month: month}); err != nil {
			return fmt.Errorf("operations: clear the %s target: %w", metric, err)
		}
		return nil
	}
	if err := queries.UpsertTarget(ctx, tx, UpsertTargetParams{
		SiteID: site, Metric: string(metric), Month: month, Amount: *amount, Currency: currency,
	}); err != nil {
		return fmt.Errorf("operations: write the %s target: %w", metric, err)
	}
	return nil
}

// writeCommercialTarget writes one commercial metric of set, or deletes it when it has no value.
func writeCommercialTarget(ctx context.Context, tx pg.Tx, queries *Queries, site uuid.UUID, month time.Time, metric Metric, set Targets) error {
	var amount, currency *string
	switch metric {
	case PaidUsers:
		amount = countAmount(set.PaidUsers)
	case MonthlyRevenue:
		amount, currency = moneyAmount(set.MonthlyRevenue)
	case GrossMargin:
		amount = countAmount(set.GrossMargin)
	case Retention:
		amount = countAmount(set.Retention)
	default:
		return fmt.Errorf("operations: unknown commercial metric %q", metric)
	}
	if amount == nil {
		if err := queries.DeleteCommercialTarget(ctx, tx, DeleteCommercialTargetParams{SiteID: site, Metric: string(metric), Month: month}); err != nil {
			return fmt.Errorf("operations: clear the %s target: %w", metric, err)
		}
		return nil
	}
	if err := queries.UpsertCommercialTarget(ctx, tx, UpsertCommercialTargetParams{
		SiteID: site, Metric: string(metric), Month: month, Amount: *amount, Currency: currency,
	}); err != nil {
		return fmt.Errorf("operations: write the %s target: %w", metric, err)
	}
	return nil
}

// countAmount is the stored text of a count or rate target; nil stays nil so the row is deleted.
func countAmount(value *int64) *string {
	if value == nil {
		return nil
	}
	amount := strconv.FormatInt(*value, 10)
	return &amount
}

// moneyAmount is the stored amount and currency of a money target; nil stays nil so the row is deleted.
func moneyAmount(value *Money) (*string, *string) {
	if value == nil {
		return nil, nil
	}
	currency := value.Currency.String()
	return &value.Amount, &currency
}

// storedTarget is one stored row from either target table, so reading and prorating do not care which table it came from.
type storedTarget struct {
	Month    time.Time
	Metric   Metric
	Amount   string
	Currency *string
}

func targetRows(rows []ReadTargetsRow) []storedTarget {
	out := make([]storedTarget, len(rows))
	for i, row := range rows {
		out[i] = storedTarget{Month: row.Month, Metric: Metric(row.Metric), Amount: row.Amount, Currency: row.Currency}
	}
	return out
}

func commercialRows(rows []ReadCommercialTargetsRow) []storedTarget {
	out := make([]storedTarget, len(rows))
	for i, row := range rows {
		out[i] = storedTarget{Month: row.Month, Metric: Metric(row.Metric), Amount: row.Amount, Currency: row.Currency}
	}
	return out
}

// targetsFrom turns one month's rows into Targets, refusing a stored value that does not fit its metric.
func targetsFrom(rows []storedTarget, month time.Time) (Targets, error) {
	targets := Targets{Month: month}
	for _, row := range rows {
		if err := targets.add(row); err != nil {
			return Targets{}, err
		}
	}
	return targets, nil
}

func (t *Targets) add(row storedTarget) error {
	switch row.Metric {
	case Calls:
		count, err := storedCount(row)
		if err != nil {
			return err
		}
		t.Calls = &count
	case Errors:
		count, err := storedCount(row)
		if err != nil {
			return err
		}
		t.Errors = &count
	case Revenue:
		money, err := storedMoney(row)
		if err != nil {
			return err
		}
		t.Revenue = money
	case Cost:
		money, err := storedMoney(row)
		if err != nil {
			return err
		}
		t.Cost = money
	case PaidUsers:
		count, err := storedCount(row)
		if err != nil {
			return err
		}
		t.PaidUsers = &count
	case MonthlyRevenue:
		money, err := storedMoney(row)
		if err != nil {
			return err
		}
		t.MonthlyRevenue = money
	case GrossMargin:
		rate, err := storedRate(row)
		if err != nil {
			return err
		}
		t.GrossMargin = &rate
	case Retention:
		rate, err := storedRate(row)
		if err != nil {
			return err
		}
		t.Retention = &rate
	default:
		return fmt.Errorf("operations: stored target has unknown metric %q", row.Metric)
	}
	return nil
}

func storedCount(row storedTarget) (int64, error) {
	count, err := strconv.ParseInt(row.Amount, 10, 64)
	if err != nil || count < 0 {
		return 0, fmt.Errorf("operations: stored %s target %q is not a non-negative count", row.Metric, row.Amount)
	}
	return count, nil
}

func storedRate(row storedTarget) (int64, error) {
	rate, err := strconv.ParseInt(row.Amount, 10, 64)
	if err != nil || rate < 0 || rate > 10000 {
		return 0, fmt.Errorf("operations: stored %s target %q is not a rate in basis points", row.Metric, row.Amount)
	}
	if row.Currency != nil {
		return 0, fmt.Errorf("operations: stored %s target has a currency", row.Metric)
	}
	return rate, nil
}

func storedMoney(row storedTarget) (*Money, error) {
	if row.Currency == nil {
		return nil, fmt.Errorf("operations: stored %s target has no currency", row.Metric)
	}
	currency, err := platform.ParseCurrency(*row.Currency)
	if err != nil {
		return nil, fmt.Errorf("operations: stored %s target: %w", row.Metric, err)
	}
	return &Money{Amount: row.Amount, Currency: currency}, nil
}

// expectedFrom prorates each count and money row to the part of the window inside its month and sums the months. The
// arithmetic is in big.Int because a monthly target and the product with the window's share can be wider than int64.
// A money row in another currency is skipped, not compared. A rate row contributes only when it is the target of the
// UTC month the window ends in, and is taken as it is.
func expectedFrom(rows []ReadTargetsRow, currency platform.Currency, since, until time.Time) (Expected, error) {
	return expectedFromStored(targetRows(rows), currency, since, until)
}

// expectedFromStored is expectedFrom over rows from either target table.
func expectedFromStored(rows []storedTarget, currency platform.Currency, since, until time.Time) (Expected, error) {
	sums := map[Metric]*big.Int{}
	endMonth := monthStart(until.Add(-time.Nanosecond))
	rates := map[Metric]int64{}
	for _, row := range rows {
		switch row.Metric {
		case GrossMargin, Retention:
			rate, err := storedRate(row)
			if err != nil {
				return Expected{}, err
			}
			if row.Month.Equal(endMonth) {
				rates[row.Metric] = rate
			}
			continue
		}
		if err := addExpected(sums, row, currency, since, until); err != nil {
			return Expected{}, err
		}
	}
	expected := Expected{}
	var err error
	if expected.Calls, err = countTarget(sums[Calls]); err != nil {
		return Expected{}, err
	}
	if expected.Errors, err = countTarget(sums[Errors]); err != nil {
		return Expected{}, err
	}
	if expected.Revenue, err = moneyTarget(sums[Revenue], currency); err != nil {
		return Expected{}, err
	}
	if expected.Cost, err = moneyTarget(sums[Cost], currency); err != nil {
		return Expected{}, err
	}
	if expected.PaidUsers, err = countTarget(sums[PaidUsers]); err != nil {
		return Expected{}, err
	}
	if expected.MonthlyRevenue, err = moneyTarget(sums[MonthlyRevenue], currency); err != nil {
		return Expected{}, err
	}
	if rate, ok := rates[GrossMargin]; ok {
		expected.GrossMargin = &rate
	}
	if rate, ok := rates[Retention]; ok {
		expected.Retention = &rate
	}
	return expected, nil
}

// addExpected adds one row's share to the window's sums. A money row in another currency is skipped, not compared; a
// row outside the window contributes nothing. Rate rows never reach here.
func addExpected(sums map[Metric]*big.Int, row storedTarget, currency platform.Currency, since, until time.Time) error {
	switch row.Metric {
	case Calls, Errors, PaidUsers:
	case Revenue, Cost, MonthlyRevenue:
		if row.Currency == nil || *row.Currency != currency.String() {
			return nil
		}
	default:
		return fmt.Errorf("operations: stored target has unknown metric %q", row.Metric)
	}
	amount, ok := new(big.Int).SetString(row.Amount, 10)
	if !ok || amount.Sign() < 0 {
		return fmt.Errorf("operations: stored %s target %q is not a non-negative amount", row.Metric, row.Amount)
	}
	monthEnd := row.Month.AddDate(0, 1, 0)
	start := later(since, row.Month)
	end := earlier(until, monthEnd)
	if !end.After(start) {
		return nil
	}
	part := prorate(amount, monthEnd.Sub(row.Month), end.Sub(start))
	if sum, ok := sums[row.Metric]; ok {
		sum.Add(sum, part)
		return nil
	}
	sums[row.Metric] = part
	return nil
}

// prorate is amount × part / whole rounded half up, in the metric's unit. whole is one month and part the covered
// part of it; both must be positive, and amount non-negative. Counts thereby round to the nearest whole call or
// error, money to the nearest billionth of a unit.
func prorate(amount *big.Int, whole, part time.Duration) *big.Int {
	// floor((2·amount·part + whole) / (2·whole)) is half up for a non-negative amount. Duration is nanoseconds, so a
	// window with a fractional second keeps that fraction; rounding happens once, in the metric's unit, rather than
	// truncating each month's overlap to whole seconds.
	numerator := new(big.Int).Mul(amount, big.NewInt(int64(part)))
	numerator.Lsh(numerator, 1)
	numerator.Add(numerator, big.NewInt(int64(whole)))
	denominator := big.NewInt(int64(whole))
	denominator.Lsh(denominator, 1)
	return numerator.Quo(numerator, denominator)
}

func countTarget(sum *big.Int) (*int64, error) {
	if sum == nil {
		return nil, nil //nolint:nilnil // a metric absent from every month is an absent target, not an error
	}
	if !sum.IsInt64() {
		return nil, fmt.Errorf("operations: a prorated count target is wider than 64 bits")
	}
	count := sum.Int64()
	return &count, nil
}

func moneyTarget(sum *big.Int, currency platform.Currency) (*Money, error) {
	if sum == nil {
		return nil, nil //nolint:nilnil // a metric absent from every month is an absent target, not an error
	}
	if sum.Sign() < 0 {
		return nil, fmt.Errorf("operations: a prorated money target is negative")
	}
	return &Money{Amount: sum.String(), Currency: currency}, nil
}

// detail is the audit entry's side of the change: the metrics that have a value, so clearing a target shows as the
// metric disappearing from the map.
func (t *Targets) detail() map[string]any {
	detail := map[string]any{}
	if t.Calls != nil {
		detail["calls"] = *t.Calls
	}
	if t.Errors != nil {
		detail["errors"] = *t.Errors
	}
	if t.Revenue != nil {
		detail["revenue"] = map[string]any{"amount": t.Revenue.Amount, "currency": t.Revenue.Currency.String()}
	}
	if t.Cost != nil {
		detail["cost"] = map[string]any{"amount": t.Cost.Amount, "currency": t.Cost.Currency.String()}
	}
	if t.PaidUsers != nil {
		detail["paid_users"] = *t.PaidUsers
	}
	if t.MonthlyRevenue != nil {
		detail["monthly_revenue"] = map[string]any{
			"amount": t.MonthlyRevenue.Amount, "currency": t.MonthlyRevenue.Currency.String(),
		}
	}
	if t.GrossMargin != nil {
		detail["gross_margin"] = *t.GrossMargin
	}
	if t.Retention != nil {
		detail["retention"] = *t.Retention
	}
	return detail
}

// monthStart is the UTC first day of t's month, the key a target is stored under.
func monthStart(t time.Time) time.Time {
	year, month, _ := t.UTC().Date()
	return time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
