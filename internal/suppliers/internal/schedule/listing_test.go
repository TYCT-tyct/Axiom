package schedule

import (
	"slices"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/persist"
)

// Each run returns with its lease released. A different node resumes the stored position;
// an expired page restarts the listing without retaining items that have disappeared.
func TestALimitedListingIsKeptForTheNextJob(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		shrink bool
	}{{"resume", false}, {"page disappeared", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			limitedListing(t, tc.shrink)
		})
	}
}

func limitedListing(t *testing.T, shrink bool) {
	t.Helper()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	until := now.Add(3 * time.Minute)
	s := &system{account: healthy()}
	s.keys = paging{pages: 3, stopAt: 3, until: until}
	s.offer = paging{pages: 3, stopAt: 2, until: until}
	c := newCluster(t, s)
	c.now = func() time.Time { return now }
	first := c.node("a", time.Hour)
	if err := first.s.sync(t.Context(), c.supplier); err != nil {
		t.Fatal(err)
	}
	if s.keysFinished.Load() != 0 || s.opens.Load() != 1 {
		t.Fatal("the interrupted listing finished or reopened its adapter")
	}
	second := c.node("b", time.Hour)
	checkPausedListing(t, c, second, now)
	now = until
	wantKeys, wantOffer, count := []int{1, 2, 3}, []int{1, 2, 3}, 3
	if shrink {
		s.keys.pages, s.offer.pages = 1, 1
		wantKeys, wantOffer, count = []int{1, 2, 1}, []int{1, 1}, 1
	}
	if err := second.s.sync(t.Context(), c.supplier); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.keys.readPages(), wantKeys) || !slices.Equal(s.offer.readPages(), wantOffer) {
		t.Fatalf("pages: keys %v, offer %v", s.keys.readPages(), s.offer.readPages())
	}
	session, err := first.s.node.Acquire(t.Context(), c.supplier)
	if err != nil {
		t.Fatal(err)
	}
	checkCompleteListing(t, c, session, count)
}

func checkPausedListing(t *testing.T, c *cluster, second *node, now time.Time) {
	t.Helper()
	var due time.Time
	var failures int
	c.row("SELECT next_due, (state->'Tasks'->2->>'Failures')::int FROM suppliers.sync_states WHERE supplier_id = $1", &due, &failures)
	// Balance is due sooner; the interrupted listings still wait until their own deadline.
	if !due.Equal(now.Add(time.Minute)) || failures != 0 {
		t.Fatalf("next due %v, failures %d", due, failures)
	}
	session, err := second.s.node.Acquire(t.Context(), c.supplier)
	if err != nil {
		t.Fatalf("the job kept its lease: %v", err)
	}
	st, err := session.Store().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.KeysAt) == 0 || len(st.OfferAt) == 0 {
		t.Fatal("the interrupted positions were not stored")
	}
	if err := session.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Neither listing is retried while the clock is still before its deadline.
	if err := second.s.sync(t.Context(), c.supplier); err != nil {
		t.Fatal(err)
	}
	if c.system.opens.Load() != 1 {
		t.Fatal("a task ran before it was due")
	}
}

func checkCompleteListing(t *testing.T, c *cluster, session *persist.Session, count int) {
	t.Helper()
	st, err := session.Store().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Keys) != count || len(st.Groups) != count || len(st.KeysAt) != 0 || len(st.OfferAt) != 0 {
		t.Fatalf("completed listing: %d keys, %d groups, positions %q/%q", len(st.Keys), len(st.Groups), st.KeysAt, st.OfferAt)
	}
	var listing bool
	c.row("SELECT listing IS NULL FROM suppliers.sync_states WHERE supplier_id = $1", &listing)
	if !listing {
		t.Fatal("a completed listing kept its positions")
	}
	if err := session.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// paging records the position and items of a listing interrupted between pages.
type paging struct {
	mu            sync.Mutex
	pages, stopAt int
	until         time.Time
	served        []int
}

func (p *paging) read(from adapter.Listing) (items []int, next adapter.Listing, done bool, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pos := struct {
		Page, Total int
		Items       []int
	}{Page: 1, Total: p.pages}
	if len(from) > 0 {
		if err := jsonx.Unmarshal(from, &pos); err != nil {
			panic(err)
		}
	}
	if pos.Total != p.pages {
		pos.Page, pos.Total, pos.Items = 1, p.pages, nil
	}
	for {
		if pos.Page == p.stopAt {
			p.stopAt = 0
			b, err := jsonx.MarshalCanonical(pos)
			if err != nil {
				panic(err)
			}
			return nil, b, false, p.until
		}
		p.served = append(p.served, pos.Page)
		pos.Items = append(pos.Items, pos.Page)
		pos.Page++
		if pos.Page > p.pages {
			return pos.Items, nil, true, time.Time{}
		}
	}
}

func (p *paging) readPages() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.served)
}
