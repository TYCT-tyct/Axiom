package billing

import (
	"fmt"
	"testing"

	"axiom/internal/platform/pg"
)

func TestPrepareRefundPagesWithoutExpiry(t *testing.T) {
	t.Parallel()

	b := newBank(t, 0, 0, Config{})
	f := b.refundable(1000)
	b.sql(fmt.Sprintf(`INSERT INTO billing.transactions (id,site_id,account_id,kind,request_id)
		SELECT gen_random_uuid(),'%s','%s','settle',gen_random_uuid() FROM generate_series(1,%d)`,
		b.admitted.Site, f.Account, 2*movementPage+1))
	if b.count("SELECT allocated_seq FROM billing.balances") != 0 {
		t.Fatal("fixture already replayed")
	}
	if err := PrepareRefund(t.Context(), b.app, b.platform, f); err != nil {
		t.Fatal(err)
	}
	if b.count("SELECT allocated_seq FROM billing.balances") != b.count("SELECT max(seq) FROM billing.transactions") {
		t.Fatal("preparation did not finish all pages of a nonexpiring account")
	}
	if got := b.freeze(f); got != 1000 {
		t.Fatalf("freeze after preparation = %d", got)
	}
}

func TestPrepareRefundLeavesBalanceUnlocked(t *testing.T) {
	t.Parallel()

	b := newBank(t, 0, 0, Config{})
	f := b.refundable(1000)
	super, err := pg.Open(t.Context(), b.db.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close()
	done := make(chan error, 1)
	err = super.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), "LOCK TABLE billing.entries IN ACCESS EXCLUSIVE MODE"); err != nil {
			return err
		}
		go func() { done <- PrepareRefund(t.Context(), b.app, b.platform, f) }()
		b.waitForLockWait()
		_, err := tx.Exec(t.Context(), "SELECT 1 FROM billing.balances WHERE account_id=$1 FOR UPDATE NOWAIT", f.Account)
		return err
	})
	prepareErr := <-done
	if err != nil {
		t.Fatalf("pre-replay held the money lock while reading history: %v", err)
	}
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	b.checkStored(f.Account)
}

func TestRefundRejectsInvalidBatchBeforeLock(t *testing.T) {
	t.Parallel()

	b := newBank(t, 0, 0, Config{})
	f := b.refundable(1000)
	f.Batch = b.creditBatch(100, nil)
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		if _, err := lockAccount(t.Context(), tx, f.Account); err != nil {
			return err
		}
		return b.app.Tx(t.Context(), b.platform, func(other pg.Tx) error {
			if _, err := other.Exec(t.Context(), "SET LOCAL lock_timeout='1ms'"); err != nil {
				return err
			}
			_, err := FreezeRefund(t.Context(), other, f)
			if err == nil || pg.SQLState(err) == "55P03" {
				return fmt.Errorf("invalid batch waited for a balance lock: %w", err)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
}
