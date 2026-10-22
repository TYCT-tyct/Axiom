package billing

import (
	"context"
	"math"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

// The admission counter is the sum of the ledger's cumulative event totals, independent of batch consumption.
func checkFrozenEvents(t *testing.T, a *allocation, log []movement) {
	t.Helper()
	totals := make(map[uuid.UUID][3]int64)
	for _, m := range log {
		v := totals[m.request]
		switch m.kind {
		case "freeze":
			v[0] = m.revision
		case "unfreeze":
			v[1] = m.revision
		case "refund":
			v[2] = m.revision
		default:
			continue
		}
		totals[m.request] = v
	}
	var frozen int64
	for _, v := range totals {
		frozen += v[0] - v[1] - v[2]
	}
	if got := a.frozen(); got != frozen {
		t.Fatalf("batch freezes %d, event totals freeze %d", got, frozen)
	}
}

func TestFrozenRoomExtremes(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  LockBalanceRow
		held int64
		want int64
	}{
		{"all frozen", LockBalanceRow{Balance: math.MaxInt64, Frozen: math.MaxInt64}, 0, 0},
		{"subtraction does not wrap", LockBalanceRow{Frozen: math.MaxInt64, Reserved: math.MaxInt64}, 0, 0},
		{"own hold remains payable", LockBalanceRow{Balance: math.MaxInt64, Frozen: math.MaxInt64 - 10, Reserved: 10}, 10, 10},
		{"floor allowance saturates", LockBalanceRow{Balance: math.MaxInt64, Frozen: math.MaxInt64 - 10, Floor: math.MinInt64}, 0, math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := room(tc.row, tc.held); got != tc.want {
				t.Fatalf("room = %d, want %d", got, tc.want)
			}
		})
	}
}

// Earlier charges must see the freeze at their own position; refunds name their batch instead of spending by expiry.
func TestRefundReplayHistory(t *testing.T) {
	paid, gift := batchRow(1, 100, "paid", nil), batchRow(2, 100, "gift", nil)
	log := []movement{
		creditOf(paid), creditOf(gift),
		settled(3, "settle", 30),
		{key: keyAt(4), kind: "freeze", request: paid.ID, revision: 80},
		settled(5, "settle", 100),
		{key: keyAt(6), kind: "unfreeze", request: paid.ID, revision: 30},
		{key: keyAt(7), kind: "freeze", request: paid.ID, revision: 100},
		{key: keyAt(8), kind: "refund", request: paid.ID, revision: 70, debit: 70},
	}
	rows := []LiveBatchesRow{paid, gift}
	a := newAllocation(AllocationStateRow{}, rows)
	applyAll(t, a, log...)
	p := a.batches[paid.ID]
	if p.consumed != 20 || p.refunded != 70 || p.remaining() != 10 || a.unfunded != 0 || a.frozen() != 0 {
		t.Fatalf("paid batch %+v, debt %d, frozen %d", *p, a.unfunded, a.frozen())
	}
	if a.batches[gift.ID].consumed != 100 {
		t.Fatal("refund took the gift batch")
	}
	checkReplays(t, rows, log, a)
}

func TestRefundReplayRejectsInvalidEvents(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		revision   int64
		debit      int64
	}{
		{"nonmonotone freeze", "freeze", 50, 0},
		{"freeze beyond available", "freeze", 101, 0},
		{"freeze with entries", "freeze", 75, 25},
		{"zero release", "unfreeze", 0, 0},
		{"negative release", "unfreeze", -1, 0},
		{"release beyond frozen", "unfreeze", 51, 0},
		{"release with entries", "unfreeze", 25, -25},
		{"refund beyond frozen", "refund", 51, 51},
		{"refund different debit", "refund", 25, 24},
		{"negative revision", "refund", math.MinInt64, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paid := batchRow(1, 100, "paid", nil)
			a := newAllocation(AllocationStateRow{}, []LiveBatchesRow{paid})
			applyAll(t, a, creditOf(paid), movement{key: keyAt(2), kind: "freeze", request: paid.ID, revision: 50})
			m := movement{key: keyAt(3), kind: tc.kind, request: paid.ID, revision: tc.revision, debit: tc.debit}
			if err := a.apply(m); err == nil {
				t.Fatalf("accepted %+v", m)
			}
		})
	}
}

// Repeated freeze/release cycles can reach the ledger limit even with a tiny batch. Refuse before issuing SQL.
func TestFreezeCumulativeOverflow(t *testing.T) {
	a := &allocation{}
	f := Freeze{Account: uuid.NewV7(), Batch: uuid.NewV7(), Amount: 1}
	if err := a.postRefund(context.Background(), pg.Tx{}, f, uuid.NewV7(), "freeze", math.MaxInt64, nil); err == nil {
		t.Fatal("cumulative freeze wrapped")
	}
}

func TestFreezeEventsLockTheBalance(t *testing.T) {
	for _, kind := range []string{"freeze", "unfreeze"} {
		t.Run(kind, func(t *testing.T) {
			b := newBank(t, 0, 0, Config{})
			e := event{site: b.admitted.Site, account: b.admitted.Account, kind: kind, request: uuid.NewV7(), revision: 1}
			posted, release, done := make(chan error, 1), make(chan struct{}), make(chan error, 1)
			go func() {
				done <- b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
					_, err := post(t.Context(), tx, e)
					posted <- err
					<-release
					return err
				})
			}()
			err := <-posted
			if err == nil {
				err = b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
					_, err := tx.Exec(t.Context(), "SELECT 1 FROM billing.balances WHERE account_id = $1 FOR UPDATE NOWAIT", e.account)
					return err
				})
				if pg.SQLState(err) != "55P03" {
					t.Errorf("zero-entry %s did not lock the balance: %v", kind, err)
				}
			} else {
				t.Error(err)
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
