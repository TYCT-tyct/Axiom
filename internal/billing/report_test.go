package billing

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// TestPlatformReportReadsTheCallsOverTheWindow: revenue is what the window's calls were charged, cost is charged less
// the recorded profit, a call whose profit is unknown is counted apart rather than added as zero cost, only this site's
// settled calls count, and cash comes from the ledger's credits and refunds, not from the calls.
func TestPlatformReportReadsTheCallsOverTheWindow(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	site, account := b.admitted.Site, b.admitted.Account
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return start.Add(d) }
	holder := uuid.NewV7()

	reservation := func(request uuid.UUID, reservationSite uuid.UUID, state string, charged int64, profit any, created time.Time) string {
		window := start.Format(time.RFC3339)
		return fmt.Sprintf(`('%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', 1, 5, '%s', '%s', %d, %v, '%s')`,
			request, reservationSite, account, uuid.NewV7(), uuid.NewV7(), holder, window, window, state,
			created.Add(time.Hour).Format(time.RFC3339), charged, profit, created.Format(time.RFC3339))
	}
	settle, free, estimated := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	pending, outside, foreign := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	b.sql(
		fmt.Sprintf(`INSERT INTO billing.holders (id, expires_at) VALUES ('%s', '%s')`,
			holder, at(time.Hour).Format(time.RFC3339)),
		`INSERT INTO billing.reservations (request_id, site_id, account_id, workspace_id, key_id, holder_id,
			workspace_window, key_window, segment, amount, state, deadline, charged, profit, created_at) VALUES `+strings.Join([]string{
			reservation(settle, site, "settled", 900, 500, at(time.Hour)),
			reservation(free, site, "settled", 0, -400, at(2*time.Hour)),
			reservation(estimated, site, "cleanup_settled", 300, "NULL", at(3*time.Hour)),
			reservation(pending, site, "pending", 0, "NULL", at(4*time.Hour)),
			reservation(outside, site, "settled", 700, 200, at(-time.Hour)),
			reservation(foreign, uuid.NewV7(), "settled", 700, 200, at(time.Hour)),
		}, ", "),
		// Cash moves on its own rows: a paid credit in and a refund out, one of each outside the window or site.
		fmt.Sprintf(`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id, created_at) VALUES
			('%s', '%s', '%s', 'credit', '%s', '%s'),
			('%s', '%s', '%s', 'refund', '%s', '%s'),
			('%s', '%s', '%s', 'credit', '%s', '%s'),
			('%s', '%s', '%s', 'credit', '%s', '%s')`,
			uuid.NewV7(), site, account, uuid.NewV7(), at(5*time.Hour).Format(time.RFC3339),
			uuid.NewV7(), site, account, uuid.NewV7(), at(6*time.Hour).Format(time.RFC3339),
			uuid.NewV7(), site, account, uuid.NewV7(), at(-time.Hour).Format(time.RFC3339),
			uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), at(time.Hour).Format(time.RFC3339)),
		fmt.Sprintf(`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount)
			SELECT t.id, t.site_id, t.account_id, 'cash', CASE t.kind WHEN 'refund' THEN -300 ELSE 1100 END
			FROM billing.transactions t WHERE t.site_id = '%s'`, site),
	)

	var report PlatformReport
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) (err error) {
		report, err = ReadPlatformReport(t.Context(), tx, site, testCurrency(t), start, at(24*time.Hour))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if report.Revenue != "1200" || report.Cost != "800" || report.UnknownCost != 1 {
		t.Errorf("report = %+v, want revenue 1200, cost 800 and 1 unknown cost", report)
	}
	if len(report.Cash) != 1 || report.Cash[0].Amount != "800" || report.Cash[0].Currency.String() != "USD" {
		t.Errorf("cash = %+v, want 800 USD", report.Cash)
	}
}

