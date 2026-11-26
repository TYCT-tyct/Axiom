package billing

import (
	"cmp"
	"context"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
)

// The administrator's reads across every account's subscriptions (docs/domains/operations.md「决策与理由」): one page of
// them, and one site's counts with what its subscriptions sold for.

// SubscriptionQuery is one page of every account's subscriptions. Each filter is exact and narrows nothing when unset.
// ByEnd orders the soonest to end first, by (ends_at, id); otherwise the newest first, by (created_at, id). After is
// the last row of the previous page on the same axis.
type SubscriptionQuery struct {
	State         string // "active" or "expired"
	Plan, Account *uuid.UUID
	EndsBefore    *time.Time
	ByEnd         bool
	After         *page.Cursor
	Limit         int
}

// ListAllSubscriptions reads one page of q in tx, which must be in the platform scope, through the index of its order
// (migrations 00050).
func ListAllSubscriptions(ctx context.Context, tx pg.Tx, q SubscriptionQuery) ([]Subscription, *page.Cursor, error) {
	sql, args := q.sql()
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("billing: list the subscriptions: %w", err)
	}
	defer rows.Close()
	out := make([]Subscription, 0, q.Limit+1)
	for rows.Next() {
		var s Subscription
		if err := rows.Scan(&s.ID, &s.Site, &s.Account, &s.PlanID, &s.PlanName, &s.StartsAt, &s.EndsAt, &s.CurrentSeq,
			&s.State, &s.ClosedAt, &s.CreatedAt, &s.Window.Seq, &s.Window.StartsAt, &s.Window.EndsAt, &s.Window.Quota,
			&s.Window.Used, &s.Window.Reserved); err != nil {
			return nil, nil, fmt.Errorf("billing: list the subscriptions: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("billing: list the subscriptions: %w", err)
	}
	if len(out) <= q.Limit {
		return out, nil, nil
	}
	out = out[:q.Limit]
	last := out[len(out)-1]
	next := &page.Cursor{At: last.CreatedAt, ID: last.ID}
	if q.ByEnd {
		next.At = last.EndsAt
	}
	return out, next, nil
}

// sql is q's statement and its arguments. The conditions are only those q sets, so the planner sees no always-true
// branch to plan around and reads the index of the order.
func (q SubscriptionQuery) sql() (string, []any) {
	var where []string
	var args []any
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	if q.State != "" {
		where = append(where, "s.state = "+arg(q.State))
	}
	if q.Plan != nil {
		where = append(where, "s.plan_id = "+arg(*q.Plan))
	}
	if q.Account != nil {
		where = append(where, "s.account_id = "+arg(*q.Account))
	}
	if q.EndsBefore != nil {
		where = append(where, "s.ends_at < "+arg(*q.EndsBefore))
	}
	axis, after, order := "s.created_at", "<", "s.created_at DESC, s.id DESC"
	if q.ByEnd {
		axis, after, order = "s.ends_at", ">", "s.ends_at, s.id"
	}
	if q.After != nil {
		where = append(where, fmt.Sprintf("(%s, s.id) %s (%s, %s)", axis, after, arg(q.After.At), arg(q.After.ID)))
	}
	sql := `SELECT s.id, s.site_id, s.account_id, s.plan_id, s.plan_name, s.starts_at, s.ends_at, s.current_seq, s.state,
    s.closed_at, s.created_at, w.seq, w.starts_at, w.ends_at, w.quota, w.used, w.reserved
FROM billing.subscriptions s
JOIN billing.subscription_windows w ON w.subscription_id = s.id AND w.seq = s.current_seq`
	if len(where) > 0 {
		sql += "\nWHERE " + strings.Join(where, " AND ")
	}
	return sql + "\nORDER BY " + order + " LIMIT " + arg(q.Limit+1), args
}

// SubscriptionSummary is one site's subscriptions counted at the database's clock, and what its subscriptions sold for
// in a UTC month and the month before. Revenue is in billionths of the site's accounting currency.
type SubscriptionSummary struct {
	Month                    time.Time // the first instant of the UTC month
	Active, EndingSoon       int64     // active subscriptions; of them, those ending within 7 days
	Revenue, PreviousRevenue *big.Int
	Plans                    []PlanShare // every plan of the site, by ID
}

// PlanShare is one plan's active subscriptions and their share of all the site's active ones, in basis points.
type PlanShare struct {
	Plan    uuid.UUID
	Active  int64
	ShareBP int64
}

// endingSoon is the horizon of SubscriptionSummary.EndingSoon.
const endingSoon = 7 * 24 * time.Hour

// ReadSubscriptionSummary reads site's summary in tx, which must be in the platform scope. A zero month is the current
// UTC month at the database's clock.
//
// Revenue invariants: only paid grants count, a gift never does; a grant counts in the UTC month of its created_at and
// in no other; its value is CreditsFor at its own rate, the credit the ledger posted against the plan's liability, so
// the revenue of a month is the sum of what its sales were worth in the site's currency, and refunds made later do not
// rewrite it. The shares add up to exactly 10000 whenever any subscription is active.
func ReadSubscriptionSummary(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, month time.Time) (SubscriptionSummary, error) {
	now, err := queries.Now(ctx, tx)
	if err != nil {
		return SubscriptionSummary{}, fmt.Errorf("billing: read the clock: %w", err)
	}
	if month.IsZero() {
		month = now
	}
	s := SubscriptionSummary{Month: monthStart(month)}
	if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE ends_at < $2)
FROM billing.subscriptions WHERE site_id = $1 AND state = 'active'`, site, now.Add(endingSoon)).Scan(&s.Active, &s.EndingSoon); err != nil {
		return SubscriptionSummary{}, fmt.Errorf("billing: count the active subscriptions: %w", err)
	}
	if s.Plans, err = planShares(ctx, tx, site); err != nil {
		return SubscriptionSummary{}, err
	}
	previous := s.Month.AddDate(0, -1, 0)
	if s.PreviousRevenue, err = soldFor(ctx, tx, site, currency, previous, s.Month); err != nil {
		return SubscriptionSummary{}, err
	}
	if s.Revenue, err = soldFor(ctx, tx, site, currency, s.Month, s.Month.AddDate(0, 1, 0)); err != nil {
		return SubscriptionSummary{}, err
	}
	return s, nil
}

// planShares is every plan of the site with its active subscriptions. The shares are rounded by the largest remainder,
// ties to the plan that sorts first, so they add up to 10000 rather than drifting by the rounding of each.
func planShares(ctx context.Context, tx pg.Tx, site uuid.UUID) ([]PlanShare, error) {
	rows, err := tx.Query(ctx, `SELECT p.id, count(s.id)
FROM billing.subscription_plans p
LEFT JOIN billing.subscriptions s ON s.plan_id = p.id AND s.site_id = $1 AND s.state = 'active'
WHERE p.site_id = $1 GROUP BY p.id ORDER BY p.id`, site)
	if err != nil {
		return nil, fmt.Errorf("billing: count the plans' subscriptions: %w", err)
	}
	defer rows.Close()
	var plans []PlanShare
	var total int64
	for rows.Next() {
		var p PlanShare
		if err := rows.Scan(&p.Plan, &p.Active); err != nil {
			return nil, fmt.Errorf("billing: count the plans' subscriptions: %w", err)
		}
		plans = append(plans, p)
		total += p.Active
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("billing: count the plans' subscriptions: %w", err)
	}
	if total == 0 {
		return plans, nil
	}
	remainders := make([]int64, len(plans))
	var given int64
	for i := range plans {
		share := plans[i].Active * 10000
		plans[i].ShareBP, remainders[i] = share/total, share%total
		given += plans[i].ShareBP
	}
	order := make([]int, len(plans))
	for i := range order {
		order[i] = i
	}
	// The floors fall short of 10000 by less than one for each plan, so every plan gets at most one more.
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(remainders[b], remainders[a]) })
	for _, i := range order[:10000-given] {
		plans[i].ShareBP++
	}
	return plans, nil
}

// soldFor is the value of the site's paid grants created in [since, until), in billionths of currency. Grants of the
// same payment and rate are worth the same, so each such group is converted once and multiplied by its count; the
// groups are as many as the distinct prices sold, not the sales.
func soldFor(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, since, until time.Time) (*big.Int, error) {
	sales, err := readSales(ctx, tx, site, since, until)
	if err != nil {
		return nil, err
	}
	total := new(big.Int)
	for _, s := range sales {
		rate, err := readRate(ctx, tx, s.rate)
		if err != nil {
			return nil, err
		}
		value, err := CreditsFor(s.payment, currency, rate)
		if err != nil {
			return nil, err
		}
		total.Add(total, new(big.Int).Mul(big.NewInt(value), big.NewInt(s.count)))
	}
	return total, nil
}

// sale is count paid grants of one payment at one rate; the zero rate is none, a payment in the site's currency.
type sale struct {
	payment Payment
	rate    uuid.UUID
	count   int64
}

// readSales groups the site's paid grants created in [since, until) by payment and rate. It reads them all before
// soldFor reads a rate, which needs the connection.
func readSales(ctx context.Context, tx pg.Tx, site uuid.UUID, since, until time.Time) ([]sale, error) {
	rows, err := tx.Query(ctx, `SELECT currency, coalesce(rate_id, '00000000-0000-0000-0000-000000000000'), paid, fee, count(*)
FROM billing.subscription_grants
WHERE site_id = $1 AND source = 'paid' AND created_at >= $2 AND created_at < $3
GROUP BY 1, 2, 3, 4`, site, since, until)
	if err != nil {
		return nil, fmt.Errorf("billing: read the subscriptions sold: %w", err)
	}
	defer rows.Close()
	var sales []sale
	for rows.Next() {
		var s sale
		var code string
		if err := rows.Scan(&code, &s.rate, &s.payment.Paid, &s.payment.Fee, &s.count); err != nil {
			return nil, fmt.Errorf("billing: read the subscriptions sold: %w", err)
		}
		if s.payment.Currency, err = platform.ParseCurrency(code); err != nil {
			return nil, fmt.Errorf("billing: a grant's currency %q: %w", code, err)
		}
		sales = append(sales, s)
	}
	return sales, rows.Err()
}
