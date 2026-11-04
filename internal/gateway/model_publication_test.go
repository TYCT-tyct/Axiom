package gateway

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// The publication callback observes the write gate while both stores are still protected; readers then see the
// matching list and price. TryRLock makes the exclusion assertion deterministic, without a scheduling timeout.
func TestModelPublicationExcludesReaders(t *testing.T) {
	t.Parallel()
	models := NewModelList()
	router, err := routing.New(routing.Config{})
	if err != nil {
		t.Fatal(err)
	}
	list := orListing(t, 1)
	list.built = time.Now()
	m := list.models[0]
	price, err := catalog.ParseSchedule([]byte(cheap))
	if err != nil {
		t.Fatal(err)
	}
	version := uuid.NewV7()
	snap, err := router.NewSnapshot(list.built, []routing.Endpoint{{
		ID: routing.EndpointID(uuid.NewV7()), Model: m.model, Enabled: true, BalanceOK: true, Egresses: 1,
		Context: 200000, MaxOutput: 1000, Streams: true, ServiceTiers: 1 << catalog.ServiceTierStandard,
		Native: routing.Of(protocol.Messages), Served: routing.Of(protocol.Messages),
		Price: routing.PriceRef{Version: version, Schedule: price},
	}}, presets())
	if err != nil {
		t.Fatal(err)
	}
	d := RouteDeps{Router: router, Models: models, Built: func(Count) {
		if models.mu.TryRLock() {
			models.mu.RUnlock()
			t.Error("a reader can enter an unfinished publication")
		}
	}}
	d.Publish(snap, list, Count{})
	if got := models.published(); got != list {
		t.Error("discovery did not read the published list")
	}
	plan, _, err := models.plan(router, call(m.model))
	if err != nil || len(plan.Entries) != 1 || plan.Entries[0].Price.Version != version {
		t.Errorf("planning did not read the published price version: %+v, %v", plan, err)
	}
}
