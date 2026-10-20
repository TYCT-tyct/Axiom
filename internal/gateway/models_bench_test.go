package gateway

import (
	"fmt"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/routing"
)

// benchListing is a listing of n models, two names each, and a router whose snapshot has an endpoint for every one.
func benchListing(b *testing.B, n int) (*listing, *routing.Router, []catalog.ModelID) {
	b.Helper()
	router, err := routing.New(routing.Config{})
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
		endpoints = append(endpoints, routing.Endpoint{ID: routing.EndpointID(uuid.NewV7()), Model: id, Context: 1, ServiceTiers: 1 << catalog.ServiceTierStandard})
	}
	snap, err := router.NewSnapshot(time.Now(), endpoints, nil)
	if err != nil {
		b.Fatal(err)
	}
	router.Replace(snap)
	return l, router, ids
}

// BenchmarkModelList is what a request to GET /v1/models costs after its key is admitted: choosing the entries its key
// sees from the encoded list and copying them out, for keys that see every model and keys that see every other one. The
// rule (docs/performance.md「调用路径上的规则」): one allocation, the buffer, whatever the size of the catalog; the
// benchmark fails if it grows, and bench-compare holds the count after that.
func BenchmarkModelList(b *testing.B) {
	for _, n := range []int{50, 300} {
		l, router, ids := benchListing(b, n)
		half := map[catalog.ModelID]struct{}{}
		for i := 0; i < len(ids); i += 2 {
			half[ids[i]] = struct{}{}
		}
		for _, key := range []struct {
			name    string
			allowed map[catalog.ModelID]struct{}
		}{{"all", nil}, {"half", half}} {
			v := visibility{allowed: key.allowed, router: router}
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
