package kv

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

type registryWorld struct {
	db    *pg.DB
	scope pg.Scope
}

func newRegistryWorld(t *testing.T) registryWorld {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return registryWorld{db: db, scope: pg.PlatformScope()}
}

func (w registryWorld) start(t *testing.T, id string) *Client {
	t.Helper()
	c := &Client{hear: newHeartbeat(w.db, w.scope, id)}
	if err := c.hear.start(t.Context(), 3, &atomic.Int64{}, newMetrics(noop.NewMeterProvider().Meter("test"))); err != nil {
		t.Fatal(err)
	}
	return c
}

func registryView(t *testing.T, c *Client) []ShareNode {
	t.Helper()
	nodes, err := c.ShareView(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

func (w registryWorld) age(t *testing.T, seconds int) {
	t.Helper()
	if err := w.db.Tx(t.Context(), w.scope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE platform.nodes SET seen_at=clock_timestamp()-make_interval(secs=>$1)`, seconds)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialShareRegistryRejectsStaleMembership(t *testing.T) {
	t.Parallel()
	w := newRegistryWorld(t)
	a := w.start(t, "a")
	view := registryView(t, a)
	if len(view) != 1 || view[0].ID != a.ShareID() || len(view[0].View) != 0 {
		t.Fatalf("initial membership: %+v", view)
	}
	if view[0].Remaining <= 0 || view[0].Remaining > AliveFor {
		t.Fatalf("heartbeat lifetime: %s", view[0].Remaining)
	}
	old := []string{a.ShareID()}
	if err := a.ConfirmShares(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	b := w.start(t, "b")
	if err := a.ConfirmShares(t.Context(), old); err == nil {
		t.Fatal("accepted stale membership")
	}
	ids := []string{a.ShareID(), b.ShareID()}
	if err := a.ConfirmShares(t.Context(), ids); err != nil {
		t.Fatal(err)
	}
	view = registryView(t, b)
	if !slices.Equal(view[0].View, ids) {
		t.Fatalf("peer did not see acknowledgement: %+v", view)
	}
	if err := a.ConfirmShares(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if len(registryView(t, b)[0].View) != 0 {
		t.Fatal("expired local lease retained acknowledgement")
	}
}

func TestCredentialShareRegistryFencesOldProcessesAndKeepsTheirMinute(t *testing.T) {
	t.Parallel()
	w := newRegistryWorld(t)
	a := w.start(t, "a")
	if err := a.ConfirmShares(t.Context(), []string{a.ShareID()}); err != nil {
		t.Fatal(err)
	}
	restarted := w.start(t, "a")
	if err := a.ConfirmShares(t.Context(), nil); err == nil {
		t.Fatal("old process wrote new process's acknowledgement")
	}
	view := registryView(t, restarted)
	if len(view) != 2 {
		t.Fatalf("expected live and departed incarnation: %+v", view)
	}
	for _, n := range view {
		switch n.ID {
		case a.ShareID():
			if n.Remaining <= 0 || !slices.Equal(n.View, []string{a.ShareID()}) {
				t.Fatalf("restart lost the old minute: %+v", n)
			}
		case restarted.ShareID():
			if n.Remaining <= 0 || len(n.View) != 0 {
				t.Fatalf("restart inherited old view: %+v", n)
			}
		default:
			t.Fatalf("unexpected member: %+v", n)
		}
	}
}

func TestAnOldCloseCannotRemoveTheReplacementHeartbeat(t *testing.T) {
	t.Parallel()
	w := newRegistryWorld(t)
	old := w.start(t, "a")
	next := w.start(t, "a")
	if err := old.hear.stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	view := registryView(t, next)
	if !slices.ContainsFunc(view, func(n ShareNode) bool { return n.ID == next.ShareID() && n.Remaining > 0 }) {
		t.Fatal("old close removed new process")
	}
}

func TestAHeartbeatThatReturnsAfterExpiryMustRejoin(t *testing.T) {
	t.Parallel()
	w := newRegistryWorld(t)
	c := w.start(t, "returning")
	if err := c.ConfirmShares(t.Context(), []string{c.ShareID()}); err != nil {
		t.Fatal(err)
	}
	w.age(t, 20)
	nodes := registryView(t, c)
	if len(nodes) != 1 || nodes[0].Remaining > 10*time.Second {
		t.Fatalf("stale heartbeat received a fresh lease: %+v", nodes)
	}
	w.age(t, 31)
	nodes = registryView(t, c)
	if len(nodes) != 1 || nodes[0].Remaining > 0 {
		t.Fatal("expired node is still live")
	}
	if _, err := c.hear.beat(t.Context()); err != nil {
		t.Fatal(err)
	}
	nodes = registryView(t, c)
	if len(nodes) != 1 || len(nodes[0].View) != 0 {
		t.Fatalf("returning node kept its old confirmation: %+v", nodes)
	}
}

func TestDeparturesReserveRPMThroughCloseAndGarbageCollection(t *testing.T) {
	for _, graceful := range []bool{false, true} {
		t.Run(map[bool]string{false: "crash", true: "close"}[graceful], func(t *testing.T) { checkDepartedPartition(t, graceful) })
	}
}

func checkDepartedPartition(t *testing.T, graceful bool) {
	t.Helper()
	t.Parallel()
	w := newRegistryWorld(t)
	c := w.start(t, "leaving")
	ids := []string{c.ShareID()}
	if err := c.ConfirmShares(t.Context(), ids); err != nil {
		t.Fatal(err)
	}
	if graceful {
		if err := c.hear.stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	} else {
		w.age(t, 61)
		if err := w.db.Tx(t.Context(), w.scope, func(tx pg.Tx) error { return forget(t.Context(), tx) }); err != nil {
			t.Fatal(err)
		}
	}
	nodes := registryView(t, c)
	if len(nodes) != 1 || nodes[0].Remaining > 0 || !slices.Equal(nodes[0].View, ids) {
		t.Fatalf("lost departed RPM partition: %+v", nodes)
	}
	if err := w.db.Tx(t.Context(), w.scope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE platform.credential_share_departures SET expires_at=clock_timestamp()-interval '1 second'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(registryView(t, c)) != 0 {
		t.Fatal("departed RPM partition did not expire")
	}
}
