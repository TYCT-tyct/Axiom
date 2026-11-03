package operations

import (
	"context"
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

// Metric is one figure of the operations dashboard that takes a business target. Calls and errors are counts;
// revenue and cost are money in the site's accounting currency.
type Metric string

const (
	Calls   Metric = "calls"
	Errors  Metric = "errors"
	Revenue Metric = "revenue"
	Cost    Metric = "cost"
)

// dashboardMetrics is the fixed order every write and every audit entry uses, so concurrent writers take the same
// locks in the same order and an entry's fields do not depend on map iteration.
var dashboardMetrics = [...]Metric{Calls, Errors, Revenue, Cost}

// Money is an exact amount in billionths of one currency unit, as decimal text (docs/domains/billing.md「钱的表示」).
// The amount is text so a target beyond a JSON number's precision keeps every digit.
type Money struct {
	Amount   string
	Currency platform.Currency
}

// Targets is one site's targets for one UTC calendar month. A nil field is a metric with no target for that month.
type Targets struct {
	Month   time.Time
	Calls   *int64
	Errors  *int64
	Revenue *Money
	Cost    *Money
}

// Expected is the part of a window's targets the dashboard can compare: each target is the monthly one prorated to
// the window in the metric's own unit, and a nil field is a metric with no target in any month the window touches.
type Expected struct {
	Calls   *int64
	Errors  *int64
	Revenue *Money
	Cost    *Money
}

// sitePrefix is the accounts module's site ID prefix. The audit target is the site whose targets changed, and every
// module writes a site under its TypeID; the prefix is part of the public contract, not a dependency on accounts.
var sitePrefix = typeid.MustPrefix("site")

// Read reads the site's targets for one month; a month with no rows is a Targets with only its Month set. The month
// is its UTC first day, whatever day the caller names.
func Read(ctx context.Context, tx pg.Tx, site uuid.UUID, month time.Time) (Targets, error) {
	month = monthStart(month)
	rows, err := New().ReadTargets(ctx, tx, ReadTargetsParams{
		SiteID: site, FromMonth: month, ToMonth: month,
	})
	if err != nil {
		return Targets{}, fmt.Errorf("operations: read the targets: %w", err)
	}
	return targetsFrom(rows, month)
}

// Expected reads the targets of every month the window touches and prorates each to the part of the window inside that
// month. Money targets set in another currency than the site's current accounting currency are left out: comparing
// them with the window's actual amount would add two currencies as if they were one. Callers check the call log's
// coverage before they show a count comparison, because archived calls would make the actual side incomplete.
func ExpectedFor(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, since, until time.Time) (Expected, error) {
	if !until.After(since) {
		return Expected{}, nil
	}
	rows, err := New().ReadTargets(ctx, tx, ReadTargetsParams{
		SiteID: site, FromMonth: monthStart(since), ToMonth: monthStart(until),
	})
	if err != nil {
		return Expected{}, fmt.Errorf("operations: read the targets over the window: %w", err)
	}
	return expectedFrom(rows, currency, since, until)
}

// Set replaces the site's targets for set.Month in one transaction: a metric with a nil value loses its row, a set one
// is written. It records the change in the audit log with the values it replaced, in the same transaction, so an
// unrecorded change cannot stand. A writer of the same site and month takes an advisory lock first, so two
// administrators' changes have one order and each audit entry names the values that were really there.
func Set(ctx context.Context, tx pg.Tx, actor audit.Actor, site uuid.UUID, set Targets, reason string) error {
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
	for _, metric := range dashboardMetrics {
		if err := writeTarget(ctx, tx, q, site, month, metric, set); err != nil {
			return err
		}
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "targets.update", Reason: reason,
		Target: audit.Target{Module: "operations", Kind: "site", ID: sitePrefix.Encode(site), Site: site},
		Detail: map[string]any{"month": month.Format("2006-01"), "before": before.detail(), "after": set.detail()},
	})
}

// Check refuses a target that the table would not accept as it stands, before a request reaches the database.
func (t Targets) Check() error {
	if t.Month.IsZero() || !t.Month.Equal(monthStart(t.Month)) {
		return fmt.Errorf("operations: a target's month must be the first day of a month, got %s", t.Month)
	}
	for name, count := range map[string]*int64{"calls": t.Calls, "errors": t.Errors} {
		if count != nil && *count < 0 {
			return fmt.Errorf("operations: a %s target cannot be negative", name)
		}
	}
	for name, money := range map[string]*Money{"revenue": t.Revenue, "cost": t.Cost} {
		if money == nil {
			continue
		}
		amount, ok := new(big.Int).SetString(money.Amount, 10)
		if !ok || amount.Sign() < 0 {
			return fmt.Errorf("operations: a %s target must be a non-negative whole number of billionths", name)
		}
		if _, err := platform.ParseCurrency(money.Currency.String()); err != nil {
			return fmt.Errorf("operations: a %s target: %w", name, err)
		}
	}
	return nil
}

