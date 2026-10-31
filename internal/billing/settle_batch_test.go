package billing

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestSettleBatchBasics(t *testing.T) {
	t.Parallel()
	b := newBank(t, 10_000, 0, Config{})
	holds := []*Hold{b.hold(1000), b.hold(2000), b.hold(3000)}
	o := outcome(Committed, FinishStop, used(t, 50, 20, 1))
	items := make([]Settlement, len(holds))
	for i, h := range holds {
		items[i] = Settlement{Hold: h, Outcome: o}
	}
	finals, afters, err := b.billing.SettleBatch(t.Context(), b.admitted.Scope, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(finals) != len(items) || len(afters) != len(items) {
		t.Fatalf("got %d finals and %d reports for %d items", len(finals), len(afters), len(items))
	}
	for i := range finals {
		if finals[i].Charged != finals[0].Charged {
			t.Errorf("item %d charged %d, item 0 charged %d", i, finals[i].Charged, finals[0].Charged)
		}
		afters[i]()
	}
	for _, h := range holds {
		if n := b.count(fmt.Sprintf("SELECT count(*) FROM billing.reservations WHERE request_id = '%s' AND state = 'settled'", h.id)); n != 1 {
			t.Errorf("reservation %s is not settled", h.id)
		}
	}
	// The batch inserts its events in item order, so the settles' sequence follows the batch's order.
	var got string
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT string_agg(request_id::text, ',' ORDER BY seq)
			FROM billing.transactions WHERE kind = 'settle' AND request_id = ANY($1::uuid[])`,
			[]uuid.UUID{holds[0].id, holds[1].id, holds[2].id}).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%s,%s,%s", holds[0].id, holds[1].id, holds[2].id); got != want {
		t.Errorf("settle sequence %s, want %s", got, want)
	}
	b.balanced()
}

// TestSettleBatchAndSettleShareTheAccountLock: a single settlement and a batch overlap on one account. The batch's
// balance lock is the single one, so whichever goes first writes, and the other sees its terminal reservation and writes
// nothing. The test holds the row explicitly so both are waiting before either starts.
func TestSettleBatchAndSettleShareTheAccountLock(t *testing.T) {
	t.Parallel()
	b := newBank(t, 10_000, 0, Config{})
	h1, h2 := b.hold(1000), b.hold(2000)
	o := outcome(Committed, FinishStop, used(t, 300, 0, 0))
	release := b.holdLock(b.admitted.Account)
	defer release()

	var wg sync.WaitGroup
	var batchFinals []Final
	var batchErr error
	var single Final
	var singleErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		batchFinals, _, batchErr = b.billing.SettleBatch(t.Context(), b.admitted.Scope, []Settlement{{Hold: h1, Outcome: o}, {Hold: h2, Outcome: o}})
	}()
	go func() {
		defer wg.Done()
		single, singleErr = b.billing.Settle(t.Context(), h2, o)
	}()
	b.waitFor(`SELECT count(*) FROM pg_stat_activity
		WHERE wait_event_type = 'Lock' AND query LIKE '%FROM billing.balances%'`, 2)
	release()
	wg.Wait()
	if batchErr != nil || singleErr != nil {
		t.Fatalf("batch %v, single %v", batchErr, singleErr)
	}
	if len(batchFinals) != 2 {
		t.Fatalf("batch returned %d finals", len(batchFinals))
	}
	if single.Charged != batchFinals[1].Charged {
		t.Errorf("the single settlement charged %d, the batch's second %d", single.Charged, batchFinals[1].Charged)
	}
	for _, h := range []*Hold{h1, h2} {
		if n := b.count(fmt.Sprintf("SELECT count(*) FROM billing.transactions WHERE request_id = '%s' AND kind = 'settle'", h.id)); n != 1 {
			t.Errorf("request %s has %d settle events", h.id, n)
		}
	}
	b.balanced()
}

func TestSettleBatchMatchesSettle(t *testing.T) {
	t.Parallel()

	t.Run("the cap follows the balance the earlier items leave", func(t *testing.T) {
		t.Parallel()
		o := outcome(Committed, FinishStop, used(t, 100, 0, 0)) // price 100, holds 10 each
		batch := newBank(t, 25, 0, Config{})
		holds := []*Hold{batch.holdOn(batch.exact, 10), batch.holdOn(batch.exact, 10)}
		got := settleBatch(t, batch, holds, []Outcome{o, o})
		want := []int64{15, 10}
		if !slices.Equal(got, want) {
			t.Errorf("batch charged %v, want %v", got, want)
		}
		batch.balanced()

		single := newBank(t, 25, 0, Config{})
		holds = []*Hold{single.holdOn(single.exact, 10), single.holdOn(single.exact, 10)}
		one := settleOne(t, single, holds, []Outcome{o, o})
		if !slices.Equal(got, one) {
			t.Errorf("batch charged %v, one by one charged %v", got, one)
		}
		single.balanced()
	})

	t.Run("a late settlement and a pending one in one batch", func(t *testing.T) {
		t.Parallel()
		b := newBank(t, 10_000, 0, Config{})
		late, pending := b.hold(1000), b.hold(2000)
		b.sql(fmt.Sprintf("UPDATE billing.reservations SET deadline = clock_timestamp() - interval '1 hour' WHERE request_id = '%s'", late.id))
		if _, err := b.billing.Cleanup(t.Context(), 10); err != nil {
			t.Fatal(err)
		}
		if n := b.count(fmt.Sprintf("SELECT count(*) FROM billing.reservations WHERE request_id = '%s' AND state = 'cleanup_released'", late.id)); n != 1 {
			t.Fatalf("the late reservation is not cleanup_released")
		}
		o := outcome(Committed, FinishStop, used(t, 300, 0, 0))
		got := settleBatch(t, b, []*Hold{late, pending}, []Outcome{o, o})
		if len(got) != 2 || got[0] != 300 || got[1] != 300 {
			t.Errorf("batch charged %v, want [300 300]", got)
		}
		for _, h := range []*Hold{late, pending} {
			if n := b.count(fmt.Sprintf("SELECT count(*) FROM billing.reservations WHERE request_id = '%s' AND state = 'settled'", h.id)); n != 1 {
				t.Errorf("reservation %s is not settled", h.id)
			}
		}
		b.balanced()

		one := newBank(t, 10_000, 0, Config{})
		late2, pending2 := one.hold(1000), one.hold(2000)
		one.sql(fmt.Sprintf("UPDATE billing.reservations SET deadline = clock_timestamp() - interval '1 hour' WHERE request_id = '%s'", late2.id))
		if _, err := one.billing.Cleanup(t.Context(), 10); err != nil {
			t.Fatal(err)
		}
		gotOne := settleOne(t, one, []*Hold{late2, pending2}, []Outcome{o, o})
		if !slices.Equal(got, gotOne) {
			t.Errorf("batch charged %v, one by one charged %v", got, gotOne)
		}
		one.balanced()
	})

	t.Run("a released reservation and a retried batch write nothing more", func(t *testing.T) {
		t.Parallel()
		b := newBank(t, 10_000, 0, Config{})
		released, settled := b.hold(1000), b.hold(2000)
		o := outcome(Committed, FinishStop, used(t, 50, 20, 1))
		got := settleBatch(t, b, []*Hold{released, settled}, []Outcome{{}, o})
		if got[0] != 0 {
			t.Errorf("the released reservation charged %d", got[0])
		}
		before := b.count("SELECT count(*) FROM billing.transactions")
		again := settleBatch(t, b, []*Hold{released, settled}, []Outcome{{}, o})
		if !slices.Equal(got, again) {
			t.Errorf("the retried batch charged %v, first %v", again, got)
		}
		if after := b.count("SELECT count(*) FROM billing.transactions"); after != before {
			t.Errorf("the retried batch wrote %d transactions, want none", after-before)
		}
		b.balanced()
	})
}

// settleBatch settles holds in one batch and returns their charges.
func settleBatch(t *testing.T, b *bank, holds []*Hold, outcomes []Outcome) []int64 {
	t.Helper()
	items := make([]Settlement, len(holds))
	for i := range holds {
		items[i] = Settlement{Hold: holds[i], Outcome: outcomes[i]}
	}
	finals, afters, err := b.billing.SettleBatch(t.Context(), b.admitted.Scope, items)
	if err != nil {
		t.Fatal(err)
	}
	charges := make([]int64, len(finals))
	for i := range finals {
		charges[i] = finals[i].Charged.CeilAmount()
		afters[i]()
	}
	return charges
}

// settleOne settles holds one by one and returns their charges.
func settleOne(t *testing.T, b *bank, holds []*Hold, outcomes []Outcome) []int64 {
	t.Helper()
	charges := make([]int64, len(holds))
	for i, h := range holds {
		f, err := b.billing.Settle(t.Context(), h, outcomes[i])
		if err != nil {
			t.Fatal(err)
		}
		charges[i] = f.Charged.CeilAmount()
	}
	return charges
}
