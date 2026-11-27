package persist_test

import (
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/syncer"
)

// TestAConfirmedPlanPricesOnlyTheModelItWasConfirmedFor: a plan made of a person's confirmed base makes the endpoint of
// the model the name maps to, which need not have existed before, when the base was confirmed for that model; when the
// name maps to another model now (an alias moved it), the base does not carry its price over and there is no endpoint.
func TestAConfirmedPlanPricesOnlyTheModelItWasConfirmedFor(t *testing.T) {
	t.Parallel()
	for _, confirmedForTheModel := range []bool{true, false} {
		c := newCluster(t)
		gpt := c.model("gpt-4o", catalog.StatusListed)
		other := c.model("gpt-4.1", catalog.StatusListed)
		commit := listing(1, []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
			{name: "gpt-4o", formats: []protocol.Format{protocol.Chat}, plan: &plusVAT},
		}}})
		commit.State.Groups[0].Models[0].Confirmed = true
		confirmedFor := other
		if confirmedForTheModel {
			confirmedFor = gpt
		}
		commit.State.Confirmed = []syncer.ConfirmedBase{{
			GroupRef: "default", Model: "gpt-4o", For: confirmedFor.String(), Base: plusVAT.schedule,
		}}
		if err := c.acquire(c.node("a")).Store().Commit(t.Context(), 0, commit); err != nil {
			t.Fatal(err)
		}
		var want []string
		if confirmedForTheModel {
			want = []string{gpt.String() + " known"}
		}
		same(t, "the endpoints a base confirmed for the mapped model makes",
			c.column("endpoints", "model_id::text || ' ' || cost_state", "id"), want)
	}
}
