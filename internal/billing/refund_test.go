package billing

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

func (b *bank) refundable(amount int64) Freeze {
	b.t.Helper()
	p := b.paid(amount, Payment{Currency: currency(b.t, "USD"), Paid: 1000}, uuid.UUID{})
	if err := b.creditPaid(p); err != nil {
		b.t.Fatal(err)
	}
	return Freeze{Account: p.Account, Batch: p.ID, Amount: amount}
}

func (b *bank) freeze(f Freeze) int64 {
	b.t.Helper()
	var n int64
	if err := PrepareRefund(b.t.Context(), b.app, b.platform, f); err != nil {
		b.t.Fatal(err)
	}
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		n, err = FreezeRefund(b.t.Context(), tx, f)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	b.checkStored(f.Account)
	return n
}

func (b *bank) finishRefund(f Freeze, withFee, release bool) RefundAmounts {
	b.t.Helper()
	var amounts RefundAmounts
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		if release {
			return ReleaseRefund(b.t.Context(), tx, f)
		}
		r := Refund{Freeze: f, WithFee: withFee}
		var err error
		amounts, err = QuoteRefund(b.t.Context(), tx, r)
		if err != nil {
			return err
		}
		return SettleRefund(b.t.Context(), tx, r)
	}); err != nil {
		b.t.Fatal(err)
	}
	b.checkStored(f.Account)
	b.balanced()
	return amounts
}

func TestFreezeRefundRoom(t *testing.T) {
	for _, tc := range []struct {
		name          string
		reserved      int64
		expired, gift bool
		want          int64
	}{
		{"remaining batch caps it", 0, false, false, 500},
		{"reservations stay payable", 300, false, false, 200},
		{"the floor cannot fund a refund", 550, false, false, 0},
		{"expired even before write-off", 0, true, false, 0},
		{"gifts cannot be refunded", 0, false, true, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBank(t, 0, -100, Config{})
			b.checkFreezeRoom(tc.reserved, tc.expired, tc.gift, tc.want)
		})
	}
}

func (b *bank) checkFreezeRoom(reserved int64, expired, gift bool, want int64) {
	b.t.Helper()
	f := b.refundable(500)
	f.Amount = 600
	if gift {
		f.Batch = b.creditBatch(500, nil)
	}
	if expired {
		b.sql("UPDATE billing.credit_batches SET expires_at = clock_timestamp() - interval '1 second'")
	}
	if reserved > 0 {
		if _, err := b.reserve(b.admitted, reserved); err != nil {
			b.t.Fatal(err)
		}
	}
	var got int64
	err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		got, err = FreezeRefund(b.t.Context(), tx, f)
		return err
	})
	if want < 0 {
		if err == nil {
			b.t.Fatal("gift accepted for refund")
		}
		return
	}
	if err != nil || got != want {
		b.t.Fatalf("freeze = %d, %v; want %d", got, err, want)
	}
	if again := b.freeze(f); again != 0 {
		b.t.Fatalf("froze %d more with no refundable room", again)
	}
	if got := b.count("SELECT frozen FROM billing.balances"); got != want {
		b.t.Fatalf("balance froze %d, want %d", got, want)
	}
	b.consistent()
	b.balanced()
}

// Expiry takes only unfrozen credit; release after expiry permits the next run to write it off.
func TestRefundAcrossExpiry(t *testing.T) {
	for _, release := range []bool{false, true} {
		t.Run(fmt.Sprint(release), func(t *testing.T) {
			b := newBank(t, 0, 0, Config{})
			b.refundAcrossExpiry(release)
		})
	}
}

func (b *bank) refundAcrossExpiry(release bool) {
	b.t.Helper()
	f := b.refundable(200)
	f.Amount = 150
	if n := b.freeze(f); n != 150 {
		b.t.Fatalf("froze %d, want 150", n)
	}
	b.sql("UPDATE billing.credit_batches SET expires_at = clock_timestamp() - interval '1 second'")
	if got := b.expire(); got != 50 {
		b.t.Fatalf("expired %d, want only the 50 unfrozen", got)
	}
	if got := b.expire(); got != 0 {
		b.t.Fatalf("second expiry took %d frozen credits", got)
	}
	b.finishRefund(f, false, release)
	want := int64(0)
	if release {
		want = 150
	}
	if got := b.expire(); got != want {
		b.t.Fatalf("after resolving freeze, expired %d, want %d", got, want)
	}
	if b.batchLeft(f.Batch) != 0 || b.balanceOf(f.Account) != 0 {
		b.t.Fatal("resolved and expired batch still funds credit")
	}
	for _, e := range b.ledgerOf(f.Batch) {
		if (e.Kind == "freeze" || e.Kind == "unfreeze") && len(e.Entries) != 0 {
			b.t.Fatalf("%s moved money: %+v", e.Kind, e)
		}
	}
}

