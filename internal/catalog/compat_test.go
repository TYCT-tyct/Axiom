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
