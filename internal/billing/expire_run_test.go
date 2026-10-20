package billing

import (
	"fmt"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// accountWithCredit is another account with a batch of 10 that has expired.
func (b *bank) accountWithCredit() Admitted {
	b.t.Helper()
	a := b.secondTenant(1)
	b.addCreditTo(a, Credit{ID: uuid.NewV7(), Amount: 10, ExpiresAt: longAgo})
	return a
}

// TestExpireRun: a run goes through every account with credit due, in pages, and writes each off; a second run finds
// nothing, and a batch not due is left.
func TestExpireRun(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	b.creditBatch(100, longAgo)
	var accounts []Admitted
	for range 4 {
		accounts = append(accounts, b.accountWithCredit())
	}
	b.addCreditTo(accounts[0], Credit{ID: uuid.NewV7(), Amount: 5, ExpiresAt: farAhead})
	e := b.expirer()
	e.accounts = 2 // five accounts in pages of 2, 2 and 1
	changed, failed, err := e.run(t.Context())
	if err != nil || changed != 5 || failed != 0 {
		t.Fatalf("run = %d changed, %d failed, %v; want 5 and none", changed, failed, err)
	}
	if n := b.count("SELECT sum(balance) FROM billing.balances"); n != 4*1+5 {
		t.Errorf("balances add up to %d, want the 4 not expiring and the 5 not due", n)
	}
	if changed, _, err := e.run(t.Context()); err != nil || changed != 0 {
		t.Errorf("a second run = %d, %v; want nothing", changed, err)
	}
	for _, a := range accounts {
		b.checkStored(a.Account)
	}
}

// holdLock locks the balance row of the account in a transaction of its own until the returned function is called.
func (b *bank) holdLock(account uuid.UUID) (release func()) {
	b.t.Helper()
	locked, done, stop := make(chan struct{}), make(chan error, 1), make(chan struct{})
	go func() {
		done <- b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
			if _, err := lockAccount(b.t.Context(), tx, account); err != nil {
				return err
			}
			close(locked)
			<-stop
			return nil
		})
	}()
	<-locked
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			if err := <-done; err != nil {
				b.t.Error(err)
			}
		})
	}
}

// TestAdvanceRacesAReplayThatSpendsABatch: a replay without the lock reads the place of the allocation, and before it
// reads the batches another replay commits the batch spent up, which takes it out of the batches that hold something.
// The first replay drops its work as stale and the expiry goes on under the lock; it does not fail on a credit whose
// batch it did not load (#357). The other replay holds the batches' table locked from before it saves until it
// commits, so the first one reads the place, waits on the batches and reads them after the commit, whatever the timing.
func TestAdvanceRacesAReplayThatSpendsABatch(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	b.creditBatch(100, farAhead)
	b.charge(100) // spends the batch in the ledger; only a replay writes it to the batch
	// The other replay runs as the superuser, since the application may not lock the table.
	super, err := pg.Open(t.Context(), b.db.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close()
	saved, commit, other := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	release := sync.OnceFunc(func() { close(commit) })
	defer release()
	go func() {
		other <- super.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "LOCK TABLE billing.credit_batches IN ACCESS EXCLUSIVE MODE"); err != nil {
				return err
			}
			if _, err := writeOffAccount(t.Context(), tx, b.admitted.Account, movementPage); err != nil {
				return err
			}
			close(saved)
			<-commit
			return nil
		})
	}()
	select {
	case <-saved:
	case err := <-other:
		t.Fatal(err)
	}
	expired := make(chan error, 1)
	go func() {
		_, err := b.expirer().expire(t.Context(), b.admitted.Account)
		expired <- err
	}()
	b.waitForLockWait()
	release()
	if err := <-other; err != nil {
		t.Fatal(err)
	}
	if err := <-expired; err != nil {
		t.Fatalf("the expiry raced by another replay failed: %v", err)
	}
	b.checkStored(b.admitted.Account)
}

