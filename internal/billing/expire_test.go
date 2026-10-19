package billing

import (
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

var (
	longAgo   = func() *time.Time { t := time.Now().Add(-time.Minute); return &t }()
	farAhead  = func() *time.Time { t := time.Now().Add(time.Hour); return &t }()
	muchLater = func() *time.Time { t := time.Now().Add(2 * time.Hour); return &t }()
)

// expirer is the expiry of the bank's database, its log thrown away.
func (b *bank) expirer() *expirer {
	return newExpirer(b.app, b.platform, slog.New(slog.DiscardHandler), nil)
}

// expire runs the expiry of the bank's account and checks that what it stored is what a replay of the whole ledger
// from nothing gives.
func (b *bank) expire() int64 {
	b.t.Helper()
	written, err := b.expirer().expire(b.t.Context(), b.admitted.Account)
	if err != nil {
		b.t.Fatal(err)
	}
	b.checkStored(b.admitted.Account)
	return written
}

// checkStored compares what the allocation of the account stores with a replay of the whole ledger from nothing.
func (b *bank) checkStored(account uuid.UUID) {
	b.t.Helper()
	fresh := b.replayed(account)
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		stored, err := loadAllocation(b.t.Context(), tx, account)
		if err != nil {
			return err
		}
		if stored.at.compare(fresh.at) != 0 || stored.unfunded != fresh.unfunded {
			b.t.Errorf("stored allocation at %v unfunded %d, a replay from nothing gives %v %d", stored.at, stored.unfunded, fresh.at, fresh.unfunded)
		}
		for id, batch := range fresh.batches {
			var consumed, expired int64
			if err := tx.QueryRow(b.t.Context(), "SELECT consumed, expired FROM billing.credit_batches WHERE id = $1", id).Scan(&consumed, &expired); err != nil {
				return err
			}
			if consumed != batch.consumed || expired != batch.expired {
				b.t.Errorf("batch %s stores consumed %d expired %d, a replay from nothing gives %d %d", id, consumed, expired, batch.consumed, batch.expired)
			}
		}
		return nil
	}); err != nil {
		b.t.Fatal(err)
	}
}

// replayed replays the whole ledger of the account onto its batches as the credit transactions made them, and checks
// the invariants that hold at any time: the replay funds exactly the balance, and no batch is over-spent.
func (b *bank) replayed(account uuid.UUID) *allocation {
	b.t.Helper()
	var a *allocation
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		rows, err := tx.Query(b.t.Context(), `SELECT b.id, b.site_id, b.source, b.expires_at, b.amount, 0::bigint, 0::bigint,
				coalesce(t.seq, 0)::bigint, t.created_at, t.id
			FROM billing.credit_batches b JOIN billing.transactions t
			ON t.request_id = b.id AND t.kind = 'credit' AND t.attempt_seq = 0 AND t.revision = 0
			WHERE b.account_id = $1`, account)
		if err != nil {
			return err
		}
		defer rows.Close()
		var batches []LiveBatchesRow
		for rows.Next() {
			var r LiveBatchesRow
			if err := rows.Scan(&r.ID, &r.SiteID, &r.Source, &r.ExpiresAt, &r.Amount, &r.Consumed, &r.Expired, &r.OpenedSeq, &r.OpenedAt, &r.OpenedID); err != nil {
				return err
			}
			batches = append(batches, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		a = newAllocation(AllocationStateRow{}, batches)
		return a.catchUp(b.t.Context(), tx, account, 7)
	}); err != nil {
		b.t.Fatal(err)
	}
	if got, want := a.funded(), b.count("SELECT balance FROM billing.balances WHERE account_id = '"+account.String()+"'"); got != want {
		b.t.Errorf("the batches of %s fund %d of a balance of %d", account, got, want)
	}
	for _, batch := range a.batches {
		if batch.consumed+batch.expired > batch.amount {
			b.t.Errorf("batch %+v is over-spent", *batch)
		}
	}
	return a
}

