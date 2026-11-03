package main

import (
	"net/http"
	"testing"

	"axiom/internal/platform/adminapi"
)

// TestAdminSourceClasses: the seeds, a new class with a rule, idempotent create, a stale edit refused, a seeded class
// that cannot be retired, and the retirement of the new class.
//
//nolint:cyclop,gocognit // one fixture walks the resource from list and create through edit, refusal and retirement
func TestAdminSourceClasses(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	path := "/source-classes"

	rec := f.do(http.MethodGet, path, "", as(s))
	expectAdmin(t, "list seeds", rec, 200, "")
	var list adminapi.SourceClassList
	decode(t, rec, &list)
	if len(list.Classes) != 3 || list.Classes[0].Slug != "official" || list.Classes[1].Slug != "cloud" ||
		list.Classes[2].Slug != "relay" || !list.Classes[2].Fallback || !list.Classes[2].System {
		t.Fatalf("seeds: %s", rec.Body)
	}

	body := `{"slug":"pool","name":"Pool","name_zh":"号池","description":"Shared subscription accounts",` +
		`"position":500,"rules":{"match":"all","conditions":[{"evidence":"header","name":"x-pool","operator":"present"}]},` +
		`"reason":"recognize the pool"}`
	key := newKey()
	for i := range 2 {
		rec = f.change(s, http.MethodPost, path, body, key)
		expectAdmin(t, "create", rec, 201, "")
		if i == 1 && rec.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatal("not replayed")
		}
	}
	var pool adminapi.SourceClass
	decode(t, rec, &pool)
	if pool.Slug != "pool" || pool.Position != 500 || pool.Revision != 1 || pool.Rules.Conditions[0].Name == nil ||
		*pool.Rules.Conditions[0].Name != "X-Pool" {
		t.Fatalf("created: %s", rec.Body)
	}
	if n := f.auditCount("source_class.create"); n != "1" {
		t.Fatalf("create audit entries %s", n)
	}

	rec = f.do(http.MethodGet, path, "", as(s))
	decode(t, rec, &list)
	if len(list.Classes) != 4 || list.Classes[2].Slug != "pool" {
		t.Fatalf("after create: %s", rec.Body)
	}

	rec = f.change(s, http.MethodPut, path+"/pool", `{"name":"Account pool","rules":{"match":"any","conditions":[]},"rev":1,"reason":"rename"}`, newKey())
	expectAdmin(t, "update", rec, 200, "")
	decode(t, rec, &pool)
	if pool.Name != "Account pool" || pool.Revision != 2 || pool.Rules.Match != "any" {
		t.Fatalf("updated: %s", rec.Body)
	}
	if n := f.auditCount("source_class.update"); n != "1" {
		t.Fatalf("update audit entries %s", n)
	}

	// A stale revision changes nothing; a seeded class cannot be retired; the fallback has no rules.
	expectAdmin(t, "stale update", f.change(s, http.MethodPut, path+"/pool", `{"name":"Other","rev":1,"reason":"stale"}`, newKey()), 409, codeStaleRevision)
	rec = f.do(http.MethodGet, path, "", as(s))
	decode(t, rec, &list)
	if list.Classes[2].Name != "Account pool" {
		t.Fatalf("stale update changed the class: %s", rec.Body)
	}
	expectAdmin(t, "retire a seed", f.change(s, http.MethodDelete, path+"/official", `{"rev":1,"reason":"no"}`, newKey()), 409, codeConflict)
	expectAdmin(t, "rules on the fallback", f.change(s, http.MethodPut, path+"/relay",
		`{"rules":{"match":"all","conditions":[{"evidence":"header","name":"X-Pool","operator":"present"}]},"rev":1,"reason":"no"}`, newKey()), 400, "")

	rec = f.change(s, http.MethodDelete, path+"/pool", `{"rev":2,"reason":"no longer used"}`, newKey())
	expectAdmin(t, "retire", rec, 204, "")
	if n := f.auditCount("source_class.delete"); n != "1" {
		t.Fatalf("delete audit entries %s", n)
	}
	rec = f.do(http.MethodGet, path, "", as(s))
	decode(t, rec, &list)
	if len(list.Classes) != 3 {
		t.Fatalf("after retire: %s", rec.Body)
	}

	// The slug stays reserved after retirement.
	expectAdmin(t, "reuse a retired slug", f.change(s, http.MethodPost, path,
		`{"slug":"pool","name":"Again","rules":{"match":"all","conditions":[]},"reason":"reuse"}`, newKey()), 409, codeConflict)
}

// TestAdminSourceClassValidation names the field a bad rule must fix.
func TestAdminSourceClassValidation(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	rec := f.change(s, http.MethodPost, "/source-classes",
		`{"slug":"Pool","name":"Pool","rules":{"match":"all","conditions":[]},"reason":"bad slug"}`, newKey())
	expectAdmin(t, "bad slug", rec, 400, "")
	if d := errorDetailOf(t, rec); d.Fields == nil || (*d.Fields)[0].Pointer != "/slug" {
		t.Fatalf("field: %s", rec.Body)
	}
	rec = f.change(s, http.MethodPost, "/source-classes",
		`{"slug":"pool","name":"Pool","rules":{"match":"all","conditions":[{"evidence":"header","name":"X-Pool","operator":"regex"}]},"reason":"bad rule"}`, newKey())
	expectAdmin(t, "bad rule", rec, 400, "")
	if d := errorDetailOf(t, rec); d.Fields == nil || (*d.Fields)[0].Pointer != "/rules/conditions/0/operator" {
		t.Fatalf("field: %s", rec.Body)
	}
}

// TestAdminSourceClassDefaultPosition: a class created without a position goes after the last class before the
// fallback, leaving the fallback last.
func TestAdminSourceClassDefaultPosition(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	rec := f.change(s, http.MethodPost, "/source-classes",
		`{"slug":"auto","name":"Auto","rules":{"match":"all","conditions":[]},"reason":"no position"}`, newKey())
	expectAdmin(t, "create without a position", rec, 201, "")
	var created adminapi.SourceClass
	decode(t, rec, &created)
	if created.Position != 20 {
		t.Fatalf("position: %d, want 20 after the seeds", created.Position)
	}
}
