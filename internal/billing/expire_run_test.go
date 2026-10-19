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

// TestExpireGoesPastABlockedAccount: an account whose lock is held, so that its write-off cannot finish within its
// budget, fails and is counted, and the run goes on: the accounts after it are written off in the same run.
func TestExpireGoesPastABlockedAccount(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	b.creditBatch(100, longAgo) // the first account, the first in the scan
	other := b.accountWithCredit()
	release := b.holdLock(b.admitted.Account)
	defer release()
	e := b.expirer()
	e.accountBudget = 300 * time.Millisecond
	changed, failed, err := e.run(t.Context())
	if err != nil || changed != 1 || failed != 1 {
		t.Fatalf("run = %d changed, %d failed, %v; want the other account changed and the blocked one failed", changed, failed, err)
	}
	if n := b.balanceOf(other.Account); n != 1 {
		t.Errorf("the other account holds %d, want only the 1 that does not expire", n)
	}
	release()
	if changed, failed, err := e.run(t.Context()); err != nil || changed != 1 || failed != 0 {
		t.Errorf("the next run = %d changed, %d failed, %v; want the blocked account written off", changed, failed, err)
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
