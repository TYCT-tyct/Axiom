package syncer

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// paged is the position of a fake listing: the page to read next, the total the supplier states (a listing that changed
// under the position begins again) and what the pass has read. An adapter's position carries what it read too, so a
// listing that skipped a page ends without that page's items.
type paged[T any] struct {
	V     int `json:"v"`
	Page  int `json:"page"`
	Total int `json:"total"`
	Items []T `json:"items"`
}

// pager serves one fake listing: its pages, the page whose read the supplier's limits refuse once, and the pages it
// served, in order.
type pager[T any] struct {
	t       *testing.T
	pages   [][]T
	blockAt int
	until   time.Time
	served  []int
}

func newPager[T any](t *testing.T, pages [][]T) *pager[T] { return &pager[T]{t: t, pages: pages} }

// read reads the listing from from as far as it goes, like an adapter does: page after page until it ends, or until the
// limits refuse one, which keeps the page it reached and holds no item.
func (p *pager[T]) read(from adapter.Listing) adapter.Page[T] {
	pos := paged[T]{V: 1, Page: 1, Total: len(p.pages)}
	if len(from) > 0 {
		if err := jsonx.UnmarshalKnown(from, &pos); err != nil {
			p.t.Fatalf("the fake's own position %q: %v", from, err)
		}
	}
	if pos.Total != len(p.pages) { // the supplier's listing changed under the position: it begins again
		pos = paged[T]{V: 1, Page: 1, Total: len(p.pages)}
	}
	for {
		if pos.Page == p.blockAt {
			p.blockAt = 0
			return adapter.Page[T]{Next: p.encode(pos), Until: p.until}
		}
		p.served = append(p.served, pos.Page)
		pos.Items = append(pos.Items, p.pages[pos.Page-1]...)
		pos.Page++
		if pos.Page > len(p.pages) {
			return adapter.Page[T]{Items: pos.Items, Done: true}
		}
	}
}

func (p *pager[T]) encode(pos paged[T]) adapter.Listing {
	b, err := jsonx.MarshalCanonical(pos)
	if err != nil {
		p.t.Fatal(err)
	}
	return b
}

// readPage is a fake listing as the fake manager's field takes it.
func readPage[T any](p *pager[T]) func(adapter.Listing) (adapter.Page[T], error) {
	return func(from adapter.Listing) (adapter.Page[T], error) { return p.read(from), nil }
}

// keyPages makes n pages of a keys listing, two keys each.
func keyPages(n int) (pages [][]adapter.KeyState) {
	for i := range n {
		pages = append(pages, []adapter.KeyState{
			{Ref: adapter.KeyRef(fmt.Sprintf("k%d-a", i)), Status: adapter.KeyActive},
			{Ref: adapter.KeyRef(fmt.Sprintf("k%d-b", i)), Status: adapter.KeyActive},
		})
	}
	return pages
}

// seq is the numbers from a to b, both included, and nothing when a is past b.
func seq(a, b int) (out []int) {
	for i := a; i <= b; i++ {
		out = append(out, i)
	}
	return out
}

// TestALimitedListingGoesOnFromThePageItReached: a read the supplier's limits stop keeps the page it reached, counts no
// failure and ends the round; the next round goes on from that page, over the same engine or — as a restarted node has —
// a new one and a new adapter over the stored state. Every page of the listing reaches the state, and none twice.
func TestALimitedListingGoesOnFromThePageItReached(t *testing.T) {
	t.Parallel()
	for _, block := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprintf("the limits stop page %d", block), func(t *testing.T) {
			t.Parallel()
			limitedListing(t, block)
		})
	}
}

