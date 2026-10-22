package billing

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// statement reads a page of the account's statement as the console does, in the account's own scope.
func (b *bank) statement(a Admitted, size int, after *StatementCursor) ([]StatementRow, *StatementCursor) {
	b.t.Helper()
	var rows []StatementRow
	var next *StatementCursor
	if err := b.app.ReadTx(b.t.Context(), a.Scope, func(tx pg.Tx) (err error) {
		rows, next, err = ListStatement(b.t.Context(), tx, StatementQuery{Account: a.Account, PageSize: size, After: after})
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return rows, next
}

// TestStatementKinds: what the ledger calls an event and the source of the batch a credit opened read as the kinds of a
// statement, and a kind the user has no card for is an error rather than a row.
func TestStatementKinds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind, source string
		want         StatementKind
	}{
		{"credit", "gift", StatementGift},
		{"credit", "paid", StatementTopUp},
		{"settle", "", StatementSpend},
		{"expire", "", StatementExpiry},
		{"refund", "", StatementRefund},
		{"credit", "", ""},
		{"credit", "unknown", ""},
		{"reverse", "", StatementReversal},
		{"attempt_cost", "", ""},
		{"cost_adjust", "", ""},
		{"freeze", "", StatementFreeze},
		{"unfreeze", "", StatementUnfreeze},
	} {
		got, err := statementKind(tc.kind, tc.source)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("statementKind(%q, %q) = %q, %v; want %q", tc.kind, tc.source, got, err, tc.want)
		}
	}
}

// TestStatementCursorSurvivesTheConsole: a cursor comes back from the page as the console passed it, negative balances
// and a place among the rows from before the sequence included, and text that is not one is refused rather than read as
// the start of the statement.
func TestStatementCursorSurvivesTheConsole(t *testing.T) {
	t.Parallel()
	for _, want := range []StatementCursor{
		{Seq: math.MaxInt64, At: statementNewest.At, ID: uuid.Max(), Balance: math.MinInt64},
		{Seq: 42, At: epoch, ID: uuid.NewV7(), Balance: -1, Frozen: 200},
		{At: epoch.Add(time.Microsecond), ID: uuid.Max(), Balance: math.MaxInt64},
	} {
		got, err := ParseStatementCursor(want.String())
		if err != nil || got != want {
			t.Errorf("a cursor of %+v came back as %+v, %v", want, got, err)
		}
	}
	for _, text := range []string{"", "nonsense", wantShort()} {
		if _, err := ParseStatementCursor(text); !errors.Is(err, ErrBadStatementCursor) {
			t.Errorf("%q read as a cursor: %v", text, err)
		}
	}
}

// wantShort is a well-formed base64 cursor of the wrong length.
func wantShort() string { return StatementCursor{Seq: 1}.String()[:20] }

// TestStatement: a statement is one row per movement of the user's balance, newest first, each with the balance it left,
// under the kind the user has a card for and the call or the batch it was for; a transaction that moved the balance
// none of it, such as a waived charge, takes no row.
func TestStatement(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	gift := b.creditBatch(1000, nil)
	topUp := b.paid(500, Payment{currency(t, "USD"), 600, 10}, uuid.UUID{})
	if err := b.creditPaid(topUp); err != nil {
		t.Fatal(err)
	}
	b.charge(300)
	expiring := b.creditBatch(200, longAgo)
	if written := b.expire(); written != 200 {
		t.Fatalf("the expiry wrote off %d, want 200", written)
	}
	// A charge the free-of-charge rule waived moved nothing: its transaction and its entries exist and no statement row
	// may show them.
	waived, request, site, account := uuid.NewV7(), uuid.NewV7(), b.admitted.Site, b.admitted.Account
	b.sql(
		"INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id) VALUES ('"+waived.String()+"', '"+site.String()+"', '"+account.String()+"', 'settle', '"+request.String()+"')",
		"INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount) VALUES ('"+waived.String()+"', '"+site.String()+"', '"+account.String()+"', 'revenue', 5), ('"+waived.String()+"', '"+site.String()+"', '"+account.String()+"', 'platform_borne', -5)",
	)
	rows, next := b.statement(b.admitted, 50, nil)
	if next != nil {
		t.Errorf("the last page gave a cursor %v", next)
	}
	want := []struct {
		kind   StatementKind
		amount int64
		ref    uuid.UUID
	}{{StatementExpiry, -200, expiring}, {StatementGift, 200, expiring}, {StatementSpend, -300, uuid.UUID{}}, {StatementTopUp, 500, topUp.ID}, {StatementGift, 1000, gift}}
	if len(rows) != len(want) {
		t.Fatalf("%d rows, want %d: %+v", len(rows), len(want), rows)
	}
	// The expiring gift must appear too: its credit and write-off cancel, leaving 1200 now.
	after := []int64{1200, 1400, 1200, 1500, 1000}
	checkStatementRows(t, rows, want, after, waived)
}