// TestPlatformReportNamesCashInItsCurrency: a payment in another currency keeps it, the accounting currency's NULL
// becomes the site's, and a window without cash still shows a zero in the site's currency.
func TestPlatformReportNamesCashInItsCurrency(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	site, account := b.admitted.Site, b.admitted.Account
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id := uuid.NewV7()
	b.sql(
		fmt.Sprintf(`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id, created_at)
			VALUES ('%s', '%s', '%s', 'credit', '%s', '%s')`,
			id, site, account, uuid.NewV7(), start.Add(time.Hour).Format(time.RFC3339)),
		fmt.Sprintf(`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount, currency) VALUES
			('%s', '%s', '%s', 'cash', 700, 'CNY'),
			('%s', '%s', '%s', 'supplier_prepaid', -700, 'CNY')`, id, site, account, id, site, account),
	)
	read := func(since, until time.Time) PlatformReport {
		t.Helper()
		var report PlatformReport
		if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) (err error) {
			report, err = ReadPlatformReport(t.Context(), tx, site, testCurrency(t), since, until)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return report
	}
	report := read(start, start.Add(24*time.Hour))
	if len(report.Cash) != 1 || report.Cash[0].Amount != "700" || report.Cash[0].Currency.String() != "CNY" {
		t.Errorf("cash = %+v, want 700 CNY", report.Cash)
	}
	empty := read(start.Add(-48*time.Hour), start.Add(-24*time.Hour))
	if len(empty.Cash) != 1 || empty.Cash[0].Amount != "0" || empty.Cash[0].Currency.String() != "USD" {
		t.Errorf("cash without movement = %+v, want a zero in USD", empty.Cash)
	}
}

// TestPlatformReportSumsWiderThanInt64: two rows that each fit int64 sum to an amount that does not, and the report
// keeps every digit rather than failing the page or wrapping.
func TestPlatformReportSumsWiderThanInt64(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	site, account := b.admitted.Site, b.admitted.Account
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	holder := uuid.NewV7()
	const half = int64(5_000_000_000_000_000_000)
	row := func(id uuid.UUID, at time.Time) string {
		return fmt.Sprintf(`('%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', 1, %d, 'settled', '%s', %d, 0, '%s')`,
			id, site, account, uuid.NewV7(), uuid.NewV7(), holder, start.Format(time.RFC3339), start.Format(time.RFC3339),
			half, at.Add(time.Hour).Format(time.RFC3339), half, at.Format(time.RFC3339))
	}
	credit := func(id uuid.UUID, at time.Time) string {
		return fmt.Sprintf(`('%s', '%s', '%s', 'credit', '%s', '%s')`,
			id, site, account, uuid.NewV7(), at.Format(time.RFC3339))
	}
	b.sql(
		fmt.Sprintf(`INSERT INTO billing.holders (id, expires_at) VALUES ('%s', '%s')`,
			holder, start.Add(time.Hour).Format(time.RFC3339)),
		`INSERT INTO billing.reservations (request_id, site_id, account_id, workspace_id, key_id, holder_id,
			workspace_window, key_window, segment, amount, state, deadline, charged, profit, created_at) VALUES `+
			strings.Join([]string{row(uuid.NewV7(), start.Add(time.Hour)), row(uuid.NewV7(), start.Add(2*time.Hour))}, ", "),
		`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id, created_at) VALUES `+
			strings.Join([]string{credit(uuid.NewV7(), start.Add(3*time.Hour)), credit(uuid.NewV7(), start.Add(4*time.Hour))}, ", "),
		fmt.Sprintf(`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount)
			SELECT t.id, t.site_id, t.account_id, 'cash', %d FROM billing.transactions t WHERE t.site_id = '%s'`, half, site),
	)
	var report PlatformReport
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) (err error) {
		report, err = ReadPlatformReport(t.Context(), tx, site, testCurrency(t), start, start.Add(24*time.Hour))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	const sum = "10000000000000000000"
	if report.Revenue != sum || report.Cost != sum {
		t.Errorf("report = revenue %s, cost %s; want both sums wider than int64", report.Revenue, report.Cost)
	}
	if len(report.Cash) != 1 || report.Cash[0].Amount != sum {
		t.Errorf("cash = %+v, want %s USD", report.Cash, sum)
	}
}