// TestExpireGoesPastABlockedAccount: an account whose lock is held fails and is counted, and the run goes on: the
// accounts after it are written off in the same run. The test ends the blocked write-off itself, once it waits on the
// lock, by terminating its backend; a short budget would end it by the clock, but then the next account would share
// that clock, and on a loaded machine its write-off could outlast it too (TestExpireAccountBudget has the budget).
func TestExpireGoesPastABlockedAccount(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	b.creditBatch(100, longAgo) // the first account, the first in the scan
	other := b.accountWithCredit()
	release := b.holdLock(b.admitted.Account)
	defer release()
	e := b.expirer()
	type outcome struct {
		changed, failed int
		err             error
	}
	ran := make(chan outcome, 1)
	go func() {
		changed, failed, err := e.run(b.t.Context())
		ran <- outcome{changed, failed, err}
	}()
	b.waitForLockWait()
	b.sql("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'")
	if got := <-ran; got != (outcome{1, 1, nil}) {
		t.Fatalf("run = %d changed, %d failed, %v; want the other account changed and the blocked one failed", got.changed, got.failed, got.err)
	}
	if n := b.balanceOf(other.Account); n != 1 {
		t.Errorf("the other account holds %d, want only the 1 that does not expire", n)
	}
	release()
	if changed, failed, err := e.run(t.Context()); err != nil || changed != 1 || failed != 0 {
		t.Errorf("the next run = %d changed, %d failed, %v; want the blocked account written off", changed, failed, err)
	}
}

// TestExpireAccountBudget: an account whose budget is spent before its write-off starts fails and is counted, and
// nothing of it is written; a run with its budget writes it off. The budget is already past, not short, so that no
// clock decides the outcome.
func TestExpireAccountBudget(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	b.creditBatch(100, longAgo)
	e := b.expirer()
	e.accountBudget = -time.Second
	if changed, failed, err := e.run(t.Context()); err != nil || changed != 0 || failed != 1 {
		t.Fatalf("run = %d changed, %d failed, %v; want the account failed", changed, failed, err)
	}
	if n := b.balanceOf(b.admitted.Account); n != 100 {
		t.Errorf("the account holds %d, want the 100 untouched", n)
	}
	e.accountBudget = expireAccountBudget
	if changed, failed, err := e.run(t.Context()); err != nil || changed != 1 || failed != 0 {
		t.Errorf("the next run = %d changed, %d failed, %v; want the account written off", changed, failed, err)
	}
}

// TestExpireScanGoesOn: a run whose budget is spent stops, and the next goes on after the account it reached, so an
// account that fails every time, first in the scan, does not keep the ones after it from being reached; at the end the
// scan wraps.
func TestExpireScanGoesOn(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	b.creditBatch(100, longAgo)
	second, third := b.accountWithCredit(), b.accountWithCredit()
	b.sql("UPDATE billing.balances SET balance = balance + 1 WHERE account_id = '" + b.admitted.Account.String() + "'") // never writes off
	e := b.expirer()
	e.runBudget = time.Nanosecond // each run does one account
	for i, tc := range []struct {
		changed, failed int
		second, third   int64
	}{
		{0, 1, 11, 11}, {1, 0, 1, 11}, {1, 0, 1, 1}, {0, 0, 1, 1}, {0, 1, 1, 1}, // the fourth run finds the end and wraps; the fifth starts again at the first
	} {
		changed, failed, err := e.run(t.Context())
		if err != nil || changed != tc.changed || failed != tc.failed || b.balanceOf(second.Account) != tc.second || b.balanceOf(third.Account) != tc.third {
			t.Fatalf("run %d = %d changed, %d failed, %v; balances %d %d; want %+v", i+1, changed, failed, err,
				b.balanceOf(second.Account), b.balanceOf(third.Account), tc)
		}
	}
}

// TestReplayPagesCrossTheirBoundary: whatever the size of a page of the ledger, a history that fills pages exactly or
// leaves the last one short is replayed whole, once, in short transactions without the lock and then under it.
func TestReplayPagesCrossTheirBoundary(t *testing.T) {
	for _, page := range []int32{1, 4, 13, 14} {
		t.Run(fmt.Sprint(page), func(t *testing.T) {
			b := newBank(t, 0, 0, Config{})
			b.creditBatch(1000, longAgo)
			for range 6 {
				b.charge(5)
			}
			if n := b.count("SELECT count(*) FROM billing.transactions"); n != 13 {
				t.Fatalf("%d transactions, the test needs 13 to fill pages of 13 exactly", n)
			}
			e := b.expirer()
			e.movements = page
			if written, err := e.expire(t.Context(), b.admitted.Account); err != nil || written != 970 {
				t.Fatalf("expire = %d, %v; want 970 written off", written, err)
			}
			b.checkStored(b.admitted.Account)
			if n := b.count("SELECT count(*) FROM billing.transactions WHERE kind = 'expire'"); n != 1 {
				t.Errorf("%d expiry transactions, want 1", n)
			}
		})
	}
}
