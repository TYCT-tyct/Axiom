package schedule

import (
	"slices"
	"testing"
	"time"
)

// TestALimitedListingIsKeptForTheNextJob: a job whose listing the supplier's limits stop ends there, with the page it
// reached in the supplier's state and the task due when they reopen; it does not wait, holding a worker. The job the
// sweep makes then goes on from that page, and the listing ends with its position gone.
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
	if p := c.position("keys"); p == "" {
		t.Errorf("the page the limits stopped the listing at was not kept: %s", c.listing())
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
	// Page 1 is not read again: the job began at the page the one before was stopped at.
	if want := []int{1, 2, 3}; !slices.Equal(s.keys.readPages(), want) {
		t.Errorf("the pages read were %v, want %v: the second job goes on where the first stopped", s.keys.readPages(), want)
	}
	if s.keysFinished.Load() != 1 {
		t.Errorf("the listing of keys ended %d times, want once", s.keysFinished.Load())
	}
	if l := c.listing(); l != "" {
		t.Errorf("a listing that ended kept its positions: %s", l)
	}
}

// listing is the supplier's column of listing positions, as text, empty when it holds none.
func (c *cluster) listing() string {
	var listing string
	c.row("SELECT coalesce(listing::text, '') FROM suppliers.sync_states WHERE supplier_id = $1", &listing)
	return listing
}

// position is the position of one of the listings as the supplier's state holds it, empty when it holds none.
func (c *cluster) position(member string) string {
	var p string
	c.row("SELECT coalesce(listing ->> '"+member+"', '') FROM suppliers.sync_states WHERE supplier_id = $1", &p)
	return p
}
