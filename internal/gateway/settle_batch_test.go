package gateway

import (
	"context"
	"errors"
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
		if len(items) != 2 || items[0].Outcome.Attempts[0].Seq != 1 || items[1].Outcome.Attempts[0].Seq != 3 {
			t.Fatalf("retried %d items with seqs %v", len(items), seqs(items))
		}
		out := make([]billing.Final, len(items))
		after := make([]func(), len(items))
		for i := range items {
			out[i] = billing.Final{Charged: platform.AmountExact(int64(i + 1))}
			at := itemSeq(items, i) - 1
			after[i] = func() { reports[at]++ }
		}
		return out, after, nil
	}
	items, _ := settlementBatch(batch)
	if err := runSettlementBatch(context.Background(), batch, items, run); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("run called %d times, want 2", calls)
	}
	if batch[1].err == nil || batch[1].err.Error() != "the second item" {
		t.Errorf("the failed item's error is %v", batch[1].err)
	}
	if !batch[1].closed {
		t.Error("the failed item was not returned")
	}
	if reports[1] != 0 {
		t.Error("the failed item's report ran")
	}
	if reports[0] != 1 || reports[2] != 1 {
		t.Errorf("reports ran %v, want the other two once", reports)
	}
	if finals[1].Charged.CeilAmount() != 0 || len(finals[1].Segments) != 0 {
		t.Errorf("the failed item's final was set: %+v", *finals[1])
	}
	if finals[0].Charged != platform.AmountExact(1) || finals[2].Charged != platform.AmountExact(2) {
		t.Errorf("finals %v, %v", finals[0].Charged, finals[2].Charged)
	}
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

func itemSeq(items []billing.Settlement, i int) int { return items[i].Outcome.Attempts[0].Seq }

func seqs(items []billing.Settlement) []int {
	out := make([]int, len(items))
	for i, item := range items {
		out[i] = item.Outcome.Attempts[0].Seq
	}
	return out
}