func TestExpiryRespectsFrozenAndReserved(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(800)
	b.freeze(f)
	b.creditBatch(200, nil)
	h := b.hold(0)
	// The fully frozen batch is first, so expiry must also skip it and continue to the gift.
	b.sql("UPDATE billing.credit_batches SET expires_at = clock_timestamp() - CASE WHEN source = 'paid' THEN interval '2 minutes' ELSE interval '1 minute' END")
	if got := b.expire(); got != 100 {
		t.Fatalf("expired %d, want 100 around the freeze and reservation", got)
	}
	if got := b.expire(); got != 0 {
		t.Fatalf("repeated expiry took %d from protected credit", got)
	}
	final, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 100, 0, 0)))
	if err != nil || final.Charged != platform.AmountExact(100) {
		t.Fatalf("protected reservation = %+v, %v", final, err)
	}
	b.expire()
	if b.batchLeft(f.Batch) != 800 || b.balanceOf(f.Account) != 800 {
		t.Fatal("expiry or settlement spent frozen credit")
	}
	b.balanced()
}

func TestFreezeRevisionContinuesAfterRelease(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(500)
	for range 3 {
		if got := b.freeze(f); got != 500 {
			t.Fatalf("froze %d, want 500 again", got)
		}
		b.finishRefund(f, false, true)
	}
	b.freeze(f)
	f.Amount = 300
	b.finishRefund(f, false, false)
	f.Amount = 200
	b.finishRefund(f, false, true)
	b.freeze(f)
	b.finishRefund(f, false, false)
	if b.batchLeft(f.Batch) != 0 || b.count("SELECT freeze_total FROM billing.credit_batches") != 2200 {
		t.Fatal("cumulative events did not survive repeated release and partial refund")
	}
}

func TestRefundRejectsOtherAccounts(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(500)
	other := uuid.NewV7()
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		return OpenBalance(t.Context(), tx, b.admitted.Site, other, 0)
	}); err != nil {
		t.Fatal(err)
	}
	f.Account = other
	for _, scope := range []pg.Scope{b.platform, pg.NewScope(false, nil, []uuid.UUID{other})} {
		if err := b.app.Tx(t.Context(), scope, func(tx pg.Tx) error {
			_, err := FreezeRefund(t.Context(), tx, f)
			return err
		}); err == nil {
			t.Fatal("another account's batch was accepted")
		}
	}
	if b.count("SELECT sum(frozen) FROM billing.balances") != 0 || len(b.ledgerOf(f.Batch)) != 1 {
		t.Fatal("rejected cross-account request changed money")
	}
}

// A call waiting for an uncommitted freeze must quote the frozen balance after taking the same lock.
func TestFreezeSerializesAdmission(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(1000)
	f.Amount = 800
	done := make(chan error, 1)
	err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		if n, err := FreezeRefund(t.Context(), tx, f); err != nil || n != 800 {
			return fmt.Errorf("freeze = %d: %w", n, err)
		}
		go func() {
			_, err := b.reserve(b.admitted, 201)
			done <- err
		}()
		b.waitForLockWait()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.As(err, new(*Insufficient)) {
		t.Fatalf("waiting admission = %v, want insufficient", err)
	}
	total, refused := b.storm(b.admitted, SourceBalance)
	if total > 200 || len(refused) == 0 {
		t.Fatalf("concurrent admissions reserved %d with %d refused", total, len(refused))
	}
	b.checkStored(f.Account)
	b.consistent()
}

func TestFrozenSettlementFloor(t *testing.T) {
	b := newBank(t, 0, -100, Config{})
	f := b.refundable(1000)
	f.Amount = 800
	b.freeze(f)
	h := b.hold(0)
	if err := b.billing.Extend(t.Context(), h, ExtendRequest{Seq: 2, Target: platform.AmountExact(301)}); !errors.As(err, new(*Insufficient)) {
		t.Fatalf("extension into frozen credit = %v", err)
	}
	final, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 500, 0, 0)))
	if err != nil || final.Charged != platform.AmountExact(300) {
		t.Fatalf("settle = %+v, %v; want 300 down to the unfrozen floor", final, err)
	}
	b.expire()
	if b.batchLeft(f.Batch) != 800 || b.count("SELECT unfunded FROM billing.balances") != 100 {
		t.Fatal("spending into debt took frozen credit")
	}
	f.Amount = 50
	b.finishRefund(f, false, true)
	if b.count("SELECT unfunded FROM billing.balances") != 50 {
		t.Fatal("released credit did not repay debt first")
	}
	f.Amount = 750
	b.finishRefund(f, false, false)
	if b.batchLeft(f.Batch) != 0 || b.balanceOf(f.Account) != -50 {
		t.Fatal("refund did not take exactly its frozen batch")
	}
}