// TestPlatformReportReadsPaidUsersAndRetention: a paid top-up is the commercial signal, not a gift and not a call. The
// window counts distinct paying accounts and the credit their paid batches granted; retention takes the previous UTC
// month's paying accounts and asks how many paid again from this month's first instant through the window's end. A
// full-month read then includes the payment the shorter window had not reached yet.
func TestPlatformReportReadsPaidUsersAndRetention(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	site := b.admitted.Site
	foreign := uuid.NewV7()
	paid := func(account uuid.UUID, created time.Time, amount int64, batchSite uuid.UUID) string {
		return fmt.Sprintf(`('%s', '%s', '%s', 'paid', %d, '%s', 'USD', %d, 0, 'bank')`,
			uuid.NewV7(), batchSite, account, amount, created.Format(time.RFC3339), amount)
	}
	a, bAccount, c, d := uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	rows := []string{
		paid(a, time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC), 1000, site),
		paid(a, time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC), 1000, site),
		paid(bAccount, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC), 500, site),
		paid(c, time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC), 2000, site),
		paid(d, time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), 700, site),
		paid(d, time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC), 700, site),
		paid(uuid.NewV7(), time.Date(2026, 2, 12, 0, 0, 0, 0, time.UTC), 900, foreign),
		paid(a, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), 1100, site),
	}
	rows = append(rows, fmt.Sprintf(`('%s', '%s', '%s', 'gift', 500, '%s', NULL, NULL, NULL, NULL)`,
		uuid.NewV7(), site, uuid.NewV7(), time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)))
	b.sql(`INSERT INTO billing.credit_batches (id, site_id, account_id, source, amount, created_at, currency, paid, fee, offline_channel) VALUES ` + strings.Join(rows, ", "))

	read := func(since, until time.Time) PlatformReport {
		t.Helper()
		var report PlatformReport
		if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) (err error) {
			report, err = ReadPlatformReport(t.Context(), tx, site, testCurrency(t), since, until)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return report
	}
	feb := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	partial := read(feb, time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC))
	if partial.PaidUsers != 2 || partial.MonthlyRevenue != "3000" {
		t.Errorf("partial window paid = %d, revenue = %s; want 2 and 3000", partial.PaidUsers, partial.MonthlyRevenue)
	}
	if partial.Retention.Month != feb || partial.Retention.Cohort != 3 || partial.Retention.Retained != 1 {
		t.Errorf("partial window retention = %+v; want February, cohort 3, retained 1", partial.Retention)
	}
	full := read(feb, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if full.PaidUsers != 3 || full.MonthlyRevenue != "3700" {
		t.Errorf("full window paid = %d, revenue = %s; want 3 and 3700", full.PaidUsers, full.MonthlyRevenue)
	}
	if full.Retention.Month != feb || full.Retention.Cohort != 3 || full.Retention.Retained != 2 {
		t.Errorf("full window retention = %+v; want February, cohort 3, retained 2", full.Retention)
	}
}

