package billing

import (
	"errors"
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

// TestSettleBatchIsolatesADataError: EndReservationBatch is one statement, so a data error in one item's spend window
// has no index. The batch must fall back to the single path to name that item, commit the peers it can, and leave the
// peer charged once.
func TestSettleBatchIsolatesADataError(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, 0, Config{})
	failing := b.admitted
	failing.Workspace, failing.Key = uuid.NewV7(), uuid.NewV7()
	failed, err := b.reserve(failing, 1)
	if err != nil {
		t.Fatal(err)
	}
	peer := b.hold(1)
	b.sql(fmt.Sprintf("UPDATE billing.spend SET spent = 9223372036854775807 WHERE subject IN ('%s', '%s')", failing.Workspace, failing.Key))
	o := outcome(Committed, FinishStop, used(t, 1, 0, 0))

	_, _, err = b.billing.SettleBatch(t.Context(), b.admitted.Scope, []Settlement{{Hold: failed, Outcome: o}, {Hold: peer, Outcome: o}})
	var work *BatchWorkError
	if !errors.As(err, &work) || work.Index != 0 {
		t.Fatalf("SettleBatch error %v, want the first item's BatchWorkError", err)
	}
	if state := pg.SQLState(work.Err); state != "22003" {
		t.Errorf("the first item's SQL state %q, want 22003", state)
	}
	// The peer was committed by the fallback; retrying it is the same stored Final and writes nothing more.
	f, err := b.billing.Settle(t.Context(), peer, o)
	if err != nil || f.Charged.CeilAmount() != 1 {
		t.Fatalf("retrying the peer: %+v, %v", f, err)
	}
	if n := b.count(fmt.Sprintf("SELECT count(*) FROM billing.transactions WHERE request_id = '%s' AND kind = 'settle'", peer.id)); n != 1 {
		t.Errorf("the peer has %d settle events, want 1", n)
	}
	if n := b.count(fmt.Sprintf("SELECT count(*) FROM billing.reservations WHERE request_id = '%s' AND state = 'settled'", peer.id)); n != 1 {
		t.Errorf("the peer has %d settled reservations, want 1", n)
	}
	if n := b.count(fmt.Sprintf("SELECT count(*) FROM billing.reservations WHERE request_id = '%s' AND state = 'pending'", failed.id)); n != 1 {
		t.Errorf("the failed item is not still pending")
	}
	if n := b.count(fmt.Sprintf("SELECT count(*) FROM billing.transactions WHERE request_id = '%s'", failed.id)); n != 0 {
		t.Errorf("the failed item left %d ledger events", n)
	}
	if n := b.balanceOf(b.admitted.Account); n != 999 {
		t.Errorf("balance %d, want 999 after the peer's one charge", n)
	}
}

