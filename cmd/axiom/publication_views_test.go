package main

import (
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers"
)

type publicationViewFixture struct {
	world     *callWorld
	node      adminFixture
	snap      *catalog.Snapshot
	endpoints []suppliers.Routable
}

func newPublicationView(t *testing.T) publicationViewFixture {
	t.Helper()
	w := newCallWorld(t)
	w.price()
	node := startAdminNode(w.commandLine, &testClock{now: time.Now()})
	site, ok := node.svc.MainSite()
	if !ok {
		t.Fatal("missing main site")
	}
	var snap *catalog.Snapshot
	if err := w.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) (err error) {
		if err := refreshSalePrices(t.Context(), tx, w.site, site.Currency); err != nil {
			return err
		}
		snap, err = catalog.ReadSnapshot(t.Context(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	full, err := node.upstreams.Routable()
	if err != nil {
		t.Fatal(err)
	}
	return publicationViewFixture{w, node, snap, full}
}

func TestLocalPriceViewsNeverRepublish(t *testing.T) {
	f := newPublicationView(t)
	counts := func() string {
		return f.world.query(`SELECT jsonb_build_array((SELECT count(*) FROM billing.price_versions),(SELECT count(*) FROM platform.audit_log),(SELECT count(*) FROM platform.alert_signals),(SELECT jsonb_agg(candidate_id ORDER BY model_id) FROM billing.price_publications))::text`)
	}
	before := counts()
	read := salePrices(f.world.db, f.node.svc)
	for range 4 {
		for _, view := range [][]suppliers.Routable{f.endpoints, f.endpoints[:1], f.endpoints[1:], nil} {
			if _, err := read(t.Context(), f.snap, view, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if after := counts(); before != after {
		t.Fatalf("node views changed publication state: before %s after %s", before, after)
	}
}

func TestLocalPriceCostGuard(t *testing.T) {
	f := newPublicationView(t)
	expensive := append([]suppliers.Routable(nil), f.endpoints[:1]...)
	var err error
	expensive[0].Schedule, err = expensive[0].Schedule.Scaled("1000", expensive[0].Schedule.Currency())
	if err != nil {
		t.Fatal(err)
	}
	prices, err := salePrices(f.world.db, f.node.svc)(t.Context(), f.snap, expensive, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := prices[catalog.ModelID(expensive[0].Model)]; ok {
		t.Fatal("published version sold below this node's known cost")
	}
}

func TestCanonicalPricesIgnoreNodeKeyAvailability(t *testing.T) {
	f := newPublicationView(t)
	before := f.world.query("SELECT jsonb_agg(id ORDER BY id)::text FROM billing.price_versions")
	if err := pgtest.Run(t.Context(), f.world.database.Superuser, "UPDATE suppliers.credentials SET key_version=999"); err != nil {
		t.Fatal(err)
	}
	if err := f.node.upstreams.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	local, err := f.node.upstreams.Routable()
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 0 {
		t.Fatal("missing master-key version did not remove this node's suppliers")
	}
	site, ok := f.node.svc.MainSite()
	if !ok {
		t.Fatal("missing main site")
	}
	if err := f.world.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error { return refreshSalePrices(t.Context(), tx, f.world.site, site.Currency) }); err != nil {
		t.Fatal(err)
	}
	if after := f.world.query("SELECT jsonb_agg(id ORDER BY id)::text FROM billing.price_versions"); after != before {
		t.Fatalf("node key availability republished prices: %s -> %s", before, after)
	}
}
