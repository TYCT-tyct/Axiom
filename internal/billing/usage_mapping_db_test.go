package billing

import (
	"fmt"
	"sync"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// Concurrent retries must return the same physical usage and charge the overlapped input and image output once.
func TestConcurrentOverlapAndImageSettlements(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{})
	q := metered(t)
	q.Price = mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000000001","cache_read":"0.000000001","cache_write":"0.000000002","output":"0.000000001","image_out":"0.00000001"}}`)
	q.Model.Reports = 1<<catalog.ItemCacheRead | 1<<cacheWrite | 1<<catalog.ItemImageOut
	q.MaxOutput = 10
	seg := overlapSegment(t)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for _, finish := range []FinishReason{FinishStop, FinishError, FinishCancelled} {
		h := b.holdOn(q, 100)
		for range 2 {
			wg.Go(func() {
				f, err := b.billing.Settle(t.Context(), h, outcome(Committed, finish, seg))
				if err != nil {
					errs <- err
					return
				}
				if err := checkOverlapFinal(f); err != nil {
					errs <- err
				}
			})
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := b.count("SELECT balance FROM billing.balances"); got != 10_000-3*340 {
		t.Errorf("balance = %d", got)
	}
	if held := b.count("SELECT reserved FROM billing.balances"); held != 0 {
		t.Errorf("still reserved %d", held)
	}
	b.balanced()
}

// overlapSegment is what protocol makes of 100 input tokens, 80 of them read and 80 written, and 10 output tokens that
// are all images.
func overlapSegment(t *testing.T) Segment {
	t.Helper()
	u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemCacheRead: 80, cacheWrite: 80, catalog.ItemImageOut: 10}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if u, err = u.WithPhysicalInput(100); err != nil {
		t.Fatal(err)
	}
	return Segment{Usage: u, Reported: 1<<in | 1<<catalog.ItemCacheRead | 1<<cacheWrite | 1<<catalog.ItemOutput | 1<<catalog.ItemReasoning | 1<<catalog.ItemImageOut}
}

func checkOverlapFinal(f Final) error {
	if f.Charged != platform.AmountExact(340) || f.Waived || f.Estimated || len(f.Segments) != 1 {
		return fmt.Errorf("settled %+v, want 340 charged once without waiver or estimate", f)
	}
	if n, ok := f.Segments[0].Usage.PhysicalInput(); !ok || n != 100 {
		return fmt.Errorf("physical input = %d/%t", n, ok)
	}
	return nil
}
