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
		got := estimated(original, protocol.Progress{ContentBytes: tc.bytes}, billing.Demand{TextBytes: 1000}, billing.Model{ContextLength: 1000}, 100, billing.TokenizerNone)
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
// estimate of the prompt stands in their place rather than on top of them: the input side's items total the larger of
// the two, with or without a physical prompt total, whatever the counts are (#339). The physical total is the prompt's
// own length, which cache items may overlap and exceed together, so it is checked apart from the items: raised to the
// estimate, never below what the report said. Stacking the estimate on the kept counts would charge a prompt twice and
// could pass what the reservation covers.
func TestAnInvalidUsageIsNeverChargedTwice(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(3, 5)) //nolint:gosec // G404: test data
	model := billing.Model{ContextLength: 1 << 20}
	items := []catalog.Item{catalog.ItemInput, catalog.ItemCacheRead, catalog.ItemCacheWrite, catalog.ItemCacheWrite1h, catalog.ItemAudioIn, catalog.ItemImageIn}
	for range 1000 {
		checkInvalidUsageChargedOnce(t, r, model, items)
	}
}

// checkInvalidUsageChargedOnce builds one report of random counts and an optional physical total, and checks that the
// estimate stands in place of what the input side kept rather than on top of it, and that the physical total covers the
// estimate.
func checkInvalidUsageChargedOnce(t *testing.T, r *rand.Rand, model billing.Model, items []catalog.Item) {
	t.Helper()
	counts, kept, most := map[catalog.Item]int64{}, int64(0), int64(0)
	for _, it := range items {
		counts[it] = r.Int64N(2000)
		kept, most = kept+counts[it], max(most, counts[it])
	}
	u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	physical := int64(0)
	if r.IntN(2) == 0 { // an OpenAI report carries the prompt total beside the counts, which may add up past it
		physical = most + r.Int64N(kept-most+1)
		if u, err = u.WithPhysicalInput(physical); err != nil {
			t.Fatal(err)
		}
	}
	d := billing.Demand{TextBytes: r.Int64N(40000), Images: r.Int64N(3), Documents: r.Int64N(2)}
	estimate := billing.InputEstimate(d, model)
	got := estimated(protocol.Usage{Segments: []protocol.Segment{{Usage: u}}, Invalid: errors.New("a count went down")},
		protocol.Progress{ContentBytes: r.Int64N(1000)}, d, model, 1000, billing.TokenizerNone)
	want := max(kept, estimate)
	if total := inputItems(got[0].Usage); total != want {
		t.Fatalf("counts %v with physical input %d: the input side totals %d, want %d (kept %d, estimate %d)",
			counts, physical, total, want, kept, estimate)
	}
	// The band and the client's echo read the physical total, so it must cover the estimate, and never shrink below
	// what the report carried.
	if physical > 0 {
		if n, ok := got[0].Usage.PhysicalInput(); !ok || n != max(physical, estimate) {
			t.Fatalf("counts %v: the physical input is %d, want %d (was %d, estimate %d)",
				counts, n, max(physical, estimate), physical, estimate)
		}
	}
}

// inputItems is what a usage's prompt items add up to, the sum billing prices the input side by.
func inputItems(u catalog.Usage) int64 {
	var n int64
	for it := range catalog.NumItems {
		if catalog.InputSide.Has(it) {
			n += u.Count(it)
		}
	}
	return n
}

// TestEstimatedLeavesOutEcho: echo repeats the prompt at the start of every choice; the output estimate subtracts the
// echoed bytes the demand carries, so an upstream without usage is not charged for text the model did not generate.
func TestEstimatedLeavesOutEcho(t *testing.T) {
	t.Parallel()
	plain := estimated(protocol.Usage{}, protocol.Progress{ContentBytes: 12}, billing.Demand{}, billing.Model{}, 100, billing.TokenizerNone)
	if n := plain[0].Usage.Count(catalog.ItemOutput); n != 4 {
		t.Errorf("12 generated bytes: output %d, want 4", n)
	}
	echoed := estimated(protocol.Usage{}, protocol.Progress{ContentBytes: 12}, billing.Demand{EchoBytes: 7}, billing.Model{}, 100, billing.TokenizerNone)
	if n := echoed[0].Usage.Count(catalog.ItemOutput); n != 2 {
		t.Errorf("12 bytes with 7 echoed: output %d, want the 5 generated at 3 bytes a token", n)
	}
}
