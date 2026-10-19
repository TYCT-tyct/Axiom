package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/adminapi"
)

// send sends a change with a fresh key and reads the answer as v.
func (f supplierFixture) send(method, path, body string, status int, v any) {
	f.t.Helper()
	rec := f.change(f.s, method, path, body, newKey())
	expectAdmin(f.t, method+" "+path, rec, status, "")
	decode(f.t, rec, v)
}

// TestSelectGroupAndBindKey: a group is selected and gets its code name, which stays when it is unselected; a key is
// bound to a selected group once; a group that is not selected, one the supplier lacks, and a key bound twice are refused.
func TestSelectGroupAndBindKey(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	group, idle := f.group(supplier, "1", true), f.group(supplier, "2", true)
	base := "/suppliers/" + supplier
	var chosen, off adminapi.SupplierGroup
	f.send(http.MethodPut, base+"/groups/"+group, `{"selected":true,"reason":"r"}`, http.StatusOK, &chosen)
	f.send(http.MethodPut, base+"/groups/"+group, `{"selected":false,"reason":"r"}`, http.StatusOK, &off)
	if !chosen.Selected || chosen.Codename == nil || off.Selected || off.Codename == nil || *off.Codename != *chosen.Codename {
		t.Errorf("selected %+v, then unselected %+v", chosen, off)
	}
	expectAdmin(t, "a group the supplier lacks", f.change(f.s, http.MethodPut, base+"/groups/"+uuid.NewV7().String(), `{"selected":true,"reason":"r"}`, newKey()),
		http.StatusNotFound, codeNotFound)
	f.send(http.MethodPut, base+"/groups/"+group, `{"selected":true,"reason":"r"}`, http.StatusOK, &chosen)

	var bound adminapi.SupplierCredential
	f.send(http.MethodPost, base+"/credentials", `{"group_id":"`+group+`","upstream_key_ref":"key-7","reason":"r"}`, http.StatusCreated, &bound)
	if bound.GroupId != group || bound.UpstreamKeyRef != "key-7" || bound.Status != "binding" || bound.MaxConcurrency != 4 {
		t.Errorf("the bound key: %+v", bound)
	}
	bindRefusals(t, f, supplier, group, idle)
	f.expectAudit("credential.bind", map[string]string{"reason": "r"})
}

