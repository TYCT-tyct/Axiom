package suppliers

import (
	"context"
	"fmt"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// group makes a group of the supplier, as the sync does.
func (a admins) group(supplier uuid.UUID, ref string, present bool) uuid.UUID {
	a.t.Helper()
	id := uuid.NewV7()
	a.sql(fmt.Sprintf(`INSERT INTO suppliers.supplier_groups (id, supplier_id, upstream_ref, name, ratio, present)
		VALUES ('%s', '%s', '%s', 'Group %s', 1.5, %v)`, id, supplier, ref, ref, present))
	return id
}

func (a admins) choose(supplier, group uuid.UUID, selected bool) (Group, error) {
	return inTx(a, func(ctx context.Context, tx pg.Tx) (Group, error) {
		return a.admin.SelectGroup(ctx, tx, a.actor, supplier, group, selected, "needed")
	})
}

// TestSelectGroup: the first selection gives the group its code name, which unselecting keeps and selecting again does
// not change; names differ between groups; each change of selection raises the generation.
func TestSelectGroup(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	one, two := a.group(id, "1", true), a.group(id, "2", true)
	first, err := a.choose(id, one, true)
	refuses(t, "a first selection", err, nil)
	same(t, "its shape", []any{true, 9, byte('g')}, []any{first.Selected, len(first.Codename), first.Codename[0]})
	off, err := a.choose(id, one, false)
	refuses(t, "unselecting", err, nil)
	same(t, "unselected, name kept", []any{false, first.Codename}, []any{off.Selected, off.Codename})
	again, err := a.choose(id, one, true)
	refuses(t, "selecting again", err, nil)
	same(t, "the name", first.Codename, again.Codename)
	other, err := a.choose(id, two, true)
	refuses(t, "another group", err, nil)
	if other.Codename == first.Codename {
		t.Errorf("two groups share the name %q", other.Codename)
	}
	same(t, "the generation", "4", a.generation())
}

// TestSelectGroupRefusals: a group the supplier no longer lists cannot be selected but can be unselected; one the
// supplier lacks is refused.
func TestSelectGroupRefusals(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	gone := a.group(id, "3", false)
	_, err := a.choose(id, gone, true)
	refuses(t, "a group the supplier no longer lists", err, ErrGroupGone)
	g, err := a.choose(id, gone, false)
	refuses(t, "unselecting a group that is gone", err, nil)
	same(t, "its selection", false, g.Selected)
	_, err = a.choose(id, uuid.NewV7(), true)
	refuses(t, "a group the supplier lacks", err, ErrNoGroup)
	other := a.connect("https://other.example.com", "ops@example.com")
	_, err = a.choose(other.ID, gone, true)
	refuses(t, "a group of another supplier", err, ErrNoGroup)
}

// TestBindKey: a key is bound to a selected group once, `binding` with no key text yet; a group that is not selected or
// not the supplier's, and a key already bound, are refused.
func TestBindKey(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	chosen, idle := a.group(id, "1", true), a.group(id, "2", true)
	if _, err := a.choose(id, chosen, true); err != nil {
		t.Fatal(err)
	}
	bind := func(group uuid.UUID, ref string) (Credential, error) {
		return inTx(a, func(ctx context.Context, tx pg.Tx) (Credential, error) {
			return a.admin.BindKey(ctx, tx, a.actor, id, group, ref, "a key made at the supplier")
		})
	}
	c, err := bind(chosen, "key-7")
	refuses(t, "a key", err, nil)
	same(t, "the key", Credential{ID: c.ID, Group: chosen, UpstreamKeyRef: "key-7", Status: "binding", MaxConcurrency: defaultMaxConcurrency}, c)
	same(t, "a key with no text yet", "true", a.text(`SELECT (ciphertext IS NULL)::text FROM suppliers.credentials`))
	_, err = bind(chosen, "key-7")
	refuses(t, "the same key again", err, ErrKeyBound)
	_, err = bind(idle, "key-8")
	refuses(t, "a group that is not selected", err, ErrGroupNotSelected)
	_, err = bind(uuid.NewV7(), "key-9")
	refuses(t, "a group the supplier lacks", err, ErrNoGroup)
	same(t, "audit entries", "1", a.text(`SELECT count(*)::text FROM platform.audit_log WHERE action = 'credential.bind'`))
}

// model makes a row of the supplier's model list, as the sync does: an upstream name no model is mapped to, first seen
// ago before now.
func (a admins) model(supplier, group uuid.UUID, name string, ago time.Duration) {
	a.t.Helper()
	a.sql(fmt.Sprintf(`INSERT INTO suppliers.supplier_models (supplier_id, supplier_group_id, upstream_name, formats, first_seen_at)
		VALUES ('%s', '%s', '%s', '{chat,messages}', now() - interval '%d seconds')`, supplier, group, name, int(ago.Seconds())))
}

func (a admins) pending(q PendingQuery) ([]PendingMapping, bool) {
	a.t.Helper()
	type page struct {
		names []PendingMapping
		more  bool
	}
	p, err := inTx(a, func(ctx context.Context, tx pg.Tx) (page, error) {
		names, more, err := a.admin.PendingMappings(ctx, tx, q)
		return page{names, more}, err
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return p.names, p.more
}

func namesOf(names []PendingMapping) []string {
	out := []string{}
	for _, n := range names {
		out = append(out, n.UpstreamName)
	}
	return out
}

func (a admins) mapIn(supplier uuid.UUID, group *uuid.UUID, name string, model uuid.UUID) (Mapping, error) {
	return inTx(a, func(ctx context.Context, tx pg.Tx) (Mapping, error) {
		return a.admin.MapModel(ctx, tx, a.actor, supplier, group, name, model, "mapped")
	})
}

// queue is a supplier with two groups holding, by age, oldest first: "oldest" in group one, "newest" in both.
func (a admins) queue() (supplier, one, two uuid.UUID) {
	supplier = a.connect("https://api.example.com", "ops@example.com").ID
	one, two = a.group(supplier, "1", true), a.group(supplier, "2", true)
	a.model(supplier, one, "newest", time.Minute)
	a.model(supplier, one, "oldest", time.Hour)
	a.model(supplier, two, "newest", 2*time.Minute)
	return supplier, one, two
}

// TestPendingMappingsAreWorkedOldestFirst: the queue lists the names no model is mapped to, oldest first, in pages that
// follow each other.
func TestPendingMappingsAreWorkedOldestFirst(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id, one, _ := a.queue()
	all, more := a.pending(PendingQuery{PageSize: 3}) // exactly the names there are: no more
	same(t, "the queue", []any{[]string{"oldest", "newest", "newest"}, false, id, one}, []any{namesOf(all), more, all[0].Supplier, all[0].Group})
	first, more := a.pending(PendingQuery{PageSize: 2})
	rest, moreRest := a.pending(PendingQuery{PageSize: 2, After: &first[1]})
	same(t, "pages of 2", []any{[]string{"oldest", "newest"}, true, []string{"newest"}, false, one},
		[]any{namesOf(first), more, namesOf(rest), moreRest, rest[0].Group})
	other, _ := a.pending(PendingQuery{Supplier: uuid.NewV7(), PageSize: 10})
	same(t, "another supplier's queue", []string{}, namesOf(other))
}

// TestAnAliasTakesANameOutOfTheQueue: a name leaves the queue when an alias covers it, in its group or in the whole
// supplier, and a name mapped to a model by the sync was never in it.
func TestAnAliasTakesANameOutOfTheQueue(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id, one, two := a.queue()
	a.sql(fmt.Sprintf(`UPDATE suppliers.supplier_models SET model_id = '%s' WHERE upstream_name = 'oldest'`, uuid.NewV7()))
	left, _ := a.pending(PendingQuery{PageSize: 10})
	same(t, "with a name already mapped", []string{"newest", "newest"}, namesOf(left))
	_, err := a.mapIn(id, &one, "newest", uuid.NewV7())
	refuses(t, "an alias in group one", err, nil)
	left, _ = a.pending(PendingQuery{PageSize: 10})
	same(t, "after an alias in group one", []any{[]string{"newest"}, two}, []any{namesOf(left), left[0].Group})
	_, err = a.mapIn(id, nil, "newest", uuid.NewV7())
	refuses(t, "an alias in the whole supplier", err, nil)
	left, _ = a.pending(PendingQuery{PageSize: 10})
	same(t, "after an alias in the whole supplier", []string{}, namesOf(left))
}

// TestMapModel: an alias is made once in a scope and never changed; the same name in another scope is another alias;
// a group the supplier lacks, and a supplier being removed, are refused.
func TestMapModel(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	group := a.group(id, "1", true)
	model := uuid.NewV7()
	whole, err := a.mapIn(id, nil, "gpt-4", model)
	refuses(t, "an alias for the supplier", err, nil)
	same(t, "the alias", Mapping{ID: whole.ID, Supplier: id, UpstreamName: "gpt-4", Model: model}, whole)
	_, err = a.mapIn(id, nil, "gpt-4", model)
	refuses(t, "the same name in the same scope", err, ErrAliasExists)
	narrow, err := a.mapIn(id, &group, "gpt-4", model)
	refuses(t, "the same name in a narrower scope", err, nil)
	same(t, "its group", &group, narrow.Group)
	other := uuid.NewV7()
	_, err = a.mapIn(id, &other, "gpt-5", model)
	refuses(t, "a group the supplier lacks", err, ErrNoGroup)
	a.setStatus(id, "removing")
	_, err = a.mapIn(id, nil, "gpt-6", model)
	refuses(t, "an alias for a supplier being removed", err, ErrStatusLocked)
}
