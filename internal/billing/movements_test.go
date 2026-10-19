package billing

import (
	"errors"
	"fmt"
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
	lines := []line{{bookGiftExpense, 5}, {bookUserBalance, -5}}
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

// movementsPlan runs the query of a page of the ledger under EXPLAIN (ANALYZE, BUFFERS) and returns the blocks it
// touched and the plan.
func (b *bank) movementsPlan(account uuid.UUID, after orderKey, size int32) (blocks int, plan string) {
	b.t.Helper()
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		var raw []byte
		err := tx.QueryRow(b.t.Context(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+movements, account, after.seq, after.at, after.id, size).Scan(&raw)
		if err != nil {
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

// TestMovementsReadsGrowWithThePage: a page of the ledger far into a long history reads about what the first page
// reads, from the index, not what the history behind or ahead of it holds. 30,000 transactions of the account and as
// many of others are seeded and a page of 100 read from the start and from 25,000 in.
func TestMovementsReadsGrowWithThePage(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	var statements []string
	for _, account := range []uuid.UUID{b.admitted.Account, uuid.NewV7()} {
		statements = append(statements,
			fmt.Sprintf(`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id)
				SELECT gen_random_uuid(), '%s', '%s', 'settle', gen_random_uuid() FROM generate_series(1, 30000)`, b.admitted.Site, account),
			fmt.Sprintf(`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount)
				SELECT t.id, t.site_id, t.account_id, v.book, v.amount FROM billing.transactions t, (VALUES ('user_balance', 5), ('revenue', -5)) AS v(book, amount)
				WHERE t.account_id = '%s'`, account))
	}
	b.sql(append(statements, "ANALYZE billing.transactions", "ANALYZE billing.entries")...)
	var far orderKey
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT coalesce(seq, 0), created_at, id FROM billing.transactions WHERE account_id = $1
			ORDER BY coalesce(seq, 0), created_at, id OFFSET 25000 LIMIT 1`, b.admitted.Account).Scan(&far.seq, &far.at, &far.id)
	}); err != nil {
		t.Fatal(err)
	}
	first, firstPlan := b.movementsPlan(b.admitted.Account, orderKey{at: epoch.Add(-100 * 365 * 24 * time.Hour)}, 100)
	deep, deepPlan := b.movementsPlan(b.admitted.Account, far, 100)
	t.Logf("blocks touched by a page of 100: from the start %d, from 25,000 in %d", first, deep)
	if !strings.Contains(deepPlan, "transactions_by_account_order") || strings.Contains(deepPlan, "Seq Scan") {
		t.Errorf("the page is not read from the index: %s", deepPlan)
	}
	if deep > 2*first+50 || deep > 2500 {
		t.Errorf("a page 25,000 transactions in touches %d blocks against %d at the start; the plans:\n%s\n%s", deep, first, firstPlan, deepPlan)
	}
}