func checkStatementRows(t *testing.T, rows []StatementRow, want []struct {
	kind   StatementKind
	amount int64
	ref    uuid.UUID
}, after []int64, waived uuid.UUID,
) {
	t.Helper()
	for i, w := range want {
		row := rows[i]
		if row.Kind != w.kind || row.Amount != w.amount || row.Balance != after[i] {
			t.Errorf("row %d: %s %d with %d left; want %s %d with %d", i, row.Kind, row.Amount, row.Balance, w.kind, w.amount, after[i])
		}
		if w.kind != StatementSpend && row.Reference != w.ref {
			t.Errorf("row %d refers to %s; want the batch %s", i, row.Reference, w.ref)
		}
		if w.kind == StatementSpend && row.Reference == uuid.Nil() {
			t.Errorf("row %d does not say which call it charged", i)
		}
		if row.ID == waived {
			t.Errorf("row %d is the waived charge", i)
		}
	}
	if rows[0].At.Before(rows[len(rows)-1].At) {
		t.Errorf("the newest movement is not first: %v then %v", rows[0].At, rows[len(rows)-1].At)
	}
}

// TestStatementPages: pages follow one another without a gap or a repeat whatever their size, and the balance each row
// carries runs back through them, so a reader can add up a statement as it stands.
func TestStatementPages(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	b.credit(10_000)
	for range 25 {
		b.charge(100)
	}
	var all []StatementRow
	for after := (*StatementCursor)(nil); ; {
		var rows []StatementRow
		var next *StatementCursor
		rows, next = b.statement(b.admitted, 3, after)
		if len(rows) > 3 {
			t.Fatalf("a page of 3 holds %d rows", len(rows))
		}
		all = append(all, rows...)
		if next == nil {
			break
		}
		after = next
	}
	if len(all) != 26 {
		t.Fatalf("%d movements in the statement, want the gift and 25 charges", len(all))
	}
	checkStatementChain(t, all)
	if all[0].Balance != b.balanceOf(b.admitted.Account) || all[len(all)-1].Kind != StatementGift {
		t.Errorf("the statement starts with %+v and ends with %+v", all[0], all[len(all)-1])
	}
}

// checkStatementChain verifies opening + changes = closing for both counters and unique row identities.
func checkStatementChain(t *testing.T, rows []StatementRow) {
	t.Helper()
	if len(rows) == 0 {
		t.Fatal("empty statement")
	}
	seen := map[uuid.UUID]bool{}
	for i, row := range rows {
		if seen[row.ID] {
			t.Errorf("row %d is a repeat", i)
		}
		seen[row.ID] = true
		if i > 0 {
			previous := rows[i-1]
			if previous.Balance-previous.Amount != row.Balance || previous.Frozen-previous.FrozenChange != row.Frozen {
				t.Errorf("row %d does not reconcile with the preceding row", i)
			}
		}
	}
	last := rows[len(rows)-1]
	if last.Balance-last.Amount != 0 || last.Frozen-last.FrozenChange != 0 {
		t.Errorf("statement does not reconcile to zero opening funds: %+v", last)
	}
}

