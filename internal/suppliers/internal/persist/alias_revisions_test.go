package persist_test

import (
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
	"axiom/internal/suppliers/internal/store"
)

func TestProjectionUsesCurrentGroupMappingRevision(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	one := c.model("model-one", catalog.StatusListed)
	two := c.model("model-two", catalog.StatusListed)
	groups := []listedGroup{{ref: "g", name: "g", ratio: "1", facts: "{}", models: []listedModel{{name: "alias", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice}}}}
	node := c.node("one")
	s := c.acquire(node)
	c.commitListing(s, 1, groups)
	if err := s.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	var group uuid.UUID
	c.value(&group, "SELECT id FROM suppliers.supplier_groups WHERE supplier_id = $1", c.supplier)
	admin, err := suppliers.NewAdmin(suppliers.AdminConfig{Keys: c.keys, Clock: c.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	mapTo := func(model catalog.ModelID, rev *int64) {
		t.Helper()
		if err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
			_, err := admin.MapModel(t.Context(), tx, audit.System("suppliers.test"), c.supplier, &group, "alias", uuid.UUID(model), rev, "correct the group model")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	mapTo(one, nil)
	s = c.acquire(node)
	c.commitListing(s, 2, groups)
	same(t, "initial endpoint", c.column("endpoints", "model_id::text", "id"), []string{uuid.UUID(one).String()})
	oldCosts := c.marks("cost_schedules", "id::text")
	service := c.callSnapshot(group)
	checkSnapshotModel(t, service, one)
	if err := s.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	rev := int64(1)
	mapTo(two, &rev)
	s = c.acquire(node)
	c.commitListing(s, 3, groups)
	if err := service.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkSnapshotModel(t, service, two)
	var mapped uuid.UUID
	c.value(&mapped, "SELECT model_id FROM suppliers.supplier_models WHERE supplier_group_id = $1 AND upstream_name = 'alias'", group)
	if mapped != uuid.UUID(two) {
		t.Fatalf("projection used %s, want current mapping %s", mapped, two)
	}
	var oldState string
	c.value(&oldState, "SELECT cost_state FROM suppliers.endpoints WHERE model_id = $1", uuid.UUID(one))
	if oldState != "unknown" {
		t.Fatalf("old endpoint still routable: %s", oldState)
	}
	var unchanged string
	c.value(&unchanged, "SELECT id::text || ' ' || xmin::text FROM suppliers.cost_schedules WHERE endpoint_id = (SELECT id FROM suppliers.endpoints WHERE model_id = $1)", uuid.UUID(one))
	if len(oldCosts) != 1 || unchanged != oldCosts[0] {
		t.Fatalf("historical schedule changed: %v -> %s", oldCosts, unchanged)
	}
}

func (c *cluster) callSnapshot(group uuid.UUID) *suppliers.Service {
	c.t.Helper()
	ctx := c.t.Context()
	if err := pgtest.Run(ctx, c.database.Superuser, fmt.Sprintf("UPDATE suppliers.suppliers SET kind = 'newapi' WHERE id = '%s'", c.supplier)); err != nil {
		c.t.Fatal(err)
	}
	err := c.open().Tx(ctx, platform, func(tx pg.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE suppliers.supplier_groups SET selected = true, codename = 'test-group' WHERE id = $1", group); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE suppliers.suppliers SET status = 'active', balance_native = 100 WHERE id = $1", c.supplier); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ($1, 1, 'test', 'snapshot test')", c.supplier); err != nil {
			return err
		}
		id := uuid.NewV7()
		sealed := c.keys.Seal(secret.New("placeholder-key"), secret.Location{Table: "suppliers.credentials", RowID: id.String(), Purpose: "key"})
		version := int64(sealed.KeyVersion)
		return store.New().InsertCredential(ctx, tx, store.InsertCredentialParams{
			ID: id, SupplierID: c.supplier, SupplierGroupID: group, UpstreamKeyRef: "7", Status: "active", MaxConcurrency: 4,
			Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK, KeyVersion: &version,
		})
	})
	if err != nil {
		c.t.Fatal(err)
	}
	service, err := suppliers.New(suppliers.Config{DB: c.open(), Scope: platform, Keys: c.keys, Clock: c.clock.now})
	if err != nil {
		c.t.Fatal(err)
	}
	if err := service.Reload(ctx); err != nil {
		c.t.Fatal(err)
	}
	return service
}

func checkSnapshotModel(t *testing.T, service *suppliers.Service, model catalog.ModelID) {
	t.Helper()
	endpoints, err := service.Routable()
	if err != nil || len(endpoints) != 1 || endpoints[0].Model != uuid.UUID(model) {
		t.Fatalf("snapshot = %+v, %v; want current model %s", endpoints, err, model)
	}
}
