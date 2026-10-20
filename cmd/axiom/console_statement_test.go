package main

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"axiom/internal/billing"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

// statementRows is a page of the account's statement as the contract gives it.
func (f consoleFixture) statementRows(s session, query string) consoleapi.Statement {
	f.t.Helper()
	var page consoleapi.Statement
	decode(f.t, f.do(http.MethodGet, "/statement"+query, "", as(s)), &page)
	return page
}

// TestStatement: the statement lists the account's own movements newest first, each with the balance it left, under the
// kind it was and the call or the batch it was for; a page ends where the next one starts.
func TestStatement(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	account, site := f.account(s)
	gift := uuid.NewV7()
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return billing.AddCredit(t.Context(), tx, billing.Credit{
			ID: gift, Site: site, Account: account, Amount: 5_000_000_000, Actor: audit.System("test"), Reason: "a gift",
		})
	}); err != nil {
		t.Fatal(err)
	}
	call := uuid.NewV7()
	f.run(`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id) VALUES
			('`+uuid.NewV7().String()+`', '`+site.String()+`', '`+account.String()+`', 'settle', '`+call.String()+`')`,
		`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount) VALUES
			((SELECT id FROM billing.transactions WHERE request_id = '`+call.String()+`'), '`+site.String()+`', '`+account.String()+`', 'user_balance', -1000),
			((SELECT id FROM billing.transactions WHERE request_id = '`+call.String()+`'), '`+site.String()+`', '`+account.String()+`', 'revenue', 1000)`)
	page := f.statementRows(s, "?limit=1")
	if len(page.Data) != 1 || page.NextCursor == nil {
		t.Fatalf("%d rows and the cursor %v, want one and one", len(page.Data), page.NextCursor)
	}
	want := consoleapi.StatementRow{
		Id: page.Data[0].Id, At: page.Data[0].At, Kind: consoleapi.Spend, Reference: call.String(),
		Amount: consoleapi.Money{Amount: "-1000", Currency: "USD"}, BalanceAfter: consoleapi.Money{Amount: "4999999000", Currency: "USD"},
	}
	if diff := cmp.Diff(want, page.Data[0], cmpopts.EquateApproxTime(time.Millisecond)); diff != "" {
		t.Errorf("a charge (-want +got):\n%s", diff)
	}
	if _, err := uuid.Parse(page.Data[0].Id); err != nil {
		t.Errorf("the row's ID %q is not one", page.Data[0].Id)
	}
	rest := f.statementRows(s, "?limit=5&cursor="+*page.NextCursor)
	if len(rest.Data) != 1 || rest.NextCursor != nil || rest.Data[0].Kind != consoleapi.Gift || rest.Data[0].Reference != gift.String() {
		t.Fatalf("the last page: %+v, cursor %v", rest.Data, rest.NextCursor)
	}
	if rest.Data[0].Amount != (consoleapi.Money{Amount: "5000000000", Currency: "USD"}) || rest.Data[0].BalanceAfter != rest.Data[0].Amount {
		t.Errorf("the gift: %+v", rest.Data[0])
	}
	expect(t, "a cursor that is none", f.do(http.MethodGet, "/statement?cursor=nonsense", "", as(s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
}

// TestStatementCSV: the file is the statement's rows under a header, newest first, with the amounts as exact decimals of
// the site's currency and the time in UTC, and it is a download.
func TestStatementCSV(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	account, site := f.account(s)
	gift := uuid.NewV7()
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return billing.AddCredit(t.Context(), tx, billing.Credit{
			ID: gift, Site: site, Account: account, Amount: 1_500_000_000, Actor: audit.System("test"), Reason: "a gift",
		})
	}); err != nil {
		t.Fatal(err)
	}
	call := uuid.NewV7()
	f.run(`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id) VALUES
			('`+uuid.NewV7().String()+`', '`+site.String()+`', '`+account.String()+`', 'settle', '`+call.String()+`')`,
		`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount) VALUES
			((SELECT id FROM billing.transactions WHERE request_id = '`+call.String()+`'), '`+site.String()+`', '`+account.String()+`', 'user_balance', -250000000),
			((SELECT id FROM billing.transactions WHERE request_id = '`+call.String()+`'), '`+site.String()+`', '`+account.String()+`', 'revenue', 250000000)`)
	rec := f.do(http.MethodGet, "/statement.csv", "", as(s))
	if rec.Code != http.StatusOK {
		t.Fatalf("the export: %d %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/csv" {
		t.Errorf("the export is %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, `attachment; filename="statement-`) || !strings.HasSuffix(got, `.csv"`) {
		t.Errorf("the export is named %q", got)
	}
	lines, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"at", "kind", "amount", "balance_after", "currency", "reference"},
		{"", "spend", "-0.250000000", "1.250000000", "USD", call.String()},
		{"", "gift", "1.500000000", "1.500000000", "USD", gift.String()},
	}
	if len(lines) != len(want) {
		t.Fatalf("%d lines, want %d: %v", len(lines), len(want), lines)
	}
	for i, w := range want {
		got := lines[i]
		if i > 0 {
			if _, err := time.Parse(time.RFC3339, got[0]); err != nil {
				t.Errorf("line %d's time %q: %v", i, got[0], err)
			}
			got = append([]string{""}, got[1:]...)
			w = append([]string{""}, w[1:]...)
		}
		if diff := cmp.Diff(w, got); diff != "" {
			t.Errorf("line %d (-want +got):\n%s", i, diff)
		}
	}
}

// TestStatementExportIsCapped: the file holds the newest statementExport movements of an account with more, and the
// pages still reach the rest.
func TestStatementExportIsCapped(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	account, site := f.account(s)
	const many = statementExport + 50
	f.run(`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id)
			SELECT gen_random_uuid(), '`+site.String()+`', '`+account.String()+`', 'settle', gen_random_uuid()
			FROM generate_series(1, `+strconv.Itoa(many)+`)`,
		`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount)
			SELECT t.id, t.site_id, t.account_id, b.book, b.amount FROM billing.transactions t, (VALUES ('user_balance', -1), ('revenue', 1)) AS b(book, amount)
			WHERE t.account_id = '`+account.String()+`'`)
	rec := f.do(http.MethodGet, "/statement.csv", "", as(s))
	if rec.Code != http.StatusOK {
		t.Fatalf("the export: %d %s", rec.Code, rec.Body)
	}
	if got := strings.Count(rec.Body.String(), "\n"); got != statementExport+1 {
		t.Errorf("the file has %d lines, want a header and %d movements of %d", got, statementExport, many)
	}
	if page := f.statementRows(s, ""); len(page.Data) != 50 || page.NextCursor == nil {
		t.Errorf("the first page holds %d rows and the cursor %v", len(page.Data), page.NextCursor)
	}
}

// TestCreditsOf: an amount comes out as the exact decimal it stands for, at any size and sign, never through a floating
// point number.
func TestCreditsOf(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		amount int64
		want   string
	}{
		{0, "0.000000000"},
		{1, "0.000000001"},
		{-1, "-0.000000001"},
		{1_500_000_000, "1.500000000"},
		{999_999_999_999, "999.999999999"},
		{-1_000_000_000, "-1.000000000"},
		{1 << 62, "4611686018.427387904"},
	} {
		if got := creditsOf(tc.amount); got != tc.want {
			t.Errorf("creditsOf(%d) = %q, want %q", tc.amount, got, tc.want)
		}
	}
}
