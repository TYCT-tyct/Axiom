package main

import (
	"fmt"
	"net/http"
	"testing"

	"axiom/internal/platform/adminapi"
)

func TestGroupMappingRevisionsAndIdempotentReplay(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	group := f.group(supplier, "g")
	cat := catalogFixture(f)
	one, two := cat.ready("acme/delta"), cat.ready("acme/epsilon")
	body := func(model, revision string) string {
		return fmt.Sprintf(`{"group_id":%q,"upstream_name":"alias","model_id":%q,"reason":"correct the group"%s}`, group, model, revision)
	}
	firstKey := newKey()
	for _, tc := range []struct {
		body, key string
		revision  int64
	}{
		{body(one.Id, ""), firstKey, 1},
		{body(two.Id, `,"rev":1`), newKey(), 2},
		{body(two.Id, `,"rev":2`), newKey(), 2},
		{body(one.Id, ""), firstKey, 1},
	} {
		rec := f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/model-mappings", tc.body, tc.key)
		expectAdmin(t, "group mapping revision", rec, http.StatusCreated, "")
		var mapping adminapi.ModelMapping
		decode(t, rec, &mapping)
		if mapping.Revision == nil || *mapping.Revision != tc.revision || mapping.GroupId == nil || *mapping.GroupId != group {
			t.Fatalf("mapping revision: %+v; want %d for group %s", mapping, tc.revision, group)
		}
	}
	for _, tc := range []struct {
		revision string
		code     errorCode
	}{
		{"", codeConflict},
		{`,"rev":1`, codeStaleRevision},
	} {
		rec := f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/model-mappings", body(one.Id, tc.revision), newKey())
		expectAdmin(t, "stale mapping submission", rec, http.StatusConflict, tc.code)
	}
	f.expectAudit("supplier_alias.revise", map[string]string{"reason": "correct the group"})
}