// bindRefusals: a key cannot be bound twice, to a group that is not selected or not the supplier's, or to none; the one bound stays.
func bindRefusals(t *testing.T, f supplierFixture, supplier, group, idle string) {
	base := "/suppliers/" + supplier
	for name, c := range map[string]struct {
		body   string
		status int
		code   errorCode
	}{
		"the same key again":              {`{"group_id":"` + group + `","upstream_key_ref":"key-7","reason":"r"}`, http.StatusConflict, codeConflict},
		"a group that is not selected":    {`{"group_id":"` + idle + `","upstream_key_ref":"key-8","reason":"r"}`, http.StatusUnprocessableEntity, codeUnprocessable},
		"a group the supplier lacks":      {`{"group_id":"` + uuid.NewV7().String() + `","upstream_key_ref":"key-9","reason":"r"}`, http.StatusUnprocessableEntity, codeUnprocessable},
		"a group written as none could":   {`{"group_id":"nope","upstream_key_ref":"key-9","reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
		"a key reference that is nothing": {`{"group_id":"` + group + `","upstream_key_ref":"","reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
	} {
		expectAdmin(t, name, f.change(f.s, http.MethodPost, base+"/credentials", c.body, newKey()), c.status, c.code)
	}
	rec := f.do(http.MethodGet, base, "", as(f.s))
	var detail adminapi.SupplierDetail
	decode(t, rec, &detail)
	if len(detail.Groups) != 2 || len(detail.Credentials) != 1 {
		t.Errorf("the supplier after: %+v", detail)
	}
}

// pendingNames reads the pages of the queue of names to map, following the cursor, and returns the names in order and
// the number of pages.
func (f supplierFixture) pendingNames(query string) ([]string, int) {
	f.t.Helper()
	var names []string
	cursor, pages := "", 0
	for {
		path := "/pending-mappings?" + query
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		rec := f.do(http.MethodGet, path, "", as(f.s))
		expectAdmin(f.t, "listPendingMappings "+path, rec, http.StatusOK, "")
		var list adminapi.PendingMappingList
		decode(f.t, rec, &list)
		for _, n := range list.Data {
			names = append(names, n.UpstreamName)
		}
		pages++
		if list.NextCursor == nil || pages > 10 {
			return names, pages
		}
		cursor = *list.NextCursor
	}
}

// TestMapSupplierModel: the queue lists the names no model is mapped to, oldest first, in pages that follow each other;
// an alias takes a name out of it; a model the catalog lacks or has retired, and a name already mapped, are refused.
func TestMapSupplierModel(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	group := f.group(supplier, "1", true)
	for i, name := range []string{"oldest", "middle", "newest"} {
		f.run(fmt.Sprintf(`INSERT INTO suppliers.supplier_models (supplier_id, supplier_group_id, upstream_name, formats, first_seen_at)
			VALUES ('%s', '%s', '%s', '{chat}', now() - interval '%d hours')`, supplier, group, name, 3-i))
	}
	names, pages := f.pendingNames("limit=2")
	if diff := cmp.Diff([]string{"oldest", "middle", "newest"}, names); diff != "" || pages != 2 {
		t.Errorf("the queue in pages of 2: %d pages (-want +got):\n%s", pages, diff)
	}
	expectAdmin(t, "a cursor that is none", f.do(http.MethodGet, "/pending-mappings?cursor=nope", "", as(f.s)), http.StatusBadRequest, codeInvalidRequest)

	catalog := catalogFixture(f)
	model := catalog.ready("acme/beta")
	body := `{"upstream_name":"oldest","model_id":"` + model.Id + `","reason":"it is the same model"}`
	var mapped adminapi.ModelMapping
	f.send(http.MethodPost, "/suppliers/"+supplier+"/model-mappings", body, http.StatusCreated, &mapped)
	if mapped.SupplierId != supplier || mapped.ModelId != model.Id || mapped.UpstreamName != "oldest" || mapped.GroupId != nil {
		t.Errorf("the alias: %+v", mapped)
	}
	if names, _ := f.pendingNames("supplier_id=" + supplier); len(names) != 2 || names[0] != "middle" {
		t.Errorf("the queue after the alias: %v", names)
	}
	expectAdmin(t, "the same name again", f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/model-mappings", body, newKey()), http.StatusConflict, codeConflict)
	missing := `{"upstream_name":"middle","model_id":"` + uuid.NewV7().String() + `","reason":"r"}`
	expectAdmin(t, "a model the catalog lacks", f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/model-mappings", missing, newKey()),
		http.StatusUnprocessableEntity, codeUnprocessable)
	foreign := `{"group_id":"` + uuid.NewV7().String() + `","upstream_name":"middle","model_id":"` + model.Id + `","reason":"r"}`
	expectAdmin(t, "a group the supplier lacks", f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/model-mappings", foreign, newKey()),
		http.StatusUnprocessableEntity, codeUnprocessable)
	f.expectAudit("supplier_alias.create", map[string]string{"reason": "it is the same model"})
}

// TestMappingRetryAfterTheModelRetires: a mapping that went through is answered again to its retry, though its model has
// been retired since; a new request for the retired model is still refused.
func TestMappingRetryAfterTheModelRetires(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	catalog := catalogFixture(f)
	model := catalog.ready("acme/gamma")
	body := `{"upstream_name":"gamma","model_id":"` + model.Id + `","reason":"r"}`
	key := newKey()
	expectAdmin(t, "the mapping", f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/model-mappings", body, key), http.StatusCreated, "")
	f.run(fmt.Sprintf("UPDATE catalog.model SET status = 'retired' WHERE id = '%s'", model.Id))
	rec := f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/model-mappings", body, key)
	if expectAdmin(t, "the retry", rec, http.StatusCreated, ""); rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("the retry was not a replay")
	}
	expectAdmin(t, "a new request", f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/model-mappings", strings.Replace(body, "gamma", "delta", 1), newKey()),
		http.StatusUnprocessableEntity, codeUnprocessable)
}
