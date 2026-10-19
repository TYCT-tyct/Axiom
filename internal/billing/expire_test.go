package billing

import (
	"bytes"
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

// expire runs the expiry of the bank's account and checks that what it stored is what a replay of the whole ledger
// from nothing gives.
func (b *bank) expire() int64 {
	b.t.Helper()
	var written int64
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		written, err = ExpireAccount(b.t.Context(), tx, b.admitted.Account)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	fresh := b.replayed()
	var stored struct{ seq, unfunded, unbatched int64 }
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		return tx.QueryRow(b.t.Context(), "SELECT allocated_seq, unfunded, unbatched FROM billing.balances").Scan(&stored.seq, &stored.unfunded, &stored.unbatched)
	}); err != nil {
		b.t.Fatal(err)
	}
	if stored.seq != fresh.seq || stored.unfunded != fresh.unfunded || stored.unbatched != fresh.unbatched {
		b.t.Errorf("stored allocation %+v, a replay from nothing gives seq %d unfunded %d unbatched %d", stored, fresh.seq, fresh.unfunded, fresh.unbatched)
	}
	for id, batch := range fresh.batches {
		var consumed, expired int64
		if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
			return tx.QueryRow(b.t.Context(), "SELECT consumed, expired FROM billing.credit_batches WHERE id = $1", id).Scan(&consumed, &expired)
		}); err != nil {
			b.t.Fatal(err)
		}
		if consumed != batch.consumed || expired != batch.expired {
			b.t.Errorf("batch %s stores consumed %d expired %d, a replay from nothing gives %d %d", id, consumed, expired, batch.consumed, batch.expired)
		}
	}
	return written
}

// replayed replays the bank's whole ledger onto its batches as the credit transactions made them, and checks the
// invariants that hold at any time: the replay funds exactly the balance, and no batch is over-spent.
func (b *bank) replayed() *allocation {
	b.t.Helper()
	var a *allocation
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		rows, err := tx.Query(b.t.Context(), `SELECT b.id, b.site_id, b.source, b.expires_at, b.amount, 0::bigint, 0::bigint, t.seq
			FROM billing.credit_batches b JOIN billing.transactions t
			ON t.request_id = b.id AND t.kind = 'credit' AND t.attempt_seq = 0 AND t.revision = 0`)
		if err != nil {
			return err
		}
		defer rows.Close()
		var batches []UnspentBatchesRow
		for rows.Next() {
			var r UnspentBatchesRow
			if err := rows.Scan(&r.ID, &r.SiteID, &r.Source, &r.ExpiresAt, &r.Amount, &r.Consumed, &r.Expired, &r.Seq); err != nil {
				return err
			}
			batches = append(batches, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		a = newAllocation(AllocationStateRow{}, batches)
		return a.catchUp(b.t.Context(), tx, b.admitted.Account)
	}); err != nil {
		b.t.Fatal(err)
	}
	if got, want := a.funded(), b.count("SELECT balance FROM billing.balances"); got != want {
		b.t.Errorf("the batches fund %d of a balance of %d", got, want)
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
			if n := b.count("SELECT balance FROM billing.balances"); n != 200-tc.charge-tc.want {
				t.Errorf("balance %d, want %d", n, 200-tc.charge-tc.want)
			}
			if n := b.batchLeft(expiring); n != 0 {
				t.Errorf("the expired batch holds %d", n)
			}
			if n := b.batchLeft(lasting); n != 100-max(tc.charge-100, 0) {
				t.Errorf("the batch that never expires holds %d", n)
			}
			wantTx := boolInt(tc.want > 0)
			if n := b.count("SELECT count(*) FROM billing.transactions WHERE kind = 'expire'"); n != wantTx {
				t.Errorf("%d expiry transactions, want %d", n, wantTx)
			}
			if n := b.count("SELECT coalesce(sum(amount), 0) FROM billing.entries WHERE book = 'credit_expired'"); n != -tc.want {
				t.Errorf("credit_expired holds %d, want %d", n, -tc.want)
			}
			b.balanced()
		})
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

// TestExpireKeepsReservationsPayable: a write-off leaves the balance no lower than the reservations admitted before it
// and the floor allow, so each of them still settles in full and the platform bears nothing; what the cap held back is
// written off by a later run, once the reservation is settled.
func TestExpireKeepsReservationsPayable(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		floor, settle         int64
		first, second, closed int64 // written off before and after the settlement; the balance at the end
	}{
		{"a reservation settled in part", 0, 150, 100, 50, 0},
		{"a reservation settled in full", 0, 200, 100, 0, 0},
		{"a floor below zero lets more lapse, and the call still pays in full", -50, 200, 150, 0, -50},
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
			if n := b.count("SELECT balance FROM billing.balances"); n != tc.closed {
				t.Errorf("balance %d, want %d", n, tc.closed)
			}
			b.balanced()
		})
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
		wg.Go(func() {
			expireErr = b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) (err error) {
				_, err = ExpireAccount(t.Context(), tx, b.admitted.Account)
				return err
			})
		})
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
		wg.Go(func() { b.expireNoCheck() })
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

func (b *bank) expireNoCheck() {
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		_, err := ExpireAccount(b.t.Context(), tx, b.admitted.Account)
		return err
	}); err != nil {
		b.t.Error(err)
	}
}

// TestExpireRefusesAnAllocationThatDisagrees: a balance the batches do not fund, which no code here makes, is not
// written off against: the run fails and writes nothing.
func TestExpireRefusesAnAllocationThatDisagrees(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	b.creditBatch(100, longAgo)
	b.sql("UPDATE billing.balances SET balance = balance + 1")
	err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		_, err := ExpireAccount(t.Context(), tx, b.admitted.Account)
		return err
	})
	if err == nil {
		t.Error("the run went through")
	}
	if n := b.count("SELECT count(*) FROM billing.transactions WHERE kind = 'expire'"); n != 0 {
		t.Errorf("%d expiry transactions written", n)
	}
}

// TestExpireDue: the run goes through every account with credit due and writes each off; one it cannot write off is
// reported and does not hold the others back.
func TestExpireDue(t *testing.T) {
	var logs bytes.Buffer
	b := newBank(t, 0, 0, Config{})
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	other := b.secondTenant(10)
	b.creditBatch(100, longAgo)
	b.addCreditTo(other, Credit{ID: uuid.NewV7(), Amount: 70, ExpiresAt: longAgo})
	b.addCreditTo(other, Credit{ID: uuid.NewV7(), Amount: 5, ExpiresAt: farAhead})
	changed, err := ExpireDue(t.Context(), b.app, b.platform, logger)
	if err != nil || changed != 2 {
		t.Fatalf("ExpireDue = %d, %v; want 2 accounts", changed, err)
	}
	if n := b.count("SELECT sum(balance) FROM billing.balances"); n != 15 {
		t.Errorf("balances add up to %d, want the 15 that is not due", n)
	}
	if changed, err := ExpireDue(t.Context(), b.app, b.platform, logger); err != nil || changed != 0 {
		t.Errorf("a second run = %d, %v; want nothing", changed, err)
	}

	b.creditBatch(40, longAgo)
	b.addCreditTo(other, Credit{ID: uuid.NewV7(), Amount: 30, ExpiresAt: longAgo})
	b.sql("UPDATE billing.balances SET balance = balance + 1 WHERE account_id = '" + b.admitted.Account.String() + "'")
	changed, err = ExpireDue(t.Context(), b.app, b.platform, logger)
	if err == nil || changed != 1 {
		t.Errorf("ExpireDue = %d, %v; want the other account written off and the broken one reported", changed, err)
	}
}
