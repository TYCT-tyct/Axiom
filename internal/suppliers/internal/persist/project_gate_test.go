package persist_test

import (
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// TestDirectFormatsWaitForTheGate: a closed gate writes only the formats a pre-P18 node reads. Once every node can read
// them, opening it on the next sync writes the direct formats.
func TestDirectFormatsWaitForTheGate(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	c.model("direct-model", catalog.StatusListed)
	c.model("mixed-model", catalog.StatusListed)
	groups := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
		{name: "direct-model", formats: []protocol.Format{protocol.Rerank}, plan: &unitPrice},
		{name: "mixed-model", formats: []protocol.Format{protocol.Chat, protocol.Rerank, protocol.Embeddings}, plan: &unitPrice},
	}}}
	closed := c.acquire(c.node("closed"))
	c.commitListing(closed, 1, groups)
	if err := closed.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	same(t, "supplier_models", c.column("supplier_models", "formats::text", "upstream_name"), []string{"{chat}"})
	c.commitListing(c.acquire(c.nodeWith("open", true)), 2, groups)
	same(t, "supplier_models", c.column("supplier_models", "formats::text", "upstream_name"),
		[]string{"{rerank}", "{chat,embeddings,rerank}"})
}
