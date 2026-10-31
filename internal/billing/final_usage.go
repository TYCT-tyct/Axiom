package billing

import (
	"fmt"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

func usages(segments []Segment) []catalog.Usage {
	u := make([]catalog.Usage, len(segments))
	for i, s := range segments {
		u[i] = s.Usage
	}
	return u
}

// storedSegment is a segment as final_usage stores it: each item's count in item order, the service tier, the
// reported items and the usage protocol could not map.
type storedSegment struct {
	Counts        []int64                `json:"counts"`
	Tier          catalog.ServiceTier    `json:"tier"`
	Reported      catalog.ItemSet        `json:"reported"`
	Unknown       []catalog.UnknownUsage `json:"unknown,omitzero"`
	PhysicalInput *int64                 `json:"physical_input,omitzero"`
	Native        *catalog.NativeTokens  `json:"native,omitzero"`
}

// encodeUsage is the segments as final_usage stores them, so a retried Settle returns the Final the first one gave.
func encodeUsage(segments []Segment) ([]byte, error) {
	stored := make([]storedSegment, len(segments))
	for i, s := range segments {
		counts := make([]int64, catalog.NumItems)
		for it := range catalog.NumItems {
			counts[it] = s.Usage.Count(it)
		}
		// A trailing zero count is left out, so a node built before an item was added can still read a charge that uses
		// none of the newer items; a count the list leaves out is zero.
		for len(counts) > 0 && counts[len(counts)-1] == 0 {
			counts = counts[:len(counts)-1]
		}
		stored[i] = storedSegment{Counts: counts, Tier: s.Usage.ServiceTier(), Reported: s.Reported, Unknown: s.Usage.Unknown()}
		if n, ok := s.Usage.PhysicalInput(); ok {
			stored[i].PhysicalInput = &n
		}
		if s.Usage.HasNativeTokens() {
			n := s.Usage.NativeTokens()
			stored[i].Native = &n
		}
	}
	b, err := jsonx.MarshalCanonical(stored)
	if err != nil {
		return nil, fmt.Errorf("billing: write the usage: %w", err)
	}
	return b, nil
}

func decodeUsage(b []byte) ([]Segment, error) {
	var stored []storedSegment
	if err := jsonx.UnmarshalKnown(b, &stored); err != nil {
		return nil, fmt.Errorf("billing: read the stored usage: %w", err)
	}
	segments := make([]Segment, len(stored))
	for i, s := range stored {
		counts := map[catalog.Item]int64{}
		for it, n := range s.Counts {
			counts[catalog.Item(it)] = n
		}
		u, err := catalog.NewUsage(counts, s.Tier, s.Unknown)
		if err == nil && s.PhysicalInput != nil {
			u, err = u.WithPhysicalInput(*s.PhysicalInput)
		}
		if err == nil && s.Native != nil {
			u, err = u.WithNativeTokens(*s.Native)
		}
		if err != nil {
			return nil, fmt.Errorf("billing: read the stored usage: %w", err)
		}
		segments[i] = Segment{Usage: u, Reported: s.Reported}
	}
	return segments, nil
}

// serverToolPtr is the per-use sale price when a per-use item ran, nil otherwise: zero is a real price. It returns a
// copy, so a caller that keeps the price does not move the pricing itself to the heap for every settlement.
func (p *priced) serverToolPtr() *platform.Exact {
	if !p.hasServerTool {
		return nil
	}
	price := p.serverTool
	return &price
}
