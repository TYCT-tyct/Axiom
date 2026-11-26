package billing

import (
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
)

// seedPlan adds a plan of site in SQL, for the admin reads, which need many rows and no ledger.
func seedPlan(site uuid.UUID) (uuid.UUID, string) {
	id := uuid.NewV7()
	return id, fmt.Sprintf(`INSERT INTO billing.subscription_plans (id, site_id, name, currency, price, quota, window_seconds, term_seconds)
		VALUES ('%s', '%s', 'Plan %s', 'USD', 2000, 1000, 3600, 7200)`, id, site, id)
}

// seedSubscriptions adds n subscriptions of plan on site, one account each, with their first window: created and
// ending at created + i seconds and ends + i seconds.
func seedSubscriptions(site, plan uuid.UUID, n int, state, created, ends string) string {
	return fmt.Sprintf(`WITH s AS (
		INSERT INTO billing.subscriptions (id, site_id, account_id, plan_id, plan_name, starts_at, ends_at, state, closed_at, created_at)
		SELECT uuidv7(), '%[1]s', uuidv7(), '%[2]s', 'Plan', %[5]s - interval '1 day', %[5]s + i * interval '1 second', '%[3]s',
			CASE WHEN '%[3]s' = 'expired' THEN %[5]s END, %[4]s + i * interval '1 second'
		FROM generate_series(1, %[6]d) i RETURNING id, site_id, account_id, starts_at, ends_at)
		INSERT INTO billing.subscription_windows (subscription_id, seq, site_id, account_id, starts_at, ends_at, quota)
		SELECT id, 1, site_id, account_id, starts_at, ends_at, 1000 FROM s`, site, plan, state, created, ends, n)
}

// seedGrant adds a grant of plan on site created at at: paid in currency with fee and rate, or a gift when currency is "".
func seedGrant(site, plan uuid.UUID, at time.Time, currency string, paid, fee int64, rate string) string {
	payment := "NULL, NULL, NULL, NULL, 'gift'"
	if currency != "" {
		payment = fmt.Sprintf("'%s', %d, %d, '%s', 'paid'", currency, paid, fee, "bank")
	}
	rateID := "NULL"
	if rate != "" {
		rateID = "'" + rate + "'"
	}
	return fmt.Sprintf(`INSERT INTO billing.subscription_grants (id, site_id, account_id, subscription_id, plan_id, plan_name,
		quota, window_seconds, term_seconds, windows, face, currency, paid, fee, offline_channel, source, rate_id, created_at)
		VALUES (uuidv7(), '%s', uuidv7(), uuidv7(), '%s', 'Plan', 1000, 3600, 7200, 2, 2000, %s, %s, '%s')`,
		site, plan, payment, rateID, at.Format(time.RFC3339Nano))
}

