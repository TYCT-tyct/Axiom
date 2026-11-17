package billing

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/pg"
)

func TestStatementDailyCursor(t *testing.T) {
	t.Parallel()
	cursor := statementDailyCursor("2026-11-01", "Asia/Shanghai")
	day, err := ParseStatementDailyCursor(cursor, "Asia/Shanghai")
	if err != nil || day != "2026-11-01" {
		t.Fatalf("cursor = %q, %v", day, err)
	}
	for _, tc := range []struct{ cursor, zone string }{
		{cursor, "UTC"},
		{"nonsense", "UTC"},
		{"", "UTC"},
		{base64.RawURLEncoding.EncodeToString([]byte("2026-02-30|UTC")), "UTC"},
	} {
		if _, err := ParseStatementDailyCursor(tc.cursor, tc.zone); !errors.Is(err, ErrBadStatementCursor) {
			t.Errorf("cursor %q in %s: %v", tc.cursor, tc.zone, err)
		}
	}
}

func (b *bank) statementDaily(a Admitted, zone string, size int, before string) ([]StatementDay, *string) {
	b.t.Helper()
	var rows []StatementDay
	var next *string
	if err := b.app.ReadTx(b.t.Context(), a.Scope, func(tx pg.Tx) (err error) {
		rows, next, err = ListStatementDaily(b.t.Context(), tx, StatementDailyQuery{
			Account: a.Account, TimeZone: zone, PageSize: size, Before: before,
		})
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return rows, next
}

func TestStatementDailyReconciles(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	b.seedDailyHistory()
	b.sql("UPDATE billing.transactions SET created_at = '2026-11-01T12:00:00Z'")
	daily, next := b.statementDaily(b.admitted, "UTC", MaxStatementDays, "")
	balance := int64(2390)
	wantDay := []StatementDay{{Day: "2026-11-01", Spent: "210", ToppedUp: "1300", Gifted: "1500", Expired: "200", ClosingBalance: &balance}}
	if diff := cmp.Diff(wantDay, daily); diff != "" || next != nil {
		t.Fatalf("same-day totals (-want +got): %s, cursor %v", diff, next)
	}
	checkStatementDayChange(t, daily[0], balance)
	// Timestamp order deliberately disagrees with sequence, including across UTC and Shanghai midnight and US DST.
	rows, _ := b.statement(b.admitted, 200, nil)
	stamps := []string{"2026-11-02T15:59:59Z", "2026-11-01T16:00:00Z", "2026-11-02T16:00:00Z", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"}
	for i, row := range rows {
		stamp := stamps[i%len(stamps)]
		if row.Kind == StatementFreeze {
			stamp = "2026-11-10T00:00:00Z" // a hold-only day still appears, with zero totals
		}
		b.sql(fmt.Sprintf("UPDATE billing.transactions SET created_at = '%s' WHERE id = '%s'", stamp, row.ID))
	}
	// An otherwise empty day containing a waived call must stay absent.
	b.sql(fmt.Sprintf("INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id, created_at) VALUES ('%s', '%s', '%s', 'settle', '%s', '2026-10-20')", uuid.NewV7(), b.admitted.Site, b.admitted.Account, uuid.NewV7()))
	rows, _ = b.statement(b.admitted, 200, nil)
	for _, zone := range []string{"UTC", "Asia/Shanghai", "America/New_York"} {
		got, next := b.statementDaily(b.admitted, zone, MaxStatementDays, "")
		want := statementDaysFromRows(t, rows, zone)
		if diff := cmp.Diff(want, got); diff != "" || next != nil {
			t.Errorf("%s daily statement (-want +got): %s, cursor %v", zone, diff, next)
		}
	}
	other := b.accountWithCredit()
	other.Account = b.admitted.Account // query the victim's ID under another account's RLS scope
	if rows, next := b.statementDaily(other, "UTC", MaxStatementDays, ""); len(rows) != 0 || next != nil {
		t.Fatalf("another account read %+v, %v", rows, next)
	}
}

func (b *bank) seedDailyHistory() {
	b.t.Helper()
	b.credit(1000)
	f := b.refundable(1000)
	b.creditBatch(200, longAgo)
	b.expire()
	if err := b.redeem(RedeemCredit{ID: uuid.NewV7(), Amount: 300}); err != nil {
		b.t.Fatal(err)
	}
	b.charge(250)
	f.Amount = 150
	b.freeze(f)
	f.Amount = 50
	b.finishRefund(f, false, true)
	f.Amount = 100
	b.finishRefund(f, false, false)
	p := b.paidBatch(400, 400)
	back, err := b.chargeback(p.ID, 400)
	if err != nil {
		b.t.Fatal(err)
	}
	if err := b.reverseChargeback(back); err != nil {
		b.t.Fatal(err)
	}
	b.reverseStatementCharge(40)
}

// The reference consumes the public statement's kinds, signed amounts and balances, independently of the SQL mapping.
func statementDaysFromRows(t *testing.T, rows []StatementRow, zone string) []StatementDay {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	days := map[string]*StatementDay{}
	for _, row := range rows {
		day := row.At.In(loc).Format(time.DateOnly)
		d := days[day]
		if d == nil {
			d = &StatementDay{Day: day, Spent: "0", ToppedUp: "0", Gifted: "0", Expired: "0", ClosingBalance: row.Balance}
			days[day] = d
		}
		switch row.Kind {
		case StatementSpend, StatementReversal:
			d.Spent = addStatementAmount(d.Spent, row.Amount, true)
		case StatementTopUp, StatementRefund, StatementChargeback, StatementChargebackReversal:
			d.ToppedUp = addStatementAmount(d.ToppedUp, row.Amount, false)
		case StatementGift, StatementRedeem:
			d.Gifted = addStatementAmount(d.Gifted, row.Amount, false)
		case StatementExpiry:
			d.Expired = addStatementAmount(d.Expired, row.Amount, true)
		case StatementFreeze, StatementUnfreeze:
		default:
			t.Fatalf("unmapped statement kind %q", row.Kind)
		}
	}
	out := make([]StatementDay, 0, len(days))
	for _, d := range days {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day > out[j].Day })
	return out
}

func addStatementAmount(total string, amount int64, debit bool) string {
	n, _ := new(big.Int).SetString(total, 10)
	if debit {
		return n.Sub(n, big.NewInt(amount)).String()
	}
	return n.Add(n, big.NewInt(amount)).String()
}

func TestStatementDailyPages(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	for range 35 {
		b.credit(10)
	}
	rows, _ := b.statement(b.admitted, 200, nil)
	for i, row := range rows {
		day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i*2).Format(time.DateOnly)
		b.sql(fmt.Sprintf("UPDATE billing.transactions SET created_at = '%s' WHERE id = '%s'", day, row.ID))
	}
	rows, _ = b.statement(b.admitted, 200, nil)
	want := statementDaysFromRows(t, rows, "UTC")
	for _, size := range []int{1, 3, MaxStatementDays} {
		all := b.statementDailyPages(size)
		checkStatementDailyChanges(t, all)
		if diff := cmp.Diff(want, all); diff != "" {
			t.Errorf("page size %d (-want +got): %s", size, diff)
		}
	}
}

func (b *bank) statementDailyPages(size int) []StatementDay {
	b.t.Helper()
	var all []StatementDay
	before := ""
	for page := 0; ; page++ {
		if page >= 35 {
			b.t.Fatal("daily cursor did not advance")
		}
		got, next := b.statementDaily(b.admitted, "UTC", size, before)
		if len(got) > size {
			b.t.Fatalf("page of %d holds %d days", size, len(got))
		}
		all = append(all, got...)
		if next == nil {
			break
		}
		if size == MaxStatementDays && page == 0 {
			b.credit(20) // a newly posted day must not shift the remaining historical pages
		}
		var err error
		before, err = ParseStatementDailyCursor(*next, "UTC")
		if err != nil {
			b.t.Fatal(err)
		}
	}
	return all
}

func TestStatementDailyLegacyBalance(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, 0, Config{})
	b.charge(200)
	b.sql("UPDATE billing.transactions SET seq = NULL, created_at = '2026-01-01'")
	b.charge(70)
	b.sql("UPDATE billing.transactions SET created_at = '2026-01-02' WHERE seq IS NOT NULL")
	got, next := b.statementDaily(b.admitted, "UTC", 1, "")
	balance := int64(730)
	want := []StatementDay{{Day: "2026-01-02", Spent: "70", ToppedUp: "0", Gifted: "0", Expired: "0", ClosingBalance: &balance}}
	if diff := cmp.Diff(want, got); diff != "" || next == nil {
		t.Fatalf("numbered day (-want +got): %s, %v", diff, next)
	}
	before, err := ParseStatementDailyCursor(*next, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	got, next = b.statementDaily(b.admitted, "UTC", 1, before)
	want = []StatementDay{{Day: "2026-01-01", Spent: "200", ToppedUp: "0", Gifted: "1000", Expired: "0"}}
	if diff := cmp.Diff(want, got); diff != "" || next != nil {
		t.Fatalf("legacy day (-want +got): %s, %v", diff, next)
	}
	b.sql("UPDATE billing.transactions SET created_at = '2026-01-01' WHERE seq IS NOT NULL")
	got, _ = b.statementDaily(b.admitted, "UTC", 1, "")
	want[0].Spent, want[0].ClosingBalance = "270", &balance
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("mixed legacy and numbered day (-want +got): %s", diff)
	}
}