// limitedListing runs a listing of four pages whose read of page block the limits stop once, and a restart in between.
func limitedListing(t *testing.T, block int) {
	t.Helper()
	pages := keyPages(4)
	first := newPager(t, pages)
	first.blockAt, first.until = block, t0.Add(time.Minute)
	m := healthy()
	m.keys = readPage(first)
	e, st, clk := engine(m)
	round(t, e)
	if len(st.state.KeysAt) == 0 || st.state.Tasks[TaskKeys].Failures != 0 || !st.state.Tasks[TaskKeys].Due.Equal(first.until) {
		t.Fatalf("after the limits: position %q, task %+v; want it kept and due at %v", st.state.KeysAt, st.state.Tasks[TaskKeys], first.until)
	}
	if len(st.state.Keys) != 0 || len(st.events) != 0 {
		t.Errorf("a listing that did not end stored %d keys and %v", len(st.state.Keys), st.kinds())
	}
	if want := seq(1, block-1); !slices.Equal(first.served, want) {
		t.Fatalf("the first job read pages %v, want %v", first.served, want)
	}

	// A node restarted between the two: another engine, another adapter, the stored state.
	second := newPager(t, pages)
	restarted := healthy()
	restarted.keys = readPage(second)
	clk.advance(time.Minute)
	e = New(st, restarted, Defaults(), clk.now)
	round(t, e)

	if want := seq(block, 4); !slices.Equal(second.served, want) {
		t.Errorf("after the restart the listing read pages %v, want %v: it goes on where it stopped", second.served, want)
	}
	if len(st.state.KeysAt) != 0 || len(st.state.Keys) != 8 {
		t.Errorf("after the whole listing: position %q, %d keys; want no position and 8 keys", st.state.KeysAt, len(st.state.Keys))
	}
	allKeys(t, st.state.Keys, pages)
}

// allKeys checks that every key of the pages is in the state.
func allKeys(t *testing.T, got []adapter.KeyState, pages [][]adapter.KeyState) {
	t.Helper()
	for _, page := range pages {
		for _, k := range page {
			if !slices.ContainsFunc(got, func(g adapter.KeyState) bool { return g.Ref == k.Ref }) {
				t.Errorf("key %s of the listing is missing from the state", k.Ref)
			}
		}
	}
}

// TestAStaleListingPositionStartsTheListingAgain: the supplier's keys changed under a stored position (fewer of them: the
// pages moved forward, so going on would skip a key). The listing begins again, is read whole, and the keys that are gone
// do not stay in the state.
func TestAStaleListingPositionStartsTheListingAgain(t *testing.T) {
	t.Parallel()
	first := newPager(t, keyPages(4))
	first.blockAt, first.until = 3, t0.Add(time.Minute)
	m := healthy()
	m.keys = readPage(first)
	e, st, clk := engine(m)
	round(t, e)
	if len(st.state.KeysAt) == 0 {
		t.Fatal("the position was not kept")
	}

	second := newPager(t, keyPages(2))
	m.keys = readPage(second)
	clk.advance(time.Minute)
	round(t, e)
	if want := []int{1, 2}; !slices.Equal(second.served, want) {
		t.Errorf("the listing read pages %v, want %v: one that changed under its position begins again", second.served, want)
	}
	if len(st.state.Keys) != 4 || len(st.state.KeysAt) != 0 {
		t.Errorf("after the changed listing: %d keys, position %q; want the 4 keys it holds now", len(st.state.Keys), st.state.KeysAt)
	}
}

// TestALimitedOfferGoesOnAndEndsCleared: the offer keeps its position the same way, and the read that ends it clears the
// position with the groups it read.
func TestALimitedOfferGoesOnAndEndsCleared(t *testing.T) {
	t.Parallel()
	page := func(ref, ratio string) []adapter.Group {
		return []adapter.Group{{Ref: ref, Name: ref, Ratio: adapter.Amount(ratio), Facts: adapter.GroupFacts(`{}`), Models: []adapter.Model{{UpstreamName: "m"}}}}
	}
	offer := newPager(t, [][]adapter.Group{page("1", "1"), page("2", "2"), page("3", "3")})
	offer.blockAt, offer.until = 2, t0.Add(time.Minute)
	m := healthy()
	m.offer = readPage(offer)
	e, st, clk := engine(m)
	round(t, e)
	if len(st.state.OfferAt) == 0 || st.state.Tasks[TaskCatalog].Failures != 0 || !st.state.Tasks[TaskCatalog].Due.Equal(offer.until) {
		t.Fatalf("after the limits: position %q, task %+v", st.state.OfferAt, st.state.Tasks[TaskCatalog])
	}
	clk.advance(time.Minute)
	round(t, e)
	if want := []int{1, 2, 3}; !slices.Equal(offer.served, want) {
		t.Errorf("the offer read pages %v, want %v: it goes on where it stopped", offer.served, want)
	}
	if len(st.state.OfferAt) != 0 || len(st.state.Groups) != 3 {
		t.Errorf("after the offer ended: position %q, %d groups; want no position and 3 groups", st.state.OfferAt, len(st.state.Groups))
	}
}
