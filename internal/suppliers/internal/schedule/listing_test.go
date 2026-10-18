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
	s.keys.pages, s.keys.stopAt, s.keys.wait = 3, 2, 200*time.Millisecond
	c := newCluster(t, s)
	n := c.node("a", time.Hour)
	start := time.Now()
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
	// Due when the limits reopen, not at one of the intervals of minutes: a supplier whose limits stopped it is due again
	// when they do, so the sweep brings a job for it then, without the wait counting as a failure or holding a worker.
	if wait := due.Sub(start); wait <= 0 || wait > 10*time.Second {
		t.Errorf("the supplier is due %v after the job began, want it due when the limits reopen, in %v", wait, s.keys.wait)
	}

	s.keys.wait = 0 // the limits have reopened
	time.Sleep(300 * time.Millisecond)
	if err := n.s.sync(t.Context(), c.supplier); err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 2, 3}; !slices.Equal(s.keys.readPages(), want) {
		t.Errorf("the pages read were %v, want %v: the second job goes on where the first stopped", s.keys.readPages(), want)
	}
	if s.keysFinished.Load() != 1 {
		t.Errorf("the listing of keys ended %d times, want once", s.keysFinished.Load())
	}
	var ended bool
	c.row("SELECT coalesce(listing -> 'keys', 'null'::jsonb) = 'null'::jsonb FROM suppliers.sync_states WHERE supplier_id = $1", &ended)
	if !ended {
		t.Error("a listing that ended kept its page")
	}
}
