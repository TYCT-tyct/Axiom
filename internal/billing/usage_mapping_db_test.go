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
	seg := acceptedOpenAI(t, `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":80},"output_tokens":10,"output_tokens_details":{"image_tokens":10}}`, catalog.ServiceTierStandard, true)
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
				if f.Charged != platform.AmountExact(340) || f.Waived || f.Estimated || len(f.Segments) != 1 {
					errs <- fmt.Errorf("settled %+v, want 340 charged once without waiver or estimate", f)
					return
				}
				if n, ok := f.Segments[0].Usage.PhysicalInput(); !ok || n != 100 {
					errs <- fmt.Errorf("physical input = %d/%t", n, ok)
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
