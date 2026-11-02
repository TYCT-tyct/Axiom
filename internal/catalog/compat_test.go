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

// TestAllowFileSearch: the file_search item and ability are new names in two closed enumerations, so they pass the same
// rolling-upgrade gate: a price, a spec or a max_uses entry that names them is refused until every node reads them.
func TestAllowFileSearch(t *testing.T) {
	t.Parallel()
	withItem, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","file_search":"0.0025"}}`))
	if err != nil {
		t.Fatal(err)
	}
	token, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","output":"2"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Catalog{}).allowPrices(withItem); !errors.Is(err, ErrFileSearch) {
		t.Errorf("a file_search price while off: %v, want ErrFileSearch", err)
	}
	if err := (&Catalog{cfg: Config{FileSearch: true}}).allowPrices(withItem); err != nil {
		t.Errorf("a file_search price while on: %v", err)
	}
	if err := (&Catalog{}).allowPrices(token); err != nil {
		t.Errorf("a token price while off: %v", err)
	}
	// It has a switch of its own: the other two do not open it, and it opens neither of them.
	request, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","request":"2"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Catalog{cfg: Config{FileSearch: true}}).allowPrices(request); !errors.Is(err, ErrRequestPrices) {
		t.Errorf("a request price with only file search open: %v, want ErrRequestPrices", err)
	}
	if err := (&Catalog{cfg: Config{RequestPrices: true, CountedItems: true}}).allowPrices(withItem); !errors.Is(err, ErrFileSearch) {
		t.Errorf("a file_search price with the other gates open: %v, want ErrFileSearch", err)
	}
}

// TestAllowFileSearchSpec: the spec's new names — the item, the ability and the max_uses key — pass the same gate, and
// an ability without the item it bills is refused whatever the gate says.
func TestAllowFileSearchSpec(t *testing.T) {
	t.Parallel()
	spec, err := parseSavedSpec([]byte(`{"items":["input","file_search"],"tiers":["standard"],
		"abilities":["file_search"],"max_uses":{"file_search":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Catalog{}).allowSpec(spec); !errors.Is(err, ErrFileSearch) {
		t.Errorf("a file_search spec while off: %v, want ErrFileSearch", err)
	}
	if err := (&Catalog{cfg: Config{FileSearch: true}}).allowSpec(spec); err != nil {
		t.Errorf("a file_search spec while on: %v", err)
	}
	uses, err := parseSavedSpec([]byte(`{"items":["input"],"tiers":["standard"],"max_uses":{"file_search":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Catalog{}).allowSpec(uses); !errors.Is(err, ErrFileSearch) {
		t.Errorf("a file_search max_uses while off: %v, want ErrFileSearch", err)
	}
	// The ability runs calls billed as the item, so a spec that lists it without the item is refused whatever the gate.
	if _, err := parseSavedSpec([]byte(`{"items":["input"],"tiers":["standard"],"abilities":["file_search"]}`)); err == nil {
		t.Error("an ability without its item was accepted")
	}
}

// TestAllowFileParse: the file_parse item is a new name in the price schedule's closed enumeration, so it has the same
// rolling-upgrade gate, separate from every other new item. It adds no spec item or ability: only a price waits.
func TestAllowFileParse(t *testing.T) {
	t.Parallel()
	withItem, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","file_parse":"0.002"}}`))
	if err != nil {
		t.Fatal(err)
	}
	token, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","output":"2"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Catalog{}).allowPrices(withItem); !errors.Is(err, ErrFileParse) {
		t.Errorf("a file_parse price while off: %v, want ErrFileParse", err)
	}
	if err := (&Catalog{cfg: Config{FileParse: true}}).allowPrices(withItem); err != nil {
		t.Errorf("a file_parse price while on: %v", err)
	}
	if err := (&Catalog{}).allowPrices(token); err != nil {
		t.Errorf("a token price while off: %v", err)
	}
	// Its switch is its own: the other new-item gates do not open it, and it opens none of them.
	search, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","file_search":"0.0025"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Catalog{cfg: Config{FileParse: true}}).allowPrices(search); !errors.Is(err, ErrFileSearch) {
		t.Errorf("a file_search price with only file parse open: %v, want ErrFileSearch", err)
	}
	if err := (&Catalog{cfg: Config{CountedItems: true, RequestPrices: true, FileSearch: true}}).allowPrices(withItem); !errors.Is(err, ErrFileParse) {
		t.Errorf("a file_parse price with the other gates open: %v, want ErrFileParse", err)
	}
}
