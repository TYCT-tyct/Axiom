package main

import (
	"slices"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
)

// TestClaimedByCatalog: the verification adapter answers the model author's display name and every other author's, and
// nothing for a model the catalog does not have, so a check never compares an answer with an empty claim.
func TestClaimedByCatalog(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	w.seedFailover()
	deps, _ := w.controlledCallDeps(&testClock{now: time.Now()})
	claimed, ok := claimedByCatalog(deps.catalog)(catalog.ModelID(uuid.MustParse(
		w.query("SELECT id::text FROM catalog.model WHERE slug = 'anthropic/claude-opus-5-5'"))))
	if !ok || claimed.Author != "anthropic" || !slices.Contains(claimed.Others, "openai") || slices.Contains(claimed.Others, "anthropic") {
		t.Fatalf("the claim: %+v, ok %v", claimed, ok)
	}
	if _, ok := claimedByCatalog(deps.catalog)(catalog.ModelID(uuid.NewV7())); ok {
		t.Fatal("an unknown model still claims something")
	}
}