// creditBatch adds a gift of amount that expires at expires, never if nil, and returns the batch's ID.
func (b *bank) creditBatch(amount int64, expires *time.Time) uuid.UUID {
	b.t.Helper()
	c := Credit{ID: uuid.NewV7(), Amount: amount, ExpiresAt: expires}
	b.addCreditTo(b.admitted, c)
	return c.ID
}

// addCreditTo adds c, a gift, to the account a is admitted for, in the platform scope.
func (b *bank) addCreditTo(a Admitted, c Credit) {
	b.t.Helper()
	c.Site, c.Account, c.Actor, c.Reason = a.Site, a.Account, audit.System("billing.test"), "test credit"
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error { return AddCredit(b.t.Context(), tx, c) }); err != nil {
		b.t.Fatal(err)
	}
}

// charge settles a call of exactly n, whose reservation is as close to n as a reservation can be.
func (b *bank) charge(n int64) {
	b.t.Helper()
	f, err := b.billing.Settle(b.t.Context(), b.hold(max(n-100, 0)), outcome(Committed, FinishStop, used(b.t, n, 0, 0)))
	if err != nil || f.Charged != platform.AmountExact(n) {
		b.t.Fatalf("Settle = %+v, %v; want %d charged", f, err, n)
	}
}

// batchLeft is what the batch holds: its amount less what was consumed and what expired, as stored.
func (b *bank) batchLeft(id uuid.UUID) int64 {
	b.t.Helper()
	return b.count("SELECT amount - consumed - expired FROM billing.credit_batches WHERE id = '" + id.String() + "'")
}

// balanceOf is the balance of the account.
func (b *bank) balanceOf(account uuid.UUID) int64 {
	b.t.Helper()
	return b.count("SELECT balance FROM billing.balances WHERE account_id = '" + account.String() + "'")
}

// TestExpire: what is left of a batch past its expiry is written off, once, as the books' credit-expired income. What
// was spent from it after its expiry and before the run is use, not lapse (docs/domains/billing.md「额度批次」); of
// two batches the one that expires is spent first, so a charge of 130 takes all of it and 30 of the other.
func TestExpire(t *testing.T) {
	for _, tc := range []struct {
		name         string
		charge, want int64
	}{
		{"unused", 0, 100},
		{"partly used", 30, 70},
		{"used up", 100, 0},
		{"used into the next batch", 130, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBank(t, 0, 0, Config{})
			expiring, lasting := b.creditBatch(100, longAgo), b.creditBatch(100, nil)
			if tc.charge > 0 {
				b.charge(tc.charge)
			}
			if got := b.expire(); got != tc.want {
				t.Fatalf("written off %d, want %d", got, tc.want)
			}
			if got := b.expire(); got != 0 {
				t.Errorf("a second run wrote off %d", got)
			}
			b.wantExpired(tc.charge, tc.want, expiring, lasting)
			b.balanced()
		})
	}
}

// wantExpired checks the books after a write-off of want from the batch expiring, once a charge had been made: the
// balance, what each batch holds, and the expiry transaction and its entries.
func (b *bank) wantExpired(charge, want int64, expiring, lasting uuid.UUID) {
	b.t.Helper()
	if n := b.count("SELECT balance FROM billing.balances"); n != 200-charge-want {
		b.t.Errorf("balance %d, want %d", n, 200-charge-want)
	}
	if n := b.batchLeft(expiring); n != 0 {
		b.t.Errorf("the expired batch holds %d", n)
	}
	if n := b.batchLeft(lasting); n != 100-max(charge-100, 0) {
		b.t.Errorf("the batch that never expires holds %d", n)
	}
	if n := b.count("SELECT count(*) FROM billing.transactions WHERE kind = 'expire'"); n != boolInt(want > 0) {
		b.t.Errorf("%d expiry transactions, want %d", n, boolInt(want > 0))
	}
	if n := b.count("SELECT coalesce(sum(amount), 0) FROM billing.entries WHERE book = 'credit_expired'"); n != -want {
		b.t.Errorf("credit_expired holds %d, want %d", n, -want)
	}
}

