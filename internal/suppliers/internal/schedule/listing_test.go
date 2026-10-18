package schedule

import (
	"slices"
	"testing"
	"time"
)

// TestALimitedListingIsKeptForTheNextJob: a job whose listing the supplier's limits stop ends there, with the page it
// reached in the supplier's state and the task due when they reopen; it does not wait, holding a worker. The job the
// sweep makes then goes on from that page, and the listing ends.
func TestALimitedListingIsKeptForTheNextJob(t *testing.T) {
	t.Parallel()
	s := &system{account: healthy()}
	s.keys.pages, s.keys.stopAt, s.keys.wait = 3, 2, time.Minute
	c := newCluster(t, s)
	n := c.node("a", time.Hour)
	if err := n.s.sync(t.Context(), c.supplier); err != nil {
		t.Fatal(err)
	}
	if s.keysFinished.Load() != 0 || s.opens.Load() != 1 {
		t.Errorf("the listing of keys ended %d times after %d opens, want it stopped by the limits", s.keysFinished.Load(), s.opens.Load())
	}
	var kept bool
	c.row("SELECT listing ? 'keys' FROM suppliers.sync_states WHERE supplier_id = $1", &kept)
	if !kept {
		t.Error("the page the limits stopped the listing at was not kept")
	}
	var due time.Time
	c.row("SELECT next_due FROM suppliers.sync_states WHERE supplier_id = $1", &due)
	if !due.After(time.Now()) || due.Sub(time.Now()) > s.keys.wait+time.Minute {
		t.Errorf("the supplier is due at %v, want the moment the limits reopen", due)
	}

	s.keys.wait = 0 // the limits have reopened
	if err := n.s.sync(t.Context(), c.supplier); err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 2, 3}; !slices.Equal(s.keys.readPages(), want) {
		t.Errorf("the pages read were %v, want %v: the second job goes on where the first stopped", s.keys.readPages(), want)
	}
	if s.keysFinished.Load() != 1 {
		t.Errorf("the listing of keys ended %d times, want once", s.keysFinished.Load())
	}
	c.row("SELECT listing IS NULL FROM suppliers.sync_states WHERE supplier_id = $1", &kept)
	if !kept {
		t.Error("a listing that ended kept its page")
	}
}