// TestStatementPagesAcrossNewMovements: a charge that settles after a page was read lands above the cursor, so the next
// page starts where the last one ended, and the balance the cursor carries stays the one the rows left.
func TestStatementPagesAcrossNewMovements(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	b.credit(10_000)
	for range 4 {
		b.charge(100)
	}
	first, next := b.statement(b.admitted, 3, nil)
	if len(first) != 3 || next == nil {
		t.Fatalf("the first page holds %d rows and a cursor %v", len(first), next)
	}
	before := b.balanceOf(b.admitted.Account)
	b.charge(250)
	second, _ := b.statement(b.admitted, 3, next)
	if len(second) != 2 {
		t.Fatalf("the second page holds %d rows, want the last 2", len(second))
	}
	if second[0].Balance != first[len(first)-1].Balance-first[len(first)-1].Amount {
		t.Errorf("the second page starts at %d, and the first ends at %d: a movement between them moved the column",
			second[0].Balance, first[len(first)-1].Balance-first[len(first)-1].Amount)
	}
	if first[0].Balance != before {
		t.Errorf("the first page's balance reads %d, the one it was read with is %d", first[0].Balance, before)
	}
	if got := b.balanceOf(b.admitted.Account); got != before-250 {
		t.Errorf("the account holds %d after a charge of 250 from %d", got, before)
	}
	if got := second[len(second)-1]; got.Kind != StatementGift || got.Balance != got.Amount {
		t.Errorf("the statement ends with %+v", got)
	}
}

// TestStatementFromBeforeTheSequence: the rows from before the sequence come last in the statement, newest first among
// themselves by the key the ledger fixes for them, and the numbered ones come first.
func TestStatementFromBeforeTheSequence(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	b.credit(1000)
	b.charge(100)
	b.charge(50)
	b.sql("UPDATE billing.transactions SET seq = NULL")
	b.charge(20) // numbered, and so above every row from before the sequence
	legacy := b.legacyStatementIDs()
	rows, next := b.statement(b.admitted, 50, nil)
	if next != nil {
		t.Errorf("the last page gave a cursor %v", next)
	}
	var got []uuid.UUID
	for _, row := range rows {
		got = append(got, row.ID)
	}
	if len(got) != len(legacy)+1 || !slices.Equal(got[1:], legacy) {
		t.Errorf("the statement is %v; want the numbered charge, then %v", got, legacy)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Balance-rows[i-1].Amount != rows[i].Balance {
			t.Errorf("row %d does not continue the balance above it", i)
		}
	}
}

func (b *bank) legacyStatementIDs() []uuid.UUID {
	b.t.Helper()

	var ids []uuid.UUID
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		rows, err := tx.Query(b.t.Context(), "SELECT id FROM billing.transactions WHERE account_id = $1 AND seq IS NULL AND EXISTS (SELECT 1 FROM billing.entries e WHERE e.transaction_id = billing.transactions.id AND e.book = 'user_balance') ORDER BY created_at DESC, id DESC", b.admitted.Account)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}); err != nil {
		b.t.Fatal(err)
	}
	return ids
}

// TestStatementIsTheAccountsOwn: another account's scope reads nothing of this one's statement, and a page size the
// contract does not give out is refused.
func TestStatementIsTheAccountsOwn(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, 0, Config{})
	stranger := Admitted{Scope: pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()}), Account: uuid.NewV7()}
	// Deliberately name the victim's account: an empty query for the stranger's own ID would not test RLS.
	stranger.Account = b.admitted.Account
	if rows, next := b.statement(stranger, 50, nil); len(rows) != 0 || next != nil {
		t.Errorf("another account read %+v with a cursor %v", rows, next)
	}
	for _, size := range []int{0, -1, MaxStatementPage + 1} {
		if err := b.app.ReadTx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
			_, _, err := ListStatement(t.Context(), tx, StatementQuery{Account: b.admitted.Account, PageSize: size})
			return err
		}); err == nil {
			t.Errorf("a page of %d was read", size)
		}
	}
}

