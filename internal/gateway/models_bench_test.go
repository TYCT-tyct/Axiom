package gateway

import (
	"fmt"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/routing"
)

// benchListing is a listing of n models, two names each, and the view of a key without a preset of a snapshot that has
// a priced endpoint for every one, under the open zero-ID preset such a key is routed by.
func benchListing(b *testing.B, n int) (*listing, *routing.KeyView, []catalog.ModelID) {
	b.Helper()
	router, err := routing.New(routing.Config{})
	if err != nil {
		b.Fatal(err)
	}
	price, err := catalog.ParseSchedule([]byte(cheap))
	if err != nil {
		b.Fatal(err)
	}
	l := &listing{}
	ids := make([]catalog.ModelID, 0, n)
	endpoints := make([]routing.Endpoint, 0, n)
	for i := range n {
		id := catalog.ModelID(uuid.NewV7())
		m := modelEntries{id: id}
		for _, name := range []string{fmt.Sprintf("vendor%d/model-%d", i%20, i), fmt.Sprintf("model-%d", i)} {
			e, err := encodeEntry(newModelEntry(name, "vendor", name))
			if err != nil {
				b.Fatal(err)
			}
			m.entries = append(m.entries, e)
			m.size += len(e.body) + 1
		}
		l.models = append(l.models, m)
		ids = append(ids, id)
		endpoints = append(endpoints, routing.Endpoint{
			ID: routing.EndpointID(uuid.NewV7()), Model: id, Context: 1, ServiceTiers: 1 << catalog.ServiceTierStandard,
			Class: routing.ClassOfficial, Price: routing.PriceRef{Version: uuid.NewV7(), Schedule: price},
		})
	}
	snap, err := router.NewSnapshot(time.Now(), endpoints, []routing.Preset{{Classes: routing.AllClasses, Policies: routing.AllPolicies}})
	if err != nil {
		b.Fatal(err)
	}
	router.Replace(snap)
	keys, err := router.KeyView(routing.Admission{})
	if err != nil {
		b.Fatal(err)
	}
	return l, &keys, ids
}

// BenchmarkModelList is what a request to GET /v1/models costs after its key is admitted: choosing the entries its key
// sees from the encoded list and copying them out, for keys that see every model and keys that see every other one. The
// rule (docs/performance.md「调用路径上的规则」): one allocation, the buffer, whatever the size of the catalog; the
// benchmark fails if it grows, and bench-compare holds the count after that.
func BenchmarkModelList(b *testing.B) {
	for _, n := range []int{50, 300} {
		l, keys, ids := benchListing(b, n)
		half := map[catalog.ModelID]struct{}{}
		for i := 0; i < len(ids); i += 2 {
			half[ids[i]] = struct{}{}
		}
		for _, key := range []struct {
			name    string
			allowed map[catalog.ModelID]struct{}
		}{{"all", nil}, {"half", half}} {
			v := visibility{allowed: key.allowed, keys: keys}
			b.Run(fmt.Sprintf("models=%d/key=%s", n, key.name), func(b *testing.B) {
				if allocs := testing.AllocsPerRun(10, func() { l.encode(v) }); allocs > 1 {
					b.Fatalf("encode allocates %v times, want the one buffer", allocs)
				}
				b.ReportAllocs()
				for b.Loop() {
					l.encode(v)
				}
			})
		}
	}
}

// orListing is a listing of n models at the rig's cheap price, and the models it serves: the set a route build records.
func orListing(tb testing.TB, n int) *openRouterListing {
	tb.Helper()
	schedule, err := catalog.ParseSchedule([]byte(cheap))
	if err != nil {
		tb.Fatal(err)
	}
	abilities := catalog.AbilitySet(1<<catalog.AbilityTools | 1<<catalog.AbilityReasoning | 1<<catalog.AbilityImages)
	l := &openRouterListing{served: make(map[catalog.ModelID]struct{}, n)}
	for i := range n {
		id := catalog.ModelID(uuid.NewV7())
		author := fmt.Sprintf("vendor%d", i%20)
		slug := fmt.Sprintf("%s/model-%d", author, i)
		e, ok, err := newORModel(slug, catalog.Model{ID: id, Slug: slug, Author: author},
			catalog.Serving{Context: 200000, MaxOutput: 64000, Streams: true, Abilities: abilities}, schedule)
		if err != nil {
			tb.Fatal(err)
		}
		if !ok {
			tb.Fatal("the model has no prompt and completion price")
		}
		l.models = append(l.models, e)
		l.served[id] = struct{}{}
	}
	return l
}

// orIDsOf is the models of a listing in the order it holds them.
func orIDsOf(l *openRouterListing) []catalog.ModelID {
	ids := make([]catalog.ModelID, 0, len(l.models))
	for i := range l.models {
		ids = append(ids, l.models[i].model)
	}
	return ids
}

// BenchmarkOpenRouterList is what a request to GET /api/v1/models costs after its key is admitted and its query is read:
// choosing the entries its key sees and its query keeps from the encoded list and copying them out, for keys that see
// every model and keys that see every other one. The rule (docs/performance.md「调用路径上的规则」): one allocation, the
// buffer, whatever the size of the catalog, and that buffer is the page's (TestOpenRouterPageBuffer); a sorted page
// allocates the matches it sorts as well, which is why the catalog's order is the list's own. The benchmark fails if
// either count grows, and bench-compare holds them after that.
func BenchmarkOpenRouterList(b *testing.B) {
	for _, n := range []int{50, 300} {
		l := orListing(b, n)
		half := map[catalog.ModelID]struct{}{}
		ids := orIDsOf(l)
		for i := 0; i < len(ids); i += 2 {
			half[ids[i]] = struct{}{}
		}
		for _, key := range []struct {
			name    string
			allowed map[catalog.ModelID]struct{}
		}{{"all", nil}, {"half", half}} {
			benchOREncode(b, fmt.Sprintf("models=%d/key=%s", n, key.name), l, visibility{allowed: key.allowed}, orQuery{}, 1)
		}
	}
	l := orListing(b, 300)
	benchOREncode(b, "models=300/sort=pricing-low-to-high", l, visibility{}, orQuery{sort: orCheapestFirst}, 2)
}

// benchOREncode runs the encoding of the list as the key with the query sees it, failing when it allocates more than want
// times: the buffer, and the matches of a sorted page with it.
func benchOREncode(b *testing.B, name string, l *openRouterListing, v visibility, q orQuery, want float64) {
	b.Run(name, func(b *testing.B) {
		if allocs := testing.AllocsPerRun(10, func() { l.encode(v, q) }); allocs > want {
			b.Fatalf("%s allocates %v times, want at most %v", name, allocs, want)
		}
		b.ReportAllocs()
		for b.Loop() {
			l.encode(v, q)
		}
	})
}
