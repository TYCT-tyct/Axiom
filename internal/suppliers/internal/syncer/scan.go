package syncer

import (
	"context"

	"axiom/internal/suppliers/internal/adapter"
)

// scan reads the charge records. The position is the adapter's own, opaque to the engine: the first call is made with
// none, and each page is committed with the position that comes back, so a pass interrupted by a limit, an error or a
// restart goes on where it was. The adapter says when a pass has ended (Done), also when there was nothing yet to read.
// A record is kept by its Ref, so the repeats of an overlap or a growing list cost nothing.
func (e *Engine) scan(ctx context.Context, st *State, c *Commit) error {
	for range e.set.MaxPages {
		done, err := e.page(ctx, st, c)
		if err != nil || done || st.Status == NeedsAttention {
			return err
		}
	}
	return nil
}

// page reads one page and commits it with the position after it; done is true once the pass is complete.
func (e *Engine) page(ctx context.Context, st *State, c *Commit) (done bool, err error) {
	page, next, err := e.mgr.Charges(ctx, c.State.Scan)
	if err != nil {
		return false, err // the pages before it were committed one by one: the pass goes on from there
	}
	c.Charges, c.State.Scan, c.State.ScanOpen = page.Charges, next, !page.Done
	for _, ch := range page.Charges {
		if ch.Funding != adapter.FundingWallet && c.State.Status == Normal {
			// A record paid from something other than the wallet is a cost this module does not model.
			c.State.Status, c.State.Reason = NeedsAttention, "a charge was paid from something other than the wallet"
			c.Events = append(c.Events, Event{Kind: NeedsAttentionEvent, Why: c.State.Reason})
		}
	}
	return page.Done, e.commit(ctx, st, c)
}
