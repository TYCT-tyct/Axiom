package gateway

import (
	"context"
	"errors"
	"slices"
	"testing"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// TestRunSettlementBatchIsolatesAFailedItem: a BatchWorkError fails only its item and retries the rest, and no report
// runs for the item that failed.
func TestRunSettlementBatchIsolatesAFailedItem(t *testing.T) {
	t.Parallel()
	batch, finals := settlementItems(3)
	reports := make([]int, 3)
	calls := 0
	run := func(_ context.Context, _ pg.Scope, items []billing.Settlement) ([]billing.Final, []func(), error) {
		calls++
		if calls == 1 {
			return nil, nil, &billing.BatchWorkError{Index: 1, Err: errors.New("the second item")}
		}
		if got := seqs(items); !slices.Equal(got, []int{1, 3}) {
			t.Fatalf("retried the items with seqs %v, want [1 3]", got)
		}
		return settledInOrder(items, reports)
	}
	items, _ := settlementBatch(batch)
	if err := runSettlementBatch(context.Background(), batch, items, run); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("run called %d times, want 2", calls)
	}
	if batch[1].err == nil || batch[1].err.Error() != "the second item" || !batch[1].closed {
		t.Errorf("the failed item was not returned with its error: closed=%v err=%v", batch[1].closed, batch[1].err)
	}
	if !slices.Equal(reports, []int{1, 0, 1}) {
		t.Errorf("reports ran %v, want the other two once and the failed item's never", reports)
	}
	charged := []int64{finals[0].Charged.CeilAmount(), finals[1].Charged.CeilAmount(), finals[2].Charged.CeilAmount()}
	if !slices.Equal(charged, []int64{1, 0, 2}) || len(finals[1].Segments) != 0 {
		t.Errorf("finals charged %v, want [1 0 2] and the failed item's unset: %+v", charged, *finals[1])
	}
}

// settledInOrder charges the i-th item i+1 and counts each report under its item's seq.
func settledInOrder(items []billing.Settlement, reports []int) ([]billing.Final, []func(), error) {
	out, after := make([]billing.Final, len(items)), make([]func(), len(items))
	for i := range items {
		out[i] = billing.Final{Charged: platform.AmountExact(int64(i + 1))}
		at := items[i].Outcome.Attempts[0].Seq - 1
		after[i] = func() { reports[at]++ }
	}
	return out, after, nil
}

// TestRunSettlementBatchRetriesEveryItemOnATransactionError: an error with no item (a connection or commit failure)
// reaches every item, and none is answered here. A set-based SQL data error is named by billing first; the real
// 22003 case is TestSettleBatchIsolatesADataError.
func TestRunSettlementBatchRetriesEveryItemOnATransactionError(t *testing.T) {
	t.Parallel()
	batch, _ := settlementItems(2)
	run := func(context.Context, pg.Scope, []billing.Settlement) ([]billing.Final, []func(), error) {
		return nil, []func(){func() { t.Error("a report ran after a failed transaction") }}, errors.New("commit")
	}
	items, _ := settlementBatch(batch)
	if err := runSettlementBatch(context.Background(), batch, items, run); err == nil || err.Error() != "commit" {
		t.Fatalf("error %v, want commit", err)
	}
	for i, g := range batch {
		if g.closed || g.err != nil {
			t.Errorf("item %d was answered: closed=%v err=%v", i, g.closed, g.err)
		}
	}
}

func settlementItems(n int) ([]*gateItem, []*billing.Final) {
	batch, finals := make([]*gateItem, n), make([]*billing.Final, n)
	for i := range batch {
		finals[i] = new(billing.Final)
		item := &completionItem{
			scope:        pg.Scope{},
			final:        finals[i],
			settlement:   billing.Settlement{Hold: &billing.Hold{}, Outcome: billing.Outcome{Attempts: []billing.AttemptResult{{Seq: i + 1}}}},
			isSettlement: true,
		}
		batch[i] = &gateItem{ctx: context.Background(), class: Completion, payload: item, done: make(chan struct{})}
	}
	return batch, finals
}

func seqs(items []billing.Settlement) []int {
	out := make([]int, len(items))
	for i, item := range items {
		out[i] = item.Outcome.Attempts[0].Seq
	}
	return out
}

// SettleBatch is the fake's batch path: each item goes through the fake's SettleIn, which records it, and a failure
// names its item as the real batch's does.
func (l *fakeLedger) SettleBatch(ctx context.Context, _ pg.Scope, items []billing.Settlement) ([]billing.Final, []func(), error) {
	finals, afters := make([]billing.Final, len(items)), make([]func(), len(items))
	for i, item := range items {
		f, after, err := l.SettleIn(ctx, pg.Tx{}, item.Hold, item.Outcome)
		if err != nil {
			return nil, nil, &billing.BatchWorkError{Index: i, Err: err}
		}
		finals[i], afters[i] = f, after
	}
	return finals, afters, nil
}
