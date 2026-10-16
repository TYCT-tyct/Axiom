package syncer

import (
	"context"

	"axiom/internal/suppliers/internal/adapter"
)

// scan reads the charge records. A pass covers a window, from the watermark less an overlap to a moment before now.
// Each page is committed with the position after it, so a pass interrupted by a limit, an error or a restart goes on
// where it was; when the last page is in, the watermark moves to the window's end in the same commit and never back.
// A record is kept by its Ref, so the overlap and the repeats a growing list causes cost nothing.
func (e *Engine) scan(ctx context.Context, st *State, c *Commit) error {
	if c.State.Scan == nil && !e.beginPass(c) {
		return nil
	}
	for range e.set.MaxPages {
		done, err := e.page(ctx, st, c)
		if err != nil || done || st.Status == NeedsAttention {
			return err
		}
	}
	return nil
}

// beginPass opens the next window; false when there is nothing to read yet.
func (e *Engine) beginPass(c *Commit) bool {
	now := e.now()
	start, end := c.State.Watermark.Add(-e.set.Overlap), now.Add(-e.set.Safety)
	if c.State.Watermark.IsZero() {
		start = now.Add(-e.set.Backfill)
	}
	if !end.After(start) {
		return false
	}
	c.State.Scan = adapter.BeginScan(start, end)
	return true
}

// page reads one page and commits it with the position after it; done is true once the pass is complete.
func (e *Engine) page(ctx context.Context, st *State, c *Commit) (done bool, err error) {
	page, next, err := e.mgr.Charges(ctx, c.State.Scan)
	if err != nil {
		return false, err // the pages before it were committed one by one: the pass goes on from there
	}
	c.Charges, c.State.Scan = page.Charges, next
	for _, ch := range page.Charges {
		if ch.Funding != adapter.FundingWallet && c.State.Status == Normal {
			// A record paid from something other than the wallet is a cost this module does not model.
			c.State.Status, c.State.Reason = NeedsAttention, "a charge was paid from something other than the wallet"
			c.Events = append(c.Events, Event{Kind: NeedsAttentionEvent, Why: c.State.Reason})
		}
	}
	if page.Done {
		w, err := next.Window()
		if err != nil {
			return false, err
		}
		if w.End.After(c.State.Watermark) {
			c.State.Watermark = w.End
		}
		c.State.Scan = nil
	}
	return page.Done, e.commit(ctx, st, c)
}