// TestSettleBatchWritesTheExactBalanceAcrossInt64: two late charges of 6e18 each fit int64, and so does their final
// balance of -6e18, but their total does not. Summing the user debits in int64 would clamp the second charge and leave
// the balance above the ledger; the exact final balance must be written instead.
func TestSettleBatchWritesTheExactBalanceAcrossInt64(t *testing.T) {
	t.Parallel()
	const amount int64 = 6_000_000_000_000_000_000
	b := newBank(t, amount, -amount, Config{})
	// 10¹⁰ input tokens at 0.6 credits each price 6e9 credits, or 6e18 billionths: one item's charge and the
	// balance, but not two items' total, fit int64.
	q := Quotable{Price: mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.6","output":"0"}}`), Model: Model{ContextLength: 1e12}}
	reserve := func(a Admitted) *Hold {
		var h *Hold
		if err := b.app.Tx(t.Context(), a.Scope, func(tx pg.Tx) (err error) {
			h, _, err = b.billing.Reserve(t.Context(), tx, a, uuid.NewV7(), Demand{TextBytes: 1}, []Quotable{q})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return h
	}
	a1, a2 := b.admitted, b.admitted
	a1.Workspace, a1.Key = uuid.NewV7(), uuid.NewV7()
	a2.Workspace, a2.Key = uuid.NewV7(), uuid.NewV7()
	h1, h2 := reserve(a1), reserve(a2)
	b.sql(fmt.Sprintf("UPDATE billing.reservations SET deadline = clock_timestamp() - interval '1 hour' WHERE request_id IN ('%s', '%s')", h1.id, h2.id))
	if _, err := b.billing.Cleanup(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	for _, h := range []*Hold{h1, h2} {
		if n := b.count(fmt.Sprintf("SELECT count(*) FROM billing.reservations WHERE request_id = '%s' AND state = 'cleanup_released'", h.id)); n != 1 {
			t.Fatalf("reservation %s is not cleanup_released", h.id)
		}
	}
	o := outcome(Committed, FinishStop, used(t, 10_000_000_000, 0, 0))
	finals, afters, err := b.billing.SettleBatch(t.Context(), b.admitted.Scope, []Settlement{{Hold: h1, Outcome: o}, {Hold: h2, Outcome: o}})
	if err != nil {
		t.Fatal(err)
	}
	for i := range finals {
		if got := finals[i].Charged.CeilAmount(); got != amount {
			t.Errorf("item %d charged %d, want %d", i, got, amount)
		}
		afters[i]()
	}
	if got := b.balanceOf(b.admitted.Account); got != -amount {
		t.Errorf("balance %d, want %d", got, -amount)
	}
	if got := b.count("SELECT coalesce(sum(amount), 0) FROM billing.entries WHERE book = 'user_balance'"); got != amount {
		t.Errorf("user-balance entries sum to %d, want %d", got, amount)
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
	b.waitFor(`SELECT count(DISTINCT pid) FROM pg_stat_activity
		WHERE datname = current_database() AND pid <> pg_backend_pid()
		AND wait_event_type = 'Lock' AND query LIKE '%FROM billing.balances%'`, 2)
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
	t.Run("the cap follows the balance the earlier items leave", batchCapsLikeOneByOne)
	t.Run("a late settlement and a pending one in one batch", batchSettlesLateAndPending)
	t.Run("a released reservation and a retried batch write nothing more", batchRetryWritesNothing)
}

func batchCapsLikeOneByOne(t *testing.T) {
	t.Parallel()
	o := outcome(Committed, FinishStop, used(t, 100, 0, 0)) // price 100, holds 10 each
	batch := newBank(t, 25, 0, Config{})
	holds := []*Hold{batch.holdOn(batch.exact, 10), batch.holdOn(batch.exact, 10)}
	got := settleBatch(t, batch, holds, []Outcome{o, o})
	if want := []int64{15, 10}; !slices.Equal(got, want) {
		t.Errorf("batch charged %v, want %v", got, want)
	}
	batch.balanced()

	single := newBank(t, 25, 0, Config{})
	holds = []*Hold{single.holdOn(single.exact, 10), single.holdOn(single.exact, 10)}
	if one := settleOne(t, single, holds, []Outcome{o, o}); !slices.Equal(got, one) {
		t.Errorf("batch charged %v, one by one charged %v", got, one)
	}
	single.balanced()
}

func batchSettlesLateAndPending(t *testing.T) {
	t.Parallel()
	o := outcome(Committed, FinishStop, used(t, 300, 0, 0))
	b := newBank(t, 10_000, 0, Config{})
	holds := latePending(t, b)
	got := settleBatch(t, b, holds, []Outcome{o, o})
	if want := []int64{300, 300}; !slices.Equal(got, want) {
		t.Errorf("batch charged %v, want %v", got, want)
	}
	for _, h := range holds {
		if n := b.count(fmt.Sprintf("SELECT count(*) FROM billing.reservations WHERE request_id = '%s' AND state = 'settled'", h.id)); n != 1 {
			t.Errorf("reservation %s is not settled", h.id)
		}
	}
	b.balanced()

	one := newBank(t, 10_000, 0, Config{})
	if gotOne := settleOne(t, one, latePending(t, one), []Outcome{o, o}); !slices.Equal(got, gotOne) {
		t.Errorf("batch charged %v, one by one charged %v", got, gotOne)
	}
	one.balanced()
}

// latePending holds 1000 and 2000 and lets cleanup release the first, so its settlement is a late one.
func latePending(t *testing.T, b *bank) []*Hold {
	t.Helper()
	late, pending := b.hold(1000), b.hold(2000)
	b.sql(fmt.Sprintf("UPDATE billing.reservations SET deadline = clock_timestamp() - interval '1 hour' WHERE request_id = '%s'", late.id))
	if _, err := b.billing.Cleanup(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if n := b.count(fmt.Sprintf("SELECT count(*) FROM billing.reservations WHERE request_id = '%s' AND state = 'cleanup_released'", late.id)); n != 1 {
		t.Fatalf("the late reservation is not cleanup_released")
	}
	return []*Hold{late, pending}
}

func batchRetryWritesNothing(t *testing.T) {
	t.Parallel()
	b := newBank(t, 10_000, 0, Config{})
	holds := []*Hold{b.hold(1000), b.hold(2000)}
	outcomes := []Outcome{{}, outcome(Committed, FinishStop, used(t, 50, 20, 1))}
	got := settleBatch(t, b, holds, outcomes)
	if got[0] != 0 {
		t.Errorf("the released reservation charged %d", got[0])
	}
	before := b.count("SELECT count(*) FROM billing.transactions")
	if again := settleBatch(t, b, holds, outcomes); !slices.Equal(got, again) {
		t.Errorf("the retried batch charged %v, first %v", again, got)
	}
	if after := b.count("SELECT count(*) FROM billing.transactions"); after != before {
		t.Errorf("the retried batch wrote %d transactions, want none", after-before)
	}
	b.balanced()
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