func TestStatementSkipsBoundedEmptySegments(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, 0, Config{})
	b.sql("INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id) SELECT gen_random_uuid(), '" + b.admitted.Site.String() + "', '" + b.admitted.Account.String() + "', 'settle', gen_random_uuid() FROM generate_series(1, 300)")
	rows, next := b.statement(b.admitted, 2, nil)
	if len(rows) != 0 || next == nil {
		t.Fatalf("empty segment = %+v, %v", rows, next)
	}
	b.credit(200) // newer than the cursor, including when no visible row was returned
	for pages := 0; next != nil; pages++ {
		if pages >= 20 {
			t.Fatal("cursor did not advance across zero-effect events")
		}
		previous := *next
		var page []StatementRow
		page, next = b.statement(b.admitted, 2, next)
		rows = append(rows, page...)
		if next != nil && next.Seq >= previous.Seq {
			t.Fatal("empty cursor repeated or moved forward")
		}
	}
	if len(rows) != 1 || rows[0].Amount != 1000 || rows[0].Balance != 1000 {
		t.Fatalf("visible history = %+v", rows)
	}
}

// TestStatementReadsGrowWithThePage: a page deep in a long history reads about what the first page reads, from the two
// indexes the ledger has, not what the history above it holds.
func TestStatementReadsGrowWithThePage(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	legacy := uuid.NewV7()
	b.sql(append(slices.Concat(
		seedLedger(b.admitted.Site, b.admitted.Account, 30000, false),
		seedLedger(b.admitted.Site, legacy, 12000, true),
	), "ANALYZE billing.transactions", "ANALYZE billing.entries")...)
	var far int64
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT seq FROM billing.transactions WHERE account_id = $1 ORDER BY seq OFFSET 25000 LIMIT 1", b.admitted.Account).Scan(&far)
	}); err != nil {
		t.Fatal(err)
	}
	first, firstPlan := b.plan(statementPage, b.admitted.Account, int64(math.MaxInt64), int32(100), statementNewest.At, uuid.Max())
	deep, plan := b.plan(statementPage, b.admitted.Account, far, int32(100), statementNewest.At, uuid.Max())
	t.Logf("EXPLAIN blocks: first=%d deep=%d", first, deep)
	t.Logf("first plan: %s\ndeep plan: %s", firstPlan, plan)
	checkStatementIndexPlan(t, plan)
	if !strings.Contains(plan, "transactions_by_account_seq") {
		t.Errorf("the deep page is not read from the index:\n%s", plan)
	}
	if deep > first+first/2+50 || deep > 1000 || first > 1000 {
		t.Errorf("the deep page touched %d blocks against the first page's %d:\n%s", deep, first, plan)
	}
}

// Entry lookups may use tiny bitmap scans. Only the transaction scan must preserve keyset order before LIMIT.
type statementQueryPlan struct {
	NodeType  string               `json:"Node Type"`
	Relation  string               `json:"Relation Name"`
	Direction string               `json:"Scan Direction"`
	Loops     float64              `json:"Actual Loops"`
	Plans     []statementQueryPlan `json:"Plans"`
}

func checkStatementIndexPlan(t *testing.T, raw string) {
	t.Helper()
	var plans []struct{ Plan statementQueryPlan }
	if err := jsonx.Unmarshal([]byte(raw), &plans); err != nil {
		t.Fatal(err)
	}
	var check func(statementQueryPlan)
	check = func(p statementQueryPlan) {
		if p.Relation == "transactions" && p.Loops > 0 {
			if p.NodeType != "Index Scan" || p.Direction != "Backward" {
				t.Errorf("unordered transaction scan: %s %s", p.NodeType, p.Direction)
			}
		}
		for _, child := range p.Plans {
			check(child)
		}
	}
	check(plans[0].Plan)
}
