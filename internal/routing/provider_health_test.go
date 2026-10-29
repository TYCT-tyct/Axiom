package routing

import (
	"slices"
	"testing"
	"time"

	"axiom/internal/catalog"
)

func TestProviderModelFallbackSurvivesOwnedProbe(t *testing.T) {
	t.Parallel()
	primary, alternate := catalog.ModelID{1}, catalog.ModelID{2}
	for _, ownedModel := range []catalog.ModelID{primary, {3}} {
		now := time.Unix(3600, 0)
		var order []ranked
		for i, model := range []catalog.ModelID{ownedModel, primary, primary, alternate} {
			c := &candidate{}
			c.ID, c.Model = EndpointID{byte(i + 1)}, model
			order = append(order, ranked{candidate: c})
		}
		order[0].Owner.Account = AccountID{1}
		order[0].opened = now.Add(-10 * time.Minute)
		n := &need{over: &Override{NoFallbacks: true}, now: now}
		var record Record
		kept := healthOrder(order, n, &record, MaxEntries)
		if !slices.ContainsFunc(kept, func(c ranked) bool { return c.Model == alternate }) {
			t.Fatalf("owned model %v displaced the alternate model", ownedModel)
		}
	}
}
