package suppliers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

func (a admins) reviseIn(supplier, group uuid.UUID, name string, model uuid.UUID, rev int64) (Mapping, error) {
	return inTx(a, func(ctx context.Context, tx pg.Tx) (Mapping, error) {
		return a.admin.MapModel(ctx, tx, a.actor, supplier, &group, name, model, nil, &rev, "mapped")
	})
}

func TestGroupMappingRevisionsAndAudit(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	group := a.group(id, "1", true)
	first, err := a.mapIn(id, &group, "alias", uuid.NewV7())
	refuses(t, "create", err, nil)
	second, err := a.reviseIn(id, group, "alias", uuid.NewV7(), first.Revision)
	refuses(t, "correct in the same scope", err, nil)
	if first.ID != second.ID || first.Revision != 1 || second.Revision != 2 {
		t.Fatalf("revisions: %+v -> %+v", first, second)
	}
	retry, err := a.reviseIn(id, group, "alias", second.Model, second.Revision)
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
	rev := int64(1)
	_, err = inTx(a, func(ctx context.Context, tx pg.Tx) (Mapping, error) {
		return a.admin.MapModel(ctx, tx, audit.Actor{}, id, &group, "alias", uuid.NewV7(), nil, &rev, "must be audited")
	})
	if err == nil {
		t.Fatal("revision succeeded without an actor")
	}
	same(t, "rolled back revision", "0", a.text("SELECT count(*)::text FROM suppliers.supplier_alias_revisions"))
}

func TestMappingRequiresExplicitCurrentRevision(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	group := a.group(id, "1", true)
	first, err := a.mapIn(id, &group, "alias", uuid.NewV7())
	refuses(t, "create", err, nil)
	_, err = a.mapIn(id, &group, "alias", first.Model)
	refuses(t, "create cannot revise even the same model", err, ErrAliasExists)
	_, err = a.reviseIn(id, group, "alias", uuid.NewV7(), first.Revision+1)
	refuses(t, "stale correction", err, ErrAliasRevision)
	_, err = a.reviseIn(id, group, "missing", uuid.NewV7(), first.Revision)
	refuses(t, "correction cannot create", err, ErrAliasRevision)
	second, err := a.reviseIn(id, group, "alias", uuid.NewV7(), first.Revision)
	refuses(t, "current correction", err, nil)
	_, err = a.reviseIn(id, group, "alias", second.Model, first.Revision)
	refuses(t, "same model cannot bypass a stale revision", err, ErrAliasRevision)
}

func TestConcurrentMappingCorrectionsRequireCurrentRevision(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	group := a.group(id, "1", true)
	first, err := a.mapIn(id, &group, "alias", uuid.NewV7())
	refuses(t, "create", err, nil)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := a.reviseIn(id, group, "alias", uuid.NewV7(), first.Revision)
			results <- err
		})
	}
	wg.Wait()
	one, two := <-results, <-results
	if one != nil {
		one, two = two, one
	}
	if one != nil || !errors.Is(two, ErrAliasRevision) {
		t.Fatalf("two corrections of revision %d must have one stale loser: %v, %v", first.Revision, one, two)
	}
	same(t, "one concurrent correction", "2", a.text("SELECT max(revision)::text FROM suppliers.supplier_alias_revisions"))
}
