package gateway

import (
	"errors"
	"math/rand/v2"
	"testing"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

func TestEstimatedKeepsPhysicalInputAndImageOutput(t *testing.T) {
	u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemCacheRead: 80, catalog.ItemCacheWrite: 80, catalog.ItemImageOut: 20}, catalog.ServiceTierPriority, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err = u.WithPhysicalInput(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ bytes, want int64 }{{0, 0}, {30, 0}, {90, 10}} {
		original := protocol.Usage{Segments: []protocol.Segment{{Usage: u, Reported: 1<<catalog.ItemInput | 1<<catalog.ItemCacheRead | 1<<catalog.ItemCacheWrite}, {Usage: u}}}
		got := estimated(original, protocol.Progress{ContentBytes: tc.bytes}, billing.Demand{TextBytes: 1000}, billing.Model{ContextLength: 1000}, 100)
		for _, seg := range got {
			if n, ok := seg.Usage.PhysicalInput(); !ok || n != 100 {
				t.Fatalf("physical total lost: %+v", seg)
			}
			if seg.Usage.Count(catalog.ItemImageOut) != 20 || seg.Usage.ServiceTier() != catalog.ServiceTierPriority {
				t.Fatalf("image/tier lost: %+v", seg)
			}
		}
		if n := got[0].Usage.Count(catalog.ItemOutput); n != tc.want {
			t.Errorf("%d bytes: ordinary output %d, want %d", tc.bytes, n, tc.want)
		}
	}
}

// TestAnInvalidUsageIsNeverChargedTwice: a report accepted before a count went down keeps the counts it took, and the
// estimate of the prompt stands in their place rather than on top of them: the input side totals the larger of the two,
// with or without a physical prompt total, whatever the counts are (#339). Stacking them would charge a prompt twice and
// could pass what the reservation covers.
func TestAnInvalidUsageIsNeverChargedTwice(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(3, 5)) //nolint:gosec // G404: test data
	model := billing.Model{ContextLength: 1 << 20}
	for range 1000 {
		counts, kept := map[catalog.Item]int64{}, int64(0)
		for _, it := range []catalog.Item{catalog.ItemInput, catalog.ItemCacheRead, catalog.ItemCacheWrite, catalog.ItemCacheWrite1h, catalog.ItemAudioIn, catalog.ItemImageIn} {
			counts[it] = r.Int64N(2000)
			kept += counts[it]
		}
		u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
		if err != nil {
			t.Fatal(err)
		}
		if r.IntN(2) == 0 { // an OpenAI report carries the prompt total beside the counts
			if u, err = u.WithPhysicalInput(kept); err != nil {
				t.Fatal(err)
			}
		}
		d := billing.Demand{TextBytes: r.Int64N(40000), Images: r.Int64N(3), Documents: r.Int64N(2)}
		estimate := billing.InputEstimate(d, model)
		got := estimated(protocol.Usage{Segments: []protocol.Segment{{Usage: u}}, Invalid: errors.New("a count went down")},
			protocol.Progress{ContentBytes: r.Int64N(1000)}, d, model, 1000)
		total, want := got[0].Usage.InputTotal(), max(kept, estimate)
		if total != want {
			t.Fatalf("counts %v with physical input: the input totals %d, want %d (kept %d, estimate %d)",
				counts, total, want, kept, estimate)
		}
		// The band and the client's echo read the physical total, so it must cover the estimate as well, never less.
		if n, ok := got[0].Usage.PhysicalInput(); ok && n != total {
			t.Fatalf("counts %v: the physical input is %d, want the input total %d (kept %d, estimate %d)",
				counts, n, total, kept, estimate)
		}
	}
}