func TestFreezeConcurrentSettlement(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(1000)
	holds := make([]*Hold, 5)
	for i := range holds {
		holds[i] = b.hold(0)
	}
	f.Amount = 800
	if n := b.freeze(f); n != 500 {
		t.Fatalf("froze %d, want 500 around existing reservations", n)
	}
	var wg sync.WaitGroup
	finals, errs := make([]Final, len(holds)), make([]error, len(holds))
	o := outcome(Committed, FinishStop, used(t, 1000, 0, 0))
	for i, h := range holds {
		wg.Go(func() { finals[i], errs[i] = b.billing.Settle(t.Context(), h, o) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	for _, f := range finals {
		if f.Charged != platform.AmountExact(100) {
			t.Fatalf("charged %+v, want exactly its reserved 100", f)
		}
	}
	b.expire()
	if b.batchLeft(f.Batch) != 500 || b.balanceOf(f.Account) != 500 {
		t.Fatal("concurrent settlements spent frozen credit")
	}
	b.consistent()
	b.balanced()
}

func TestFreezeAfterSpending(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(500)
	b.creditBatch(500, nil)
	b.charge(600)
	// The gift pays the first 500; only the paid batch's remaining 400 can be frozen.
	if got := b.freeze(f); got != 400 {
		t.Fatalf("froze %d after spending, want 400", got)
	}
	f.Amount = 400
	b.finishRefund(f, false, false)
	if b.batchLeft(f.Batch) != 0 || b.balanceOf(f.Account) != 0 {
		t.Fatal("refund included consumed credit")
	}
}

func TestConcurrentFreezes(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(500)
	f.Amount = 400
	amounts, errs := make([]int64, 8), make([]error, 8)
	var wg sync.WaitGroup
	for i := range amounts {
		wg.Go(func() {
			errs[i] = b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) (err error) {
				amounts[i], err = FreezeRefund(t.Context(), tx, f)
				return err
			})
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, n := range amounts {
		total += n
	}
	if total != 500 || b.count("SELECT frozen FROM billing.balances") != 500 {
		t.Fatalf("concurrent freezes held %d, want exactly 500", total)
	}
	b.checkStored(f.Account)
	b.balanced()
}

func TestFreezeRollback(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(500)
	abort := errors.New("payment state update failed")
	err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		if _, err := FreezeRefund(t.Context(), tx, f); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) || b.count("SELECT frozen FROM billing.balances") != 0 || len(b.ledgerOf(f.Batch)) != 1 {
		t.Fatalf("freeze survived payment rollback: %v", err)
	}
}

func TestRefundValidation(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(500)
	b.freeze(f)
	for _, r := range []Refund{
		{Account: f.Account, Batch: f.Batch, Amount: 0},
		{Account: f.Account, Batch: f.Batch, Amount: 501},
		{Account: f.Account, Batch: uuid.NewV7(), Amount: 1},
		{Account: f.Account, Batch: f.Batch, Amount: -1},
	} {
		if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
			return SettleRefund(t.Context(), tx, r)
		}); err == nil {
			t.Fatalf("invalid refund accepted: %+v", r)
		}
	}
	b.finishRefund(f, false, false)
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		return SettleRefund(t.Context(), tx, Refund{Freeze: f})
	}); err == nil {
		t.Fatal("fully refunded batch settled twice")
	}
}

func TestRefundRollback(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(500)
	b.freeze(f)
	abort := errors.New("payment state update failed")
	for _, release := range []bool{false, true} {
		err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
			var err error
			if release {
				err = ReleaseRefund(t.Context(), tx, f)
			} else {
				err = SettleRefund(t.Context(), tx, Refund{Freeze: f})
			}
			if err != nil {
				return err
			}
			return abort
		})
		if !errors.Is(err, abort) {
			t.Fatal(err)
		}
		b.checkStored(f.Account)
		if b.count("SELECT frozen FROM billing.balances") != 500 || b.count("SELECT count(*) FROM billing.credit_batches WHERE refund_with_fee IS NOT NULL") != 0 || len(b.ledgerOf(f.Batch)) != 2 {
			t.Fatal("refund or release survived payment rollback")
		}
	}
}

func TestRefundStopsAtFrozenMismatch(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(500)
	f.Amount = 100
	b.freeze(f)
	b.sql("UPDATE billing.balances SET frozen = 99",
		"UPDATE billing.credit_batches SET expires_at = clock_timestamp() - interval '1 second'")
	f.Amount = 1
	for _, kind := range []string{"freeze", "unfreeze", "refund", "expire"} {
		err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
			switch kind {
			case "freeze":
				_, err := FreezeRefund(t.Context(), tx, f)
				return err
			case "unfreeze":
				return ReleaseRefund(t.Context(), tx, f)
			case "refund":
				return SettleRefund(t.Context(), tx, Refund{Freeze: f})
			default:
				_, err := writeOffAccount(t.Context(), tx, f.Account, movementPage)
				return err
			}
		})
		if err == nil {
			t.Fatalf("%s accepted a frozen counter that disagrees with the ledger", kind)
		}
	}
	if len(b.ledgerOf(f.Batch)) != 2 || b.balanceOf(f.Account) != 500 {
		t.Fatal("inconsistent freeze moved money")
	}
}