// TestExpireLeavesWhatIsNotDue: a batch that has not reached its expiry, or has none, is not touched.
func TestExpireLeavesWhatIsNotDue(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	b.creditBatch(100, farAhead)
	b.creditBatch(100, nil)
	if got := b.expire(); got != 0 {
		t.Errorf("written off %d", got)
	}
	if n := b.count("SELECT balance FROM billing.balances"); n != 200 {
		t.Errorf("balance %d, want 200", n)
	}
}

// TestExpireKeepsReservationsPayable: a write-off leaves the balance no lower than the reservations admitted before it,
// so each of them still settles in full and the platform bears nothing; what the cap held back is written off by a later
// run, once the reservation is settled. A floor below zero is an allowance for charges and the write-off does not use
// it: the user is not put in debt for credit that lapsed.
func TestExpireKeepsReservationsPayable(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		floor, settle         int64
		first, second, closed int64 // written off before and after the settlement; the balance at the end
	}{
		{"a reservation settled in part", 0, 150, 100, 50, 0},
		{"a reservation settled in full", 0, 200, 100, 0, 0},
		{"a floor below zero does not let more lapse", -50, 200, 100, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBank(t, 0, tc.floor, Config{})
			b.creditBatch(300, longAgo)
			h := b.hold(100) // reserves 200
			if got := b.expire(); got != tc.first {
				t.Fatalf("written off %d with the call in flight, want %d", got, tc.first)
			}
			b.consistent()
			f, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, tc.settle, 0, 0)))
			if err != nil || f.Charged != platform.AmountExact(tc.settle) || b.borne() != 0 {
				t.Fatalf("Settle = %+v, %v; borne %d; want %d charged and nothing borne", f, err, b.borne(), tc.settle)
			}
			if got := b.expire(); got != tc.second {
				t.Errorf("written off %d after the call, want %d", got, tc.second)
			}
			if balance, debt := b.count("SELECT balance FROM billing.balances"), b.count("SELECT unfunded FROM billing.balances"); balance != tc.closed || debt != 0 {
				t.Errorf("balance %d and debt %d, want %d and none", balance, debt, tc.closed)
			}
			b.balanced()
		})
	}
}

// TestExpireStopsAtAReturn: a charge given back is not allocated yet (#277), so the account whose ledger holds one is not
// written off against: the run counts it as failed, loudly, writes nothing, and the other accounts are written off as
// usual.
func TestExpireStopsAtAReturn(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	b.creditBatch(100, longAgo)
	b.charge(60)
	b.reverse(20)
	other := b.accountWithCredit()
	changed, failed, err := b.expirer().run(t.Context())
	if err != nil || changed != 1 || failed != 1 {
		t.Fatalf("run = %d changed, %d failed, %v; want the other account changed and this one failed", changed, failed, err)
	}
	if n := b.count("SELECT count(*) FROM billing.transactions WHERE kind = 'expire' AND account_id = '" + b.admitted.Account.String() + "'"); n != 0 {
		t.Errorf("%d expiry transactions written for the account with a return", n)
	}
	if n := b.balanceOf(other.Account); n != 1 {
		t.Errorf("the other account holds %d, want the 1 that does not expire", n)
	}
}

// reverse gives n back to the account as the reversal of a charge does, under the account's lock.
func (b *bank) reverse(n int64) {
	b.t.Helper()
	e := event{site: b.admitted.Site, account: b.admitted.Account, kind: "reverse", request: uuid.NewV7(), revision: lateRevision}
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		if _, err := lockAccount(b.t.Context(), tx, e.account); err != nil {
			return err
		}
		_, err := post(b.t.Context(), tx, e, reversal(n, n)...)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
}

