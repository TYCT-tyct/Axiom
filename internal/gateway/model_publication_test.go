package gateway

import (
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// Publication must exclude both kinds of readers. Pausing inside Publish proves the guards on publication, planning
// and discovery together, without scheduling sleeps or any database work under the publication lock.
func TestModelPublicationExcludesReaders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
			Context: 200000, MaxOutput: 1000, ServiceTiers: 1 << catalog.ServiceTierStandard,
			Native: routing.Of(protocol.Messages), Served: routing.Of(protocol.Messages),
			Price: routing.PriceRef{Version: version, Schedule: price},
		}}, presets())
		if err != nil {
			t.Fatal(err)
		}
		stored, resume, published := make(chan struct{}), make(chan struct{}), make(chan struct{})
		d := RouteDeps{Router: router, Models: models, Built: func(Count) { close(stored); <-resume }}
		go func() { d.Publish(snap, list, Count{}); close(published) }()
		<-stored
		listed := make(chan *openRouterListing, 1)
		type result struct {
			plan routing.Plan
			err  error
		}
		planned := make(chan result, 1)
		go func() { listed <- models.published() }()
		go func() { plan, _, err := models.plan(router, call(m.model)); planned <- result{plan, err} }()
		synctest.Wait()
		if len(listed) != 0 || len(planned) != 0 {
			t.Error("a reader passed an unfinished publication")
		}
		close(resume)
		<-published
		if got := <-listed; got != list {
			t.Error("discovery did not read the published list")
		}
		if got := <-planned; got.err != nil || len(got.plan.Entries) != 1 || got.plan.Entries[0].Price.Version != version {
			t.Errorf("planning did not read the published price version: %+v", got)
		}
	})
}
