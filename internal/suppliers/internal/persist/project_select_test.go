package persist_test

import (
	"testing"

	"axiom/internal/protocol"
)

// TestAGroupWithoutASellableEndpointIsNotSelected: a planned model the catalog cannot map makes no endpoint, so it does
// not make the group one the sync puts in use by itself; a person can still select the group and map the name.
func TestAGroupWithoutASellableEndpointIsNotSelected(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	groups := []listedGroup{{ref: "unknown", name: "Unknown", ratio: "1", models: []listedModel{
		{name: "not-in-the-catalog", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
	}}}
	s := c.acquire(c.node("a"))
	c.commitListing(s, 1, groups)
	same(t, "the group waits for a person", c.column("supplier_groups", "upstream_ref || ' ' || selected::text || ' ' || (codename IS NOT NULL)::text", "upstream_ref"),
		[]string{"unknown false false"})
	if got := c.count("endpoints"); got != 0 {
		t.Errorf("the listing made %d endpoints, want none", got)
	}
	same(t, "the name to map", c.pending(), []string{"not-in-the-catalog"})
}
