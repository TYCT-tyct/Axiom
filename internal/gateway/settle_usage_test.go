package gateway

import (
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
