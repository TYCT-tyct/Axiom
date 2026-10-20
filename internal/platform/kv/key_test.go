package kv

import (
	"testing"
	"uuid"
)

// TestKey: the layout README「键」 fixes, an ID in 32 lowercase hex digits, and one allocation, because the call path
// builds two keys for every precheck.
func TestKey(t *testing.T) {
	client, err := Open(t.Context(), Config{Nodes: 1, Namespace: "v9"}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	site := uuid.MustParse("6F1B2F14-3A4C-4BD0-9D1E-2F5A7C8B9D10")
	const want = "ax:v9:rate:6f1b2f143a4c4bd09d1e2f5a7c8b9d10:key:abc:5"
	if got := client.Key(RateSpace, site, "key", "abc", "5"); got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
	if got := Hex(site); got != "6f1b2f143a4c4bd09d1e2f5a7c8b9d10" {
		t.Fatalf("Hex = %q", got)
	}
	if allocs := testing.AllocsPerRun(100, func() { client.Key(RateSpace, site, "key", "abc", "5") }); allocs > 1 {
		t.Fatalf("a key took %v allocations, want 1", allocs)
	}
}
