package persist_test

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/syncer"
)

// TestAWokenSupplierStillProjectsTheListing: the wake a replaced login runs puts the tasks back to work but keeps what
// each last read, because that is what tells a commit a listing is stored. A round whose catalog task is stopped still
// writes the listing the state holds — by the catalog and by the person's aliases — so a name a person has just mapped
// becomes an endpoint without waiting for the catalog task to succeed once more.
func TestAWokenSupplierStillProjectsTheListing(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	groups := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
		{name: "mystery-model", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
	}}}
	c.commitListing(c.acquire(c.node("the node with the old login")), 1, groups)
	same(t, "the endpoints of a listing with no name mapped yet", c.column("endpoints", "upstream_name", "upstream_name"), nil)

	// The login is replaced: every task is due again with nothing counted against it, and the listing stays read.
	if err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		return persist.Wake(t.Context(), tx, c.supplier, t0)
	}); err != nil {
		t.Fatalf("wake the sync: %v", err)
	}
	var version int64
	c.value(&version, "SELECT version FROM suppliers.sync_states WHERE supplier_id = $1", c.supplier)
	var raw string
	c.value(&raw, "SELECT state::text FROM suppliers.sync_states WHERE supplier_id = $1", c.supplier)
	var woken syncer.State
	if err := jsonx.UnmarshalKnown([]byte(raw), &woken); err != nil {
		t.Fatalf("the state after the wake: %v", err)
	}
	catalogTask := woken.Tasks[syncer.TaskCatalog]
	if version != 2 || !catalogTask.LastOK.Equal(t0) || !catalogTask.Due.IsZero() || catalogTask.Halted {
		t.Errorf("the state after the wake is version %d with the catalog task last read %v, due %v, halted %t; want version 2, last read %v, due at once, running",
			version, catalogTask.LastOK, catalogTask.Due, catalogTask.Halted, t0)
	}

	// The catalog task of the next round cannot read what the supplier answers, so it stops without reading anything; the
	// round still writes the listing the state holds.
	s := c.acquire(c.node("the node with the new login"))
	halted := syncer.Commit{State: woken}
	halted.State.Version = 3
	halted.State.Tasks[syncer.TaskCatalog].Halted = true
	if err := s.Store().Commit(t.Context(), 2, halted); err != nil {
		t.Fatalf("commit the stopped catalog task: %v", err)
	}

	// A person maps the name to a model the catalog has on sale.
	known := c.model("known-model", catalog.StatusListed)
	c.alias(uuid.Nil(), "mystery-model", known)

	// A step of the balance task alone: the catalog task is still stopped and read nothing, and the stored listing is
	// mapped again with the alias, so the endpoint the name was missing appears.
	balance := halted
	balance.State.Version = 4
	balance.State.Balance, balance.State.BalanceAt = "10", t0
	balance.State.Tasks[syncer.TaskBalance] = syncer.TaskState{Due: t0.Add(time.Minute), LastOK: t0}
	if err := s.Store().Commit(t.Context(), 3, balance); err != nil {
		t.Fatalf("commit the balance step: %v", err)
	}
	same(t, "the endpoint of the name a person mapped",
		c.column("endpoints", "upstream_name || ' ' || model_id::text", "upstream_name"), []string{"mystery-model " + known.String()})
}