func TestStatementDailyWideTotals(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	b.creditBatch(math.MaxInt64, longAgo)
	b.expire()
	b.credit(math.MaxInt64)
	b.sql("UPDATE billing.transactions SET created_at = '2026-01-01'")
	got, _ := b.statementDaily(b.admitted, "UTC", MaxStatementDays, "")
	if len(got) != 1 || got[0].Gifted != "18446744073709551614" || got[0].Expired != "9223372036854775807" || got[0].ClosingBalance == nil || *got[0].ClosingBalance != math.MaxInt64 {
		t.Fatalf("wide daily totals = %+v", got)
	}
}

// This fixture starts at zero and keeps one day's ledger in commit order, so net movements must equal its closing balance.
func checkStatementDayChange(t *testing.T, day StatementDay, change int64) {
	t.Helper()
	net := new(big.Int)
	for _, amount := range []string{day.ToppedUp, day.Gifted} {
		n, _ := new(big.Int).SetString(amount, 10)
		net.Add(net, n)
	}
	for _, amount := range []string{day.Spent, day.Expired} {
		n, _ := new(big.Int).SetString(amount, 10)
		net.Sub(net, n)
	}
	if net.Cmp(big.NewInt(change)) != 0 {
		t.Fatalf("daily movements %s differ from balance change %d", net, change)
	}
}