// writeTarget writes one metric of set, or deletes it when it has no value.
func writeTarget(ctx context.Context, tx pg.Tx, queries *Queries, site uuid.UUID, month time.Time, metric Metric, set Targets) error {
	count := func(value *int64) (*string, *string, error) {
		if value == nil {
			return nil, nil, nil
		}
		amount := strconv.FormatInt(*value, 10)
		return &amount, nil, nil
	}
	money := func(value *Money) (*string, *string, error) {
		if value == nil {
			return nil, nil, nil
		}
		currency := value.Currency.String()
		return &value.Amount, &currency, nil
	}
	var amount, currency *string
	var err error
	switch metric {
	case Calls:
		amount, currency, err = count(set.Calls)
	case Errors:
		amount, currency, err = count(set.Errors)
	case Revenue:
		amount, currency, err = money(set.Revenue)
	case Cost:
		amount, currency, err = money(set.Cost)
	default:
		err = fmt.Errorf("operations: unknown metric %q", metric)
	}
	if err != nil {
		return err
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

// targetsFrom turns one month's rows into Targets, refusing a stored value that does not fit its metric.
func targetsFrom(rows []ReadTargetsRow, month time.Time) (Targets, error) {
	targets := Targets{Month: month}
	for _, row := range rows {
		switch Metric(row.Metric) {
		case Calls, Errors:
			count, err := strconv.ParseInt(row.Amount, 10, 64)
			if err != nil || count < 0 {
				return Targets{}, fmt.Errorf("operations: stored %s target %q is not a non-negative count", row.Metric, row.Amount)
			}
			if Metric(row.Metric) == Calls {
				targets.Calls = &count
			} else {
				targets.Errors = &count
			}
		case Revenue, Cost:
			if row.Currency == nil {
				return Targets{}, fmt.Errorf("operations: stored %s target has no currency", row.Metric)
			}
			currency, err := platform.ParseCurrency(*row.Currency)
			if err != nil {
				return Targets{}, fmt.Errorf("operations: stored %s target: %w", row.Metric, err)
			}
			money := &Money{Amount: row.Amount, Currency: currency}
			if Metric(row.Metric) == Revenue {
				targets.Revenue = money
			} else {
				targets.Cost = money
			}
		default:
			return Targets{}, fmt.Errorf("operations: stored target has unknown metric %q", row.Metric)
		}
	}
	return targets, nil
}

// expectedFrom prorates each row to the part of the window inside its month and sums the months. The arithmetic is
// in big.Int because a monthly target and the product with the window's share can be wider than int64. A money row
// in another currency is skipped, not compared.
func expectedFrom(rows []ReadTargetsRow, currency platform.Currency, since, until time.Time) (Expected, error) {
	sums := map[Metric]*big.Int{}
	for _, row := range rows {
		metric := Metric(row.Metric)
		if metric == Revenue || metric == Cost {
			if row.Currency == nil || *row.Currency != currency.String() {
				continue
			}
		}
		amount, ok := new(big.Int).SetString(row.Amount, 10)
		if !ok || amount.Sign() < 0 {
			return Expected{}, fmt.Errorf("operations: stored %s target %q is not a non-negative amount", row.Metric, row.Amount)
		}
		monthEnd := row.Month.AddDate(0, 1, 0)
		start := later(since, row.Month)
		end := earlier(until, monthEnd)
		if !end.After(start) {
			continue
		}
		part := prorate(amount, monthEnd.Sub(row.Month), end.Sub(start))
		sum, ok := sums[metric]
		if !ok {
			sums[metric] = part
			continue
		}
		sum.Add(sum, part)
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
	return expected, nil
}

// prorate is amount × part / whole rounded half up, in the metric's unit. whole is one month and part the covered
// part of it; both must be positive, and amount non-negative. Counts thereby round to the nearest whole call or
// error, money to the nearest billionth of a unit.
func prorate(amount *big.Int, whole, part time.Duration) *big.Int {
	// floor((2·amount·part + whole) / (2·whole)) is half up for a non-negative amount.
	numerator := new(big.Int).Mul(amount, big.NewInt(int64(part/time.Second)))
	numerator.Lsh(numerator, 1)
	numerator.Add(numerator, big.NewInt(int64(whole/time.Second)))
	denominator := big.NewInt(int64(whole / time.Second))
	denominator.Lsh(denominator, 1)
	return numerator.Quo(numerator, denominator)
}

func countTarget(sum *big.Int) (*int64, error) {
	if sum == nil {
		return nil, nil
	}
	if !sum.IsInt64() {
		return nil, fmt.Errorf("operations: a prorated count target is wider than 64 bits")
	}
	count := sum.Int64()
	return &count, nil
}

func moneyTarget(sum *big.Int, currency platform.Currency) (*Money, error) {
	if sum == nil {
		return nil, nil
	}
	if sum.Sign() < 0 {
		return nil, fmt.Errorf("operations: a prorated money target is negative")
	}
	return &Money{Amount: sum.String(), Currency: currency}, nil
}

// detail is the audit entry's side of the change: the metrics that have a value, so clearing a target shows as the
// metric disappearing from the map.
func (t Targets) detail() map[string]any {
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