// TestExpireRacesSettle: a write-off and a settlement of the same account at once, in whichever order they take the
// lock, both go through: the call is charged in full, nothing is borne, the floor holds, and the credit is gone in the
// end, either spent or written off.
func TestExpireRacesSettle(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	for round := range 15 {
		b.creditBatch(300, longAgo)
		h, o := b.hold(100), outcome(Committed, FinishStop, used(t, 150, 0, 0))
		var wg sync.WaitGroup
		var settleErr, expireErr error
		var f Final
		wg.Go(func() { f, settleErr = b.billing.Settle(t.Context(), h, o) })
		wg.Go(func() { _, expireErr = b.expirer().expire(t.Context(), b.admitted.Account) })
		wg.Wait()
		if settleErr != nil || expireErr != nil || f.Charged != platform.AmountExact(150) {
			t.Fatalf("round %d: Settle = %+v, %v; expiry %v", round, f, settleErr, expireErr)
		}
		b.consistent()
		b.expire()
		if n := b.count("SELECT balance FROM billing.balances"); n != 0 || b.borne() != 0 {
			t.Fatalf("round %d: balance %d, borne %d, want both 0", round, n, b.borne())
		}
	}
	b.balanced()
}

// TestChargesTakeEachUnitFromOneBatch: charges settled at once against several batches are allocated, by one replay,
// to the batches in expiry order, every unit to exactly one of them; and two replays at once change nothing twice.
func TestChargesTakeEachUnitFromOneBatch(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	soon, later, never := b.creditBatch(100, farAhead), b.creditBatch(100, muchLater), b.creditBatch(800, nil)
	holds := make([]*Hold, 10)
	for i := range holds {
		holds[i] = b.hold(0)
	}
	o := outcome(Committed, FinishStop, used(t, 30, 0, 0))
	var wg sync.WaitGroup
	errs := make([]error, len(holds))
	for i, h := range holds {
		wg.Go(func() { _, errs[i] = b.billing.Settle(t.Context(), h, o) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		wg.Go(func() {
			if _, err := b.expirer().expire(t.Context(), b.admitted.Account); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	b.expire()
	// 300 charged: the batch that expires first, then the next, then 100 of the one that does not.
	if got := []int64{b.batchLeft(soon), b.batchLeft(later), b.batchLeft(never)}; !slices.Equal(got, []int64{0, 0, 700}) {
		t.Errorf("the batches hold %v, want [0 0 700]", got)
	}
	if n := b.count("SELECT sum(consumed) FROM billing.credit_batches"); n != 300 {
		t.Errorf("%d consumed, want the 300 charged", n)
	}
	b.balanced()
}

// TestReplayWhileCharging: the replay runs, in pages and without the lock, while calls are settled, and what it stores
// in the end is what a replay of the ledger from nothing gives: no movement is missed or replayed twice, whatever the
// order the two interleave in.
func TestReplayWhileCharging(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	b.creditBatch(100_000, farAhead)
	b.creditBatch(100_000, nil)
	e := b.expirer()
	e.movements = 3
	var wg sync.WaitGroup
	var charged int64
	var chargeErr error
	wg.Go(func() {
		for i := range int64(40) {
			h, err := b.tryHold(0)
			if err == nil {
				_, err = b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 1+i, 0, 0)))
			}
			if err != nil {
				chargeErr = err
				return
			}
			charged += 1 + i
		}
	})
	for range 25 {
		if _, err := e.expire(t.Context(), b.admitted.Account); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if chargeErr != nil {
		t.Fatal(chargeErr)
	}
	b.expire()
	if n := b.count("SELECT sum(consumed) FROM billing.credit_batches"); n != charged {
		t.Errorf("%d consumed, want the %d charged", n, charged)
	}
	b.balanced()
}

// TestExpireRefusesAnAllocationThatDisagrees: a balance the batches do not fund, which no code here makes, is not
// written off against: the run fails and writes nothing.
func TestExpireRefusesAnAllocationThatDisagrees(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	b.creditBatch(100, longAgo)
	b.sql("UPDATE billing.balances SET balance = balance + 1")
	if _, err := b.expirer().expire(t.Context(), b.admitted.Account); err == nil {
		t.Error("the run went through")
	}
	if n := b.count("SELECT count(*) FROM billing.transactions WHERE kind = 'expire'"); n != 0 {
		t.Errorf("%d expiry transactions written", n)
	}
}