// TestSubscriptionSummaryCountsTheSiteAndItsMonth: the counts are the site's active subscriptions, the shares add up to
// 10000, and a month's revenue is its paid grants' value in the site's currency, at each grant's rate, with a gift,
// another site and the month's edges left out.
func TestSubscriptionSummaryCountsTheSiteAndItsMonth(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	site, other := b.admitted.Site, uuid.NewV7()
	planA, sqlA := seedPlan(site)
	planB, sqlB := seedPlan(site)
	planC, sqlC := seedPlan(site)
	planX, sqlX := seedPlan(other)
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	previous, next := month.AddDate(0, -1, 0), month.AddDate(0, 1, 0)
	rate := uuid.NewV7()
	b.sql(sqlA, sqlB, sqlC, sqlX,
		seedSubscriptions(site, planA, 1, "active", "now()", "now() + interval '2 days'"),
		seedSubscriptions(site, planB, 1, "active", "now()", "now() + interval '30 days'"),
		seedSubscriptions(site, planC, 1, "active", "now()", "now() + interval '30 days'"),
		seedSubscriptions(site, planA, 2, "expired", "now() - interval '60 days'", "now() - interval '30 days'"),
		seedSubscriptions(other, planX, 1, "active", "now()", "now() + interval '1 day'"),
		fmt.Sprintf(`INSERT INTO billing.exchange_rates (id, base, quote, version, numerator, denominator, kind, reason, created_by)
			VALUES ('%s', 'CNY', 'USD', 1, 1, 7, 'initial', 'test', 'test')`, rate),
		seedGrant(site, planA, month, "USD", 2000, 100, ""),                       // the month's first instant
		seedGrant(site, planA, next.Add(-time.Microsecond), "USD", 2000, 100, ""), // the same payment again
		seedGrant(site, planB, month.Add(time.Hour), "CNY", 7000, 0, rate.String()),
		seedGrant(site, planA, month.Add(time.Hour), "", 0, 0, ""), // a gift
		seedGrant(other, planX, month.Add(time.Hour), "USD", 5000, 0, ""),
		seedGrant(site, planA, previous, "USD", 1000, 0, ""),
		seedGrant(site, planA, previous.AddDate(0, -1, 0), "USD", 9000, 0, ""),
		seedGrant(site, planA, next, "USD", 9000, 0, ""),
	)
	var cnyRate *BillingExchangeRate
	if err := b.app.ReadTx(t.Context(), b.platform, func(tx pg.Tx) (err error) { cnyRate, err = readRate(t.Context(), tx, rate); return err }); err != nil {
		t.Fatal(err)
	}
	cny, err := platform.ParseCurrency("CNY")
	if err != nil {
		t.Fatal(err)
	}
	cnyValue, err := CreditsFor(Payment{Currency: cny, Paid: 7000}, usd, cnyRate)
	if err != nil {
		t.Fatal(err)
	}
	summary := func(m time.Time) SubscriptionSummary {
		var s SubscriptionSummary
		if err := b.app.ReadTx(t.Context(), b.platform, func(tx pg.Tx) (err error) {
			s, err = ReadSubscriptionSummary(t.Context(), tx, site, usd, m)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	current := summary(time.Time{})
	usdSale := (2000 - 100) * int64(10_000_000) // cents less the fee, in billionths of a dollar
	for _, c := range []struct {
		what      string
		got, want string
	}{
		{"month", current.Month.String(), month.String()},
		{"active", fmt.Sprint(current.Active), "3"},
		{"ending within 7 days", fmt.Sprint(current.EndingSoon), "1"},
		{"revenue", current.Revenue.String(), big.NewInt(2*usdSale + cnyValue).String()},
		{"previous revenue", current.PreviousRevenue.String(), "10000000000"},
		{"plans", fmt.Sprint(current.Plans), fmt.Sprint(sharesOf(planA, planB, planC))},
	} {
		if c.got != c.want {
			t.Errorf("%s = %s, want %s", c.what, c.got, c.want)
		}
	}
	if before := summary(previous.Add(time.Hour)); before.Revenue.String() != "10000000000" || before.PreviousRevenue.String() != "90000000000" {
		t.Errorf("the month before: revenue %s, previous %s", before.Revenue, before.PreviousRevenue)
	}
	if empty := summary(month.AddDate(-5, 0, 0)); empty.Revenue.Sign() != 0 || empty.Active != 3 {
		t.Errorf("a month without sales: %+v", empty)
	}
}

// sharesOf is three plans with one active subscription each: the largest remainder gives the first by ID the extra
// basis point, and the three add up to 10000.
func sharesOf(plans ...uuid.UUID) []PlanShare {
	ids := append([]uuid.UUID(nil), plans...)
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			if strings.Compare(ids[j].String(), ids[i].String()) < 0 {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	return []PlanShare{{ids[0], 1, 3334}, {ids[1], 1, 3333}, {ids[2], 1, 3333}}
}

// listSubscriptions reads one page of q.
func (b *bank) listSubscriptions(q SubscriptionQuery) ([]Subscription, *page.Cursor) {
	b.t.Helper()
	var subs []Subscription
	var next *page.Cursor
	if err := b.app.ReadTx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		subs, next, err = ListAllSubscriptions(b.t.Context(), tx, q)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return subs, next
}

// allSubscriptions reads every page of q.
func (b *bank) allSubscriptions(q SubscriptionQuery) []Subscription {
	var all []Subscription
	for {
		subs, next := b.listSubscriptions(q)
		all = append(all, subs...)
		if next == nil {
			return all
		}
		q.After = next
	}
}

// follows reports whether c comes right after a in q's order: newest first, or soonest to end first.
func follows(byEnd bool, a, c Subscription) bool {
	later := strings.Compare(c.ID.String(), a.ID.String())
	if byEnd {
		return c.EndsAt.After(a.EndsAt) || c.EndsAt.Equal(a.EndsAt) && later > 0
	}
	return c.CreatedAt.Before(a.CreatedAt) || c.CreatedAt.Equal(a.CreatedAt) && later < 0
}

// TestListAllSubscriptionsPagesThroughItsIndex: both orders page without a gap or a repeat, the filters are exact, and
// with thousands of rows each order reads its own index instead of scanning and sorting the table.
func TestListAllSubscriptionsPagesThroughItsIndex(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	site := b.admitted.Site
	plan, planSQL := seedPlan(site)
	other, otherSQL := seedPlan(site)
	b.sql(planSQL, otherSQL,
		seedSubscriptions(site, plan, 3000, "expired", "now() - interval '90 days'", "now() - interval '30 days'"),
		seedSubscriptions(site, plan, 4, "active", "now()", "now() + interval '1 day'"),
		seedSubscriptions(site, other, 3, "active", "now() + interval '1 hour'", "now() + interval '5 days'"),
		"ANALYZE billing.subscriptions", "ANALYZE billing.subscription_windows",
	)
	for _, c := range []struct {
		byEnd bool
		index string
	}{{false, "subscriptions_created"}, {true, "subscriptions_ending"}} {
		seen := b.allSubscriptions(SubscriptionQuery{State: "active", ByEnd: c.byEnd, Limit: 3})
		if len(seen) != 7 {
			t.Fatalf("by end %v: %d subscriptions, want 7", c.byEnd, len(seen))
		}
		for i := 1; i < len(seen); i++ {
			if s := seen[i]; !follows(c.byEnd, seen[i-1], s) || s.State != "active" || s.Window.Seq != 1 || s.Window.Quota != 1000 {
				t.Fatalf("by end %v, row %d: %+v after %+v", c.byEnd, i, s, seen[i-1])
			}
		}
		sql, args := SubscriptionQuery{ByEnd: c.byEnd, Limit: 50}.sql()
		if _, plan := b.plan(b.platform, nil, sql, args...); !strings.Contains(plan, c.index) || strings.Contains(plan, `"Node Type": "Sort"`) {
			t.Errorf("by end %v does not read %s: %s", c.byEnd, c.index, plan)
		}
	}
	soon := time.Now().Add(48 * time.Hour)
	one, _ := b.listSubscriptions(SubscriptionQuery{Limit: 1})
	for _, c := range []struct {
		what string
		q    SubscriptionQuery
		want int
	}{
		{"plan", SubscriptionQuery{Plan: &other, Limit: 10}, 3},
		{"ends_before", SubscriptionQuery{State: "active", EndsBefore: &soon, Limit: 10}, 4},
		{"account", SubscriptionQuery{Account: &one[0].Account, Limit: 10}, 1},
	} {
		if got := b.allSubscriptions(c.q); len(got) != c.want {
			t.Errorf("%s filter: %d subscriptions, want %d", c.what, len(got), c.want)
		}
	}
}
