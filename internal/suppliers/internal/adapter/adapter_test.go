package adapter_test

import (
	"context"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/suppliers/internal/adapter"
)

// charges is a Manager whose charge list holds one record; the other methods are not used.
type charges struct {
	adapter.Manager
	page adapter.ChargePage
}

func (c charges) Charges(context.Context, adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	return c.page, nil, nil
}

// TestAChargeKeepsItsUsageThroughTheContract: what a record says was used, and which items it states, reach the reader
// of Manager.Charges, so that an estimate can be corrected item by item. An item the record does not state is not the
// same as one it states as zero.
func TestAChargeKeepsItsUsageThroughTheContract(t *testing.T) {
	t.Parallel()
	usage, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 1200, catalog.ItemOutput: 300, catalog.ItemReasoning: 0}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	var reported catalog.ItemSet
	reported.Add(catalog.ItemInput)
	reported.Add(catalog.ItemOutput)
	reported.Add(catalog.ItemReasoning) // stated as zero
	want := adapter.Charge{Ref: "r-1", Key: "k-1", OccurredAt: time.Unix(1, 0), Amount: "0.0000123456", Usage: usage, Reported: reported}
	var m adapter.Manager = charges{page: adapter.ChargePage{Charges: []adapter.Charge{want}, Done: true}}
	page, _, err := m.Charges(t.Context(), nil)
	if err != nil || len(page.Charges) != 1 {
		t.Fatalf("Charges: %v, %d records", err, len(page.Charges))
	}
	got := page.Charges[0]
	if got.Usage.OutputTotal() != 300 {
		t.Errorf("the record's output usage is %d, want 300", got.Usage.OutputTotal())
	}
	for _, c := range []struct {
		item     catalog.Item
		reported bool
	}{{catalog.ItemInput, true}, {catalog.ItemOutput, true}, {catalog.ItemReasoning, true}, {catalog.ItemCacheRead, false}, {catalog.ItemWebSearch, false}} {
		if got.Reported.Has(c.item) != c.reported {
			t.Errorf("%s reported = %v, want %v", c.item, got.Reported.Has(c.item), c.reported)
		}
	}
}
