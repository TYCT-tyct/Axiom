package operations

import (
	"fmt"
	"math/big"
	"time"

	"axiom/internal/platform"
)

// expectedFrom prorates each count and money row to the part of the window inside its month and sums the months. The
// arithmetic is in big.Int because a monthly target and the product with the window's share can be wider than int64.
// A money row in another currency is skipped, not compared. A rate row contributes only when it is the target of the
// UTC month the window ends in, and is taken as it is.
func expectedFrom(rows []ReadTargetsRow, currency platform.Currency, since, until time.Time) (Expected, error) {
	return expectedFromStored(targetRows(rows), currency, since, until)
}

// expectedFromStored is expectedFrom over rows from either target table.
func expectedFromStored(rows []storedTarget, currency platform.Currency, since, until time.Time) (Expected, error) {
	endMonth := monthStart(until.Add(-time.Nanosecond))
	sums := map[Metric]*big.Int{}
	rates := map[Metric]int64{}
	for _, row := range rows {
		if err := collectExpected(sums, rates, row, endMonth, currency, since, until); err != nil {
			return Expected{}, err
		}
	}
	return expectedTargets(sums, rates, currency)
}

// collectExpected adds one row to its side of the comparison: a rate from the month the window ends in, or a count
// and money share prorated to the window.
func collectExpected(sums map[Metric]*big.Int, rates map[Metric]int64, row storedTarget, endMonth time.Time, currency platform.Currency, since, until time.Time) error {
	if isRateMetric(row.Metric) {
		rate, err := storedRate(row)
		if err != nil {
			return err
		}
		if row.Month.Equal(endMonth) {
			rates[row.Metric] = rate
		}
		return nil
	}
	return addExpected(sums, row, currency, since, until)
}

// expectedTargets turns the accumulated sums and rates into the dashboard's expected values.
func expectedTargets(sums map[Metric]*big.Int, rates map[Metric]int64, currency platform.Currency) (Expected, error) {
	targets := Targets{}
	for _, field := range targets.metrics() {
		var err error
		switch {
		case field.rate:
			if rate, ok := rates[field.metric]; ok {
				*field.count = &rate
			}
		case field.money != nil:
			*field.money, err = moneyTarget(sums[field.metric], currency)
		default:
			*field.count, err = countTarget(sums[field.metric])
		}
		if err != nil {
			return Expected{}, err
		}
	}
	return targets.expected(), nil
}

// addExpected adds one row's share to the window's sums. A money row in another currency is skipped, not compared; a
// row outside the window contributes nothing. Rate rows never reach here.
func addExpected(sums map[Metric]*big.Int, row storedTarget, currency platform.Currency, since, until time.Time) error {
	if !isCountMetric(row.Metric) && !isMoneyMetric(row.Metric) {
		return fmt.Errorf("operations: stored target has unknown metric %q", row.Metric)
	}
	if isMoneyMetric(row.Metric) && (row.Currency == nil || *row.Currency != currency.String()) {
		return nil
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

// isCountMetric reports whether metric is stored as a whole count.
func isCountMetric(metric Metric) bool {
	return metric == Calls || metric == Errors || metric == PaidUsers
}

// isMoneyMetric reports whether metric is stored as money in the site's accounting currency.
func isMoneyMetric(metric Metric) bool {
	return metric == Revenue || metric == Cost || metric == MonthlyRevenue
}

// isRateMetric reports whether metric is stored as basis points.
func isRateMetric(metric Metric) bool {
	return metric == GrossMargin || metric == Retention
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