func TestStatementDailyBudget(t *testing.T) {
	t.Parallel()
	b := newBank(t, 10, 0, Config{})
	seed := fmt.Sprintf("INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id) SELECT uuidv7(), '%s', '%s', 'settle', uuidv7() FROM generate_series(1, %%d)", b.admitted.Site, b.admitted.Account)
	b.sql(fmt.Sprintf(seed, MaxStatementTransactions-1))
	b.statementDaily(b.admitted, "UTC", 31, "") // exactly the budget, including zero-effect transactions
	b.sql(fmt.Sprintf(seed, 1))
	b.checkDailyBudgetRefusal()
	b.sql(fmt.Sprintf(seed, 50000), "ANALYZE billing.transactions")
	b.checkDailyBudgetRefusal()
	blocks, plan := b.plan(b.admitted.Scope, func(tx pg.Tx) error { return planLedgerPage(t.Context(), tx) }, statementDailyBudget, b.admitted.Account, int32(MaxStatementTransactions+1))
	checkTransactionIndexPlan(t, plan, 1, "Forward")
	if !strings.Contains(plan, "transactions_by_account_seq") || blocks > 2*MaxStatementTransactions+100 {
		t.Fatalf("daily budget touched %d blocks without a bounded index scan: %s", blocks, plan)
	}
}

func (b *bank) checkDailyBudgetRefusal() {
	b.t.Helper()
	err := b.app.ReadTx(b.t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		rows, next, err := ListStatementDaily(b.t.Context(), tx, StatementDailyQuery{Account: b.admitted.Account, TimeZone: "UTC", PageSize: 31})
		if len(rows) != 0 || next != nil {
			b.t.Fatal("a budget refusal returned partial totals")
		}
		return err
	})
	if !errors.Is(err, ErrStatementDailySize) {
		b.t.Fatalf("oversized statement: %v", err)
	}
}

func TestStatementDailyRejectsUnknownMovement(t *testing.T) {
	t.Parallel()
	b := newBank(t, 10, 0, Config{})
	id := uuid.NewV7()
	// attempt_cost normally touches only internal books. Simulate a new balance movement without a statement mapping.
	b.sql(fmt.Sprintf("INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id) VALUES ('%s', '%s', '%s', 'attempt_cost', '%s')", id, b.admitted.Site, b.admitted.Account, uuid.NewV7()),
		fmt.Sprintf("INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount) VALUES ('%s', '%s', '%s', 'user_balance', 1), ('%s', '%s', '%s', 'revenue', -1)", id, b.admitted.Site, b.admitted.Account, id, b.admitted.Site, b.admitted.Account),
		"UPDATE billing.balances SET balance = balance - 1")
	err := b.app.ReadTx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		_, _, err := ListStatementDaily(t.Context(), tx, StatementDailyQuery{Account: b.admitted.Account, TimeZone: "UTC", PageSize: 31})
		return err
	})
	if err == nil {
		t.Fatal("an unmapped balance movement was silently omitted")
	}
}

func TestStatementDayRejectsUnknownCategory(t *testing.T) {
	t.Parallel()
	day := StatementDay{}
	if err := day.add("new_kind", "1"); err == nil {
		t.Fatal("an unknown statement kind was silently omitted")
	}
}

// Paging fixtures stamp movements in commit order, so every nonempty day's net change also bridges its known balances.
func checkStatementDailyChanges(t *testing.T, days []StatementDay) {
	t.Helper()
	var opening int64
	for _, day := range slices.Backward(days) {
		if day.ClosingBalance == nil {
			t.Fatal("numbered history lost its closing balance")
		}
		checkStatementDayChange(t, day, *day.ClosingBalance-opening)
		opening = *day.ClosingBalance
	}
}
