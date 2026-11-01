package catalog

import (
	"errors"
	"testing"
)

// TestAllowCounted: a price for an item counted per piece, second or character is refused while this release may not
// publish it, so no listed model's schedule can carry a name an older release cannot parse; a price without one, and
// the same price once CountedItems is on, pass.
func TestAllowCounted(t *testing.T) {
	t.Parallel()
	counted, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","character_in":"0.000015"}}`))
	if err != nil {
		t.Fatal(err)
	}
	token, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","output":"2"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Catalog{}).allowCounted(counted); !errors.Is(err, ErrCountedItems) {
		t.Errorf("a counted price while off: %v, want ErrCountedItems", err)
	}
	if err := (&Catalog{cfg: Config{CountedItems: true}}).allowCounted(counted); err != nil {
		t.Errorf("a counted price while on: %v", err)
	}
	if err := (&Catalog{}).allowCounted(token); err != nil {
		t.Errorf("a token price while off: %v", err)
	}
}

// TestAllowRequest: a per-request price has its own rolling-upgrade switch, separate from counted items. The release
// that reads request prices is not the release that opens counted items, so the two gates cannot share a flag.
func TestAllowRequest(t *testing.T) {
	t.Parallel()
	request, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","request":"2"}}`))
	if err != nil {
		t.Fatal(err)
	}
	token, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","output":"2"}}`))
	if err != nil {
		t.Fatal(err)
	}
	counted, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","character_in":"0.000015"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Catalog{}).allowRequest(request); !errors.Is(err, ErrRequestPrices) {
		t.Errorf("a request price while off: %v, want ErrRequestPrices", err)
	}
	if err := (&Catalog{cfg: Config{RequestPrices: true}}).allowRequest(request); err != nil {
		t.Errorf("a request price while on: %v", err)
	}
	if err := (&Catalog{}).allowRequest(token); err != nil {
		t.Errorf("a token price while off: %v", err)
	}
	// The two gates are independent: opening request prices does not open counted items, and the reverse is true too.
	if err := (&Catalog{cfg: Config{RequestPrices: true}}).allowPrices(counted); !errors.Is(err, ErrCountedItems) {
		t.Errorf("counted price with only request prices open: %v, want ErrCountedItems", err)
	}
	if err := (&Catalog{cfg: Config{CountedItems: true}}).allowPrices(request); !errors.Is(err, ErrRequestPrices) {
		t.Errorf("request price with only counted items open: %v, want ErrRequestPrices", err)
	}
}