// TestPlatformPaidPlanReadsTheWindow: paid top-ups are read through the partial covering index, so the dashboard's
// commercial figures cost a range of this site's paid batches rather than a scan of every credit. The plan test runs
// with the index the billing migration builds, not one another module owns.
func TestPlatformPaidPlanReadsTheWindow(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	site := b.admitted.Site
	now := time.Now().UTC()
	b.sql(
		fmt.Sprintf(`INSERT INTO billing.credit_batches
			(id, site_id, account_id, source, amount, created_at, currency, paid, fee, offline_channel)
			SELECT uuidv7(),
				CASE WHEN n %% 60 = 0 THEN '%s'::uuid ELSE md5(n::text || 'site')::uuid END,
				uuidv7(), 'paid', 1000, now() - make_interval(secs => n * 10),
				'USD', 1000, 0, 'bank'
			FROM generate_series(1, 240000) n`, site),
		"ANALYZE billing.credit_batches",
	)
	since := now.Add(-10 * time.Minute)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	cohort := month.AddDate(0, -1, 0)
	blocks, plan := b.plan(b.platform, nil, platformPaid,
		since, site, cohort, now, cohort, month, month)
	if strings.Contains(plan, "Seq Scan") {
		t.Errorf("the paid scan reads a table:\n%s", plan)
	}
	if !strings.Contains(plan, "credit_batches_by_site_paid_created") {
		t.Errorf("the paid scan does not read the partial index:\n%s", plan)
	}
	// The cohort month is wider than the page window, so the range still has thousands of this site's batches. What
	// must not happen is the whole table being read; the index and no-seq-scan checks above rule that out.
	if blocks > 6000 {
		t.Errorf("the paid scan touched %d blocks, want at most 6000:\n%s", blocks, plan)
	}
}

// TestPlatformReportPlanReadsTheWindow: over a large ledger, the calls' sums read the site's time range through the
// reservations index and the cash reads only the credit and refund kinds, not the settlements' volume.
func TestPlatformReportPlanReadsTheWindow(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	site, account := b.admitted.Site, b.admitted.Account
	now := time.Now().UTC()
	holder := uuid.NewV7()
	b.sql(
		fmt.Sprintf(`INSERT INTO billing.holders (id, expires_at) VALUES ('%s', now() + interval '1 day')`, holder),
		fmt.Sprintf(`INSERT INTO billing.reservations (request_id, site_id, account_id, workspace_id, key_id, holder_id,
			workspace_window, key_window, segment, amount, state, deadline, charged, profit, created_at)
			SELECT uuidv7(), '%s', '%s', uuidv7(), uuidv7(), '%s', now(), now(), 1, 5, 'settled',
				now() + interval '1 hour', 1000, CASE WHEN n %% 1200 = 0 THEN NULL ELSE 500 END,
				now() - make_interval(secs => n / 4.0)
			FROM generate_series(1, 120000) n`, site, account, holder),
		fmt.Sprintf(`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id, created_at)
			SELECT uuidv7(), '%s', '%s', 'credit', uuidv7(), now() - make_interval(secs => n / 4.0)
			FROM generate_series(1, 50000) n`, site, account),
		fmt.Sprintf(`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount)
			SELECT id, site_id, account_id, 'cash', 1100 FROM billing.transactions WHERE site_id = '%s'`, site),
		"ANALYZE billing.reservations, billing.transactions, billing.entries",
	)

	since := now.Add(-10 * time.Minute)
	blocks, plan := b.plan(b.platform, nil, platformCallMoney, site, since, now)
	if strings.Contains(plan, "Seq Scan") {
		t.Errorf("the calls' sums scan a table:\n%s", plan)
	}
	if !strings.Contains(plan, "reservations_by_site_created") {
		t.Errorf("the calls' sums do not read the site's time index:\n%s", plan)
	}
	if blocks > 1500 {
		t.Errorf("the calls' sums touched %d blocks, want at most 1500:\n%s", blocks, plan)
	}

	_, plan = b.plan(b.platform, nil, platformCash, site, since, now)
	if strings.Contains(plan, "Seq Scan") {
		t.Errorf("the cash scan reads a table:\n%s", plan)
	}
	if !strings.Contains(plan, "transactions_by_site_kind_created") {
		t.Errorf("the cash scan does not read the site's kind and time index:\n%s", plan)
	}
	// No buffer bound: the per-transaction entry probe costs a few pages each, and what must not happen is the
	// whole entries table being read, which the plan above rules out.
}

// testCurrency is the currency the tests name.
func testCurrency(t *testing.T) platform.Currency {
	t.Helper()
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	return currency
}
