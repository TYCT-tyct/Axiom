package main

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/adminapi"
)

// TestAdminPlatformSubscriptionsAndAnnouncements: the three reads answer the owner, refuse a role without their
// permission, and refuse a malformed query before reading anything.
func TestAdminPlatformSubscriptionsAndAnnouncements(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	f.addUser("member@example.com", "member") // a platform role with neither users.view nor sites.manage
	member := f.verified("member@example.com")
	main, _, err := f.svc.SiteForHost(adminHost)
	if err != nil {
		t.Fatal(err)
	}
	site := uuid.UUID(main.ID).String()
	plan := uuid.NewV7()
	now := time.Now().UTC()
	f.run(
		fmt.Sprintf(`INSERT INTO billing.subscription_plans (id, site_id, name, currency, price, quota, window_seconds, term_seconds)
			VALUES ('%s', '%s', 'Monthly', '%s', 2000, 1000, 3600, 7200)`, plan, site, main.Currency),
		fmt.Sprintf(`WITH s AS (INSERT INTO billing.subscriptions (id, site_id, account_id, plan_id, plan_name, starts_at, ends_at)
			SELECT uuidv7(), '%[1]s', uuidv7(), '%[2]s', 'Monthly', now(), now() + i * interval '1 day' FROM generate_series(1, 3) i
			RETURNING id, site_id, account_id, starts_at, ends_at)
			INSERT INTO billing.subscription_windows (subscription_id, seq, site_id, account_id, starts_at, ends_at, quota)
			SELECT id, 1, site_id, account_id, starts_at, ends_at, 1000 FROM s`, site, plan),
		fmt.Sprintf(`INSERT INTO billing.subscription_grants (id, site_id, account_id, subscription_id, plan_id, plan_name, quota,
			window_seconds, term_seconds, windows, face, currency, paid, fee, offline_channel, source)
			VALUES (uuidv7(), '%s', uuidv7(), uuidv7(), '%s', 'Monthly', 1000, 3600, 7200, 2, 2000, '%s', 2000, 0, 'bank', 'paid')`,
			site, plan, main.Currency),
		fmt.Sprintf(`INSERT INTO accounts.announcements (id, site_id, title, body, starts_at, ends_at, withdrawn_at) VALUES
			(uuidv7(), '%[1]s', 'later', 'x', now() + interval '1 hour', now() + interval '2 hours', NULL),
			(uuidv7(), '%[1]s', 'gone', 'x', now() - interval '1 hour', now() + interval '1 hour', now())`, site),
	)

	checkSubscriptionPages(t, f, owner)

	rec := f.do(http.MethodGet, "/subscriptions/summary", "", as(owner))
	expectAdmin(t, "summary", rec, 200, "")
	var summary adminapi.SubscriptionSummary
	decode(t, rec, &summary)
	paid := fmt.Sprint(2000 * 10_000_000) // 2000 minor units in billionths
	want := adminapi.SubscriptionSummary{
		Month: now.Format("2006-01"), Active: 3, EndingWithin7Days: 3,
		Revenue:         adminapi.Money{Amount: paid, Currency: main.Currency.String()},
		PreviousRevenue: adminapi.Money{Amount: "0", Currency: main.Currency.String()},
		Plans:           []adminapi.SubscriptionPlanShare{{PlanId: plan.String(), Active: 3, ShareBp: 10000}},
	}
	if fmt.Sprint(summary) != fmt.Sprint(want) {
		t.Fatalf("summary: %s", rec.Body)
	}

	var list adminapi.AdminAnnouncementList
	for _, c := range []struct {
		query string
		want  []string // each announcement's title and state, newest first
	}{
		{"?site_id=" + url.QueryEscape(main.ID.TypeID()), []string{"later scheduled", "gone withdrawn"}},
		{"?state=withdrawn", []string{"gone withdrawn"}},
	} {
		rec = f.do(http.MethodGet, "/announcements"+c.query, "", as(owner))
		expectAdmin(t, "announcements"+c.query, rec, 200, "")
		decode(t, rec, &list)
		got := make([]string, len(list.Data))
		for i, a := range list.Data {
			got[i] = a.Title + " " + string(a.State)
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) || (list.Data[len(list.Data)-1].WithdrawnAt == nil) {
			t.Fatalf("announcements%s: %s", c.query, rec.Body)
		}
	}

	for _, c := range []struct {
		what, path string
		as         session
		status     int
	}{
		{"subscriptions without users.view", "/subscriptions", member, 403},
		{"summary without sites.manage", "/subscriptions/summary", member, 403},
		{"announcements without sites.manage", "/announcements", member, 403},
		{"a malformed cursor", "/subscriptions?cursor=not-a-cursor", owner, 400},
		{"an unknown sort", "/subscriptions?sort=name", owner, 400},
		{"a malformed account", "/subscriptions?account_id=acct_x", owner, 400},
		{"a malformed plan", "/subscriptions?plan_id=nope", owner, 400},
		{"a malformed month", "/subscriptions/summary?month=2026-13", owner, 400},
		{"an unknown state", "/announcements?state=draft", owner, 400},
		{"a malformed site", "/announcements?site_id=site_x", owner, 400},
		{"a malformed announcement cursor", "/announcements?cursor=x", owner, 400},
	} {
		expectAdmin(t, c.what, f.do(http.MethodGet, c.path, "", as(c.as)), c.status, "")
	}
}

// checkSubscriptionPages reads the three subscriptions soonest to end first, two to a page: the first page has the
// two that end first and a cursor, the last the third and none.
func checkSubscriptionPages(t *testing.T, f adminFixture, owner session) {
	t.Helper()
	rec := f.do(http.MethodGet, "/subscriptions?sort=ends_at&limit=2", "", as(owner))
	expectAdmin(t, "subscriptions", rec, 200, "")
	var subs adminapi.SubscriptionPage
	decode(t, rec, &subs)
	if len(subs.Data) != 2 || subs.NextCursor == nil || !subs.Data[0].EndsAt.Before(subs.Data[1].EndsAt) {
		t.Fatalf("first page: %s", rec.Body)
	}
	rec = f.do(http.MethodGet, "/subscriptions?sort=ends_at&limit=2&cursor="+url.QueryEscape(*subs.NextCursor), "", as(owner))
	decode(t, rec, &subs)
	if len(subs.Data) != 1 || subs.NextCursor != nil {
		t.Fatalf("last page: %s", rec.Body)
	}
}
