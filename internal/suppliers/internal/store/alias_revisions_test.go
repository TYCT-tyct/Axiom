package store_test

import (
	"context"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

func TestAliasRevisionsPreserveOwnershipAndHistory(t *testing.T) {
	t.Parallel()
	db := open(t)
	w := seed(t, db)
	supplier := w.supplier["platform"]
	group := w.group["platform"]
	var alias uuid.UUID
	if err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) error {
		var err error
		alias, err = queries.InsertAlias(ctx, tx, store.InsertAliasParams{SupplierID: supplier, SupplierGroupID: &group, UpstreamName: "alias", ModelID: uuid.NewV7()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	revision := store.InsertAliasRevisionParams{AliasID: alias, SupplierID: supplier, Revision: 2, PreviousModelID: uuid.NewV7(), ModelID: uuid.NewV7(), Actor: "system:test", Reason: "correction"}
	insert := func(ctx context.Context, tx pg.Tx) error { return queries.InsertAliasRevision(ctx, tx, revision) }
	if err := run(t, db, pg.NewScope(false, []uuid.UUID{siteA}, nil), insert); pg.SQLState(err) != insufficientPrivilege {
		t.Fatalf("another tenant inserted a revision: %v", err)
	}
	if err := run(t, db, platform, insert); err != nil {
		t.Fatal(err)
	}
	if got := seen(t, db, pg.Scope{}, w, "suppliers.supplier_alias_revisions", "supplier_id"); len(got) != 0 {
		t.Fatalf("unscoped revision visibility: %v", got)
	}
	for _, sql := range []string{
		"UPDATE suppliers.supplier_alias_revisions SET reason = 'rewrite'",
		"UPDATE suppliers.supplier_aliases SET model_id = uuidv7()",
		"UPDATE suppliers.supplier_aliases SET set_by = 'system'",
		"DELETE FROM suppliers.supplier_alias_revisions",
	} {
		if err := exec(t, db, platform, sql); pg.SQLState(err) != insufficientPrivilege {
			t.Errorf("revision history may be rewritten: %s: %v", sql, err)
		}
	}
	revision.SupplierID = w.supplier["site B"]
	revision.Revision++
	if err := run(t, db, platform, insert); pg.SQLState(err) != foreignKeyViolation {
		t.Fatalf("revision attached to another supplier: %v", err)
	}
}
