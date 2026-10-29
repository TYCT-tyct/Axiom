package billing

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// TestPostLocksTheBalanceRow: a transaction that moves the user's balance takes the account's balance row lock in the
// statement that makes it, so its seq is taken under the lock even when the caller took none; a repeated event is not
// an error, and an account with no balance row is.
func TestPostLocksTheBalanceRow(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	e := event{site: b.admitted.Site, account: b.admitted.Account, kind: "credit", request: uuid.NewV7()}
	posted, release, done := make(chan error, 1), make(chan struct{}), make(chan error, 1)
	go func() { done <- b.postUnlocked(e, posted, release) }()
	if err := <-posted; err != nil {
		t.Fatal(err)
	}
	err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "SELECT 1 FROM billing.balances WHERE account_id = $1 FOR UPDATE NOWAIT", b.admitted.Account)
		return err
	})
	if err == nil || pg.SQLState(err) != "55P03" {
		t.Errorf("the balance row was not locked by post: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Error(err)
	}
}

// postUnlocked posts e in a transaction that took no lock, reports on posted how that went, and keeps the transaction
// open until release is closed; then it posts e again, and an event of an account with no balance row.
func (b *bank) postUnlocked(e event, posted chan<- error, release <-chan struct{}) error {
	lines := []line{{bookGiftExpense, 5, siteCredits}, {bookUserBalance, -5, siteCredits}}
	return b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		ok, err := post(b.t.Context(), tx, e, lines...)
		if err == nil && !ok {
			err = errors.New("the event was not posted")
		}
		posted <- err
		<-release
		if err != nil {
			return err
		}
		if again, err := post(b.t.Context(), tx, e, lines...); err != nil || again {
			return fmt.Errorf("a repeated event = %v, %w; want false and no error", again, err)
		}
		e.account, e.request = uuid.NewV7(), uuid.NewV7()
		if _, err := post(b.t.Context(), tx, e, lines...); !errors.Is(err, errNoBalance) {
			return fmt.Errorf("an account without a balance row: %w", err)
		}
		return nil
	})
}

// plan runs a query of the ledger under EXPLAIN (ANALYZE, BUFFERS) in scope, returning its blocks and plan.
func (b *bank) plan(scope pg.Scope, setup func(pg.Tx) error, query string, args ...any) (blocks int, plan string) {
	b.t.Helper()
	if err := b.app.Tx(b.t.Context(), scope, func(tx pg.Tx) error {
		if setup != nil {
			if err := setup(tx); err != nil {
				return err
			}
		}
		var raw []byte
		if err := tx.QueryRow(b.t.Context(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query, args...).Scan(&raw); err != nil {
			return err
		}
		var parsed []struct {
			Plan map[string]any
		}
		if err := jsonx.Unmarshal(raw, &parsed); err != nil {
			return err
		}
		root := parsed[0].Plan
		blocks = int(root["Shared Hit Blocks"].(float64) + root["Shared Read Blocks"].(float64)) //nolint:forcetypeassert // the shape of EXPLAIN's JSON
		plan = string(raw)
		return nil
	}); err != nil {
		b.t.Fatal(err)
	}
	return blocks, plan
}

// seedLedger adds n settlement transactions with two entries each to the account, numbered or, if legacy, as they were
// before the sequence.
func seedLedger(site, account uuid.UUID, n int, legacy bool) []string {
	statements := []string{
		fmt.Sprintf(`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id)
			SELECT gen_random_uuid(), '%s', '%s', 'settle', gen_random_uuid() FROM generate_series(1, %d)`, site, account, n),
		fmt.Sprintf(`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount)
			SELECT t.id, t.site_id, t.account_id, v.book, v.amount FROM billing.transactions t,
			(VALUES ('user_balance', 5), ('revenue', -5)) AS v(book, amount) WHERE t.account_id = '%s'`, account),
	}
	if legacy {
		statements = append(statements, fmt.Sprintf("UPDATE billing.transactions SET seq = NULL WHERE account_id = '%s'", account))
	}
	return statements
}

// TestLedgerReadsGrowWithThePage: a page of the ledger far into a long history reads about what the first page
// reads, from the index, not what the history behind or ahead of it holds, and the replay's bound reads about the same
// on a long account as on a short one. 30,000 numbered transactions of an account, 12,000 from before the sequence of
// another, as many of a third and three of a fourth are seeded, beside 2M rows across 2,000 other accounts; a page of 100 is read from the start and from deep in
// (the numbered ones at 25,000, the legacy ones at 10,000), and the bound of the long accounts against the short one.
//
// The bound over an account with rows from before the sequence pays for them: transactions_by_account_seq holds no seq
// to stop at for those rows, so the scan reaches the end of the account's prefix, which is where they sort. That is one
// pass over the account's own entries from before the release, which the release froze, rather than over a history that
// grows, so it is bounded by a budget instead of by the short account's reading.
func TestLedgerReadsGrowWithThePage(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	legacy, noise, short := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	statements := slices.Concat(seedLedger(b.admitted.Site, b.admitted.Account, 30000, false), seedLedger(b.admitted.Site, noise, 30000, false),
		seedLedger(b.admitted.Site, legacy, 12000, true), seedLedger(b.admitted.Site, short, 3, false),
		[]string{`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id)
			SELECT gen_random_uuid(), '` + b.admitted.Site.String() + `', a.id, 'settle', gen_random_uuid()
			FROM (SELECT gen_random_uuid() AS id FROM generate_series(1, 2000)) a, generate_series(1, 1000)`})
	b.sql(append(statements, fmt.Sprintf(`INSERT INTO billing.balances(site_id, account_id)
		SELECT '%s', unnest(ARRAY['%s'::uuid, '%s'::uuid, '%s'::uuid])`, b.admitted.Site, legacy, noise, short),
		"ANALYZE billing.transactions", "ANALYZE billing.entries")...)
	var farSeq, middleSeq int64
	var farAt time.Time
	var farID uuid.UUID
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		if err := tx.QueryRow(t.Context(), "SELECT seq FROM billing.transactions WHERE account_id = $1 ORDER BY seq OFFSET 25000 LIMIT 1", b.admitted.Account).Scan(&farSeq); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), "SELECT seq FROM billing.transactions WHERE account_id = $1 ORDER BY seq OFFSET 5000 LIMIT 1", b.admitted.Account).Scan(&middleSeq); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), "SELECT created_at, id FROM billing.transactions WHERE account_id = $1 ORDER BY created_at, id OFFSET 10000 LIMIT 1", legacy).Scan(&farAt, &farID)
	}); err != nil {
		t.Fatal(err)
	}
	long := epoch.Add(-100 * 365 * 24 * time.Hour)
	for _, tc := range []movementPlanCase{
		{
			"numbered", movements, "transactions_by_account_seq", "a page of 100, from the start and 25,000 in",
			[]any{b.admitted.Account, int64(0), int64(math.MaxInt64), int32(100)},
			[]any{b.admitted.Account, farSeq, int64(math.MaxInt64), int32(100)},
			0,
		},
		{
			"numbered, 25,000 rows remaining", movements, "transactions_by_account_seq", "a page of 100, from the start and 5,000 in",
			[]any{b.admitted.Account, int64(0), int64(math.MaxInt64), int32(100)},
			[]any{b.admitted.Account, middleSeq, int64(math.MaxInt64), int32(100)},
			0,
		},
		{
			"from before the sequence", legacyMovements, "transactions_legacy_by_account", "a page of 100, from the start and 10,000 in",
			[]any{legacy, long, uuid.UUID{}, int32(100)},
			[]any{legacy, farAt, farID, int32(100)},
			0,
		},
		{
			"the bound", replayThrough, "transactions_by_account_seq", "the bound over an account with 3 transactions and one with 30,000",
			[]any{short},
			[]any{b.admitted.Account},
			0,
		},
		{
			"the bound over rows from before the sequence", replayThrough, "transactions_by_account_seq", "the bound over an account with 3 transactions and one with 12,000 that all precede the sequence",
			[]any{short},
			[]any{legacy},
			1000,
		},
	} {
		for name, scope := range map[string]pg.Scope{
			"platform": b.platform,
			"account":  pg.NewScope(false, nil, []uuid.UUID{tc.first[0].(uuid.UUID), tc.deep[0].(uuid.UUID)}), //nolint:forcetypeassert // every case starts with its account
		} {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				tc.check(t, b, scope)
			})
		}
	}
	t.Run("statement", func(t *testing.T) { checkStatementReadsGrowWithThePage(t, b, legacy) })
}

