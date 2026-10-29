package suppliers

import (
	"context"
	"strings"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

func TestGroupMappingRevisionsAndAudit(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	group := a.group(id, "1", true)
	first, err := a.mapIn(id, &group, "alias", uuid.NewV7())
	refuses(t, "create", err, nil)
	second, err := a.mapIn(id, &group, "alias", uuid.NewV7())
	refuses(t, "correct in the same scope", err, nil)
	if first.ID != second.ID || first.Revision != 1 || second.Revision != 2 {
		t.Fatalf("revisions: %+v -> %+v", first, second)
	}
	retry, err := a.mapIn(id, &group, "alias", second.Model)
	refuses(t, "repeat current model", err, nil)
	same(t, "retry", second, retry)
	same(t, "immutable original", first.Model.String(), a.text("SELECT model_id::text FROM suppliers.supplier_aliases WHERE id = $1", first.ID))
	same(t, "history count", "1", a.text("SELECT count(*)::text FROM suppliers.supplier_alias_revisions"))
	entry := a.text(`SELECT actor_id || '|' || detail::text FROM platform.audit_log WHERE action = 'supplier_alias.revise'`)
	for _, want := range []string{first.Model.String(), second.Model.String(), strings.TrimPrefix(a.actor.String(), "user:"), `"revision": 2`} {
		if !strings.Contains(entry, want) {
			t.Errorf("audit %s lacks %s", entry, want)
		}
	}
	var wg sync.WaitGroup
	results := make(chan Mapping, 2)
	for range 2 {
		wg.Go(func() {
			m, err := a.mapIn(id, &group, "alias", uuid.NewV7())
			if err != nil {
				t.Error(err)
			}
			results <- m
		})
	}
	wg.Wait()
	one, two := <-results, <-results
	if one.Revision+two.Revision != 7 || one.Revision == two.Revision {
		t.Fatalf("concurrent revisions must serialize as 3 and 4: %d, %d", one.Revision, two.Revision)
	}
	if !strings.HasPrefix(a.text(`SELECT actor FROM suppliers.supplier_alias_revisions ORDER BY revision DESC LIMIT 1`), "user:") {
		t.Fatal("revision omitted its actor")
	}
}

func TestMappingRevisionRollsBackWithoutAudit(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	group := a.group(id, "1", true)
	_, err := a.mapIn(id, &group, "alias", uuid.NewV7())
	refuses(t, "create", err, nil)
	_, err = inTx(a, func(ctx context.Context, tx pg.Tx) (Mapping, error) {
		return a.admin.MapModel(ctx, tx, audit.Actor{}, id, &group, "alias", uuid.NewV7(), "must be audited")
	})
	if err == nil {
		t.Fatal("revision succeeded without an actor")
	}
	same(t, "rolled back revision", "0", a.text("SELECT count(*)::text FROM suppliers.supplier_alias_revisions"))
}