type movementPlanCase struct {
	name, query, index, what string
	first, deep              []any
	budget                   int // the most blocks deep may touch; 0 compares deep with first instead
}

func (tc movementPlanCase) check(t *testing.T, b *bank, scope pg.Scope) {
	t.Helper()
	var setup func(pg.Tx) error
	if tc.query != replayThrough {
		setup = func(tx pg.Tx) error {
			_, err := loadAllocation(t.Context(), tx, tc.first[0].(uuid.UUID)) //nolint:forcetypeassert // every case starts with its account
			return err
		}
	}
	first, firstPlan := b.plan(scope, setup, tc.query, tc.first...)
	deep, deepPlan := b.plan(scope, setup, tc.query, tc.deep...)
	t.Logf("%s: %s: blocks touched %d, then %d", tc.name, tc.what, first, deep)
	rows, direction := float64(100), "Forward"
	if tc.query == replayThrough {
		rows, direction = 1, "Backward"
	}
	for _, plan := range []string{firstPlan, deepPlan} {
		checkTransactionIndexPlan(t, plan, rows, direction)
		if !strings.Contains(plan, tc.index) {
			t.Errorf("%s: not read in index order from %s:\n%s", tc.name, tc.index, plan)
		}
	}
	if tc.budget > 0 && deep > tc.budget || tc.budget == 0 && (deep > first+first/2+50 || deep > 1000) {
		t.Errorf("%s: %d blocks against %d; the plan:\n%s", tc.name, deep, first, deepPlan)
	}
}

// TestTransactionsFromBeforeTheSequenceReplayFirst: an account whose first transactions have no seq, as every account's
// do on the day the migration runs, is replayed with those first, by their time, and then the numbered ones, whatever
// the size of a page, so a page may hold the last of the old and the first of the new.
func TestTransactionsFromBeforeTheSequenceReplayFirst(t *testing.T) {
	for _, page := range []int32{1, 2, 5, 50} {
		t.Run(fmt.Sprint(page), func(t *testing.T) {
			b := newBank(t, 0, 0, Config{})
			expiring := b.creditBatch(100, longAgo)
			b.creditBatch(100, nil)
			b.charge(30)
			b.sql("UPDATE billing.transactions SET seq = NULL")
			b.charge(10)
			e := b.expirer()
			e.movements = page
			if written, err := e.expire(t.Context(), b.admitted.Account); err != nil || written != 60 {
				t.Fatalf("expire = %d, %v; want 60 written off: 30 and 10 charged to the batch that expired", written, err)
			}
			b.checkStored(b.admitted.Account)
			if n := b.batchLeft(expiring); n != 0 {
				t.Errorf("the expired batch holds %d", n)
			}
		})
	}
}
