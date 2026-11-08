package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/operationsapi"
)

// mapAs asks, as the agent, to map name in the group to model under key.
func (f supplierFixture) mapAs(token, supplier, group, name, model, key, extra string) *httptest.ResponseRecorder {
	f.t.Helper()
	body := `{"upstream_name":"` + name + `","model_id":"` + model + `","reason":"its answers match the model"` + extra + `}`
	return f.agentSend(token, http.MethodPost, "/suppliers/"+supplier+"/groups/"+group+"/model-mappings", body, idempotent(key))
}

// listed makes the group list name with no model, as the sync does with a name the catalog could not recognise.
func (f supplierFixture) listed(supplier, group, name string) {
	f.t.Helper()
	f.run(fmt.Sprintf(`INSERT INTO suppliers.supplier_models (supplier_id, supplier_group_id, upstream_name, formats)
		VALUES ('%s', '%s', '%s', '{chat}')`, supplier, group, name))
}

// TestOperationsSupplierGroupSelect: the agent selects a group at once, as its own, and the group gets its code name; a
// retry under the key finds the operation. Unselecting it keeps the name.
func TestOperationsSupplierGroupSelect(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	group := f.group(supplier, "1")
	token := f.moneyAgent(false)
	selectAs := func(selected bool, key string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"selected":%t,"reason":"its prices are the lowest"}`, selected)
		return f.agentSend(token, http.MethodPut, "/suppliers/"+supplier+"/groups/"+group, body, idempotent(key))
	}

	key := newKey()
	rec := selectAs(true, key)
	chosen := agentOperation(t, rec)
	codename := strings.TrimSpace(f.text(`SELECT coalesce(codename, '') FROM suppliers.supplier_groups WHERE id = '` + group + `'`))
	if rec.Code != http.StatusOK || chosen.State != operationsapi.OperationStateSucceeded || codename == "" ||
		!strings.Contains(rec.Body.String(), `"selected":true,`) || !strings.Contains(rec.Body.String(), `"codename":"`+codename+`"`) {
		t.Fatalf("selecting the group: %d %s", rec.Code, rec.Body)
	}
	rec = selectAs(true, key)
	if again := agentOperation(t, rec); rec.Header().Get("Idempotency-Replayed") != "true" || *again.OperationId != *chosen.OperationId {
		t.Errorf("the retry: %d %s", rec.Code, rec.Body)
	}
	f.expectAudit("supplier_group.select", map[string]string{"actor_kind": "operations_agent", "reason": "its prices are the lowest"})

	rec = selectAs(false, newKey())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"selected":false,`) || !strings.Contains(rec.Body.String(), `"codename":"`+codename+`"`) {
		t.Errorf("unselecting the group: %d %s", rec.Code, rec.Body)
	}
}

// TestOperationsSupplierModelMap: the agent maps a name the group lists as waiting at once, as its own, and the name
// leaves the queue.
func TestOperationsSupplierModelMap(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	group := f.group(supplier, "1")
	f.listed(supplier, group, "beta-latest")
	model := catalogFixture(f).ready("acme/beta")
	rec := f.mapAs(f.moneyAgent(false), supplier, group, "beta-latest", model.Id, newKey(), "")
	mapped := agentOperation(t, rec)
	if rec.Code != http.StatusOK || mapped.State != operationsapi.OperationStateSucceeded || mapped.Target.Kind != "supplier_group" ||
		!strings.Contains(rec.Body.String(), `"model_id":"`+model.Id+`"`) || !strings.Contains(rec.Body.String(), `"revision":1`) {
		t.Fatalf("mapping the name: %d %s", rec.Code, rec.Body)
	}
	if names, _ := f.pendingNames("supplier_id=" + supplier); slices.Contains(names, "beta-latest") {
		t.Errorf("the queue still lists the mapped name: %v", names)
	}
	f.expectAudit("supplier_alias.create", map[string]string{
		"actor_kind": "operations_agent", "detail->>'group'": group, "detail->>'model'": model.Id, "reason": "its answers match the model",
	})
}

// TestOperationsSupplierMappingRefusals: a preview, a name that does not wait (not listed, or covered by the owner's
// alias for the whole supplier), a model the catalog lacks or has retired, a group the supplier lacks, a credential
// without suppliers.manage and one without the platform grant leave no operation and no alias, and the name still
// waits.
func TestOperationsSupplierMappingRefusals(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	group := f.group(supplier, "1")
	f.listed(supplier, group, "waiting")
	f.listed(supplier, group, "covered")
	catalog := catalogFixture(f)
	model, retired := catalog.ready("acme/beta"), catalog.ready("acme/gamma")
	f.run(fmt.Sprintf("UPDATE catalog.model SET status = 'retired' WHERE id = '%s'", retired.Id))
	owners := `{"upstream_name":"covered","model_id":"` + model.Id + `","reason":"the whole supplier serves it"}`
	expectAdmin(t, "the owner's alias", f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/model-mappings", owners, newKey()), http.StatusCreated, "")

	token := f.moneyAgent(false)
	rec := f.mapAs(token, supplier, group, "waiting", model.Id, newKey(), `,"dry_run":true`)
	if preview := agentOperation(t, rec); rec.Code != http.StatusOK || !preview.DryRun || preview.State != operationsapi.OperationStateApproved {
		t.Errorf("the preview: %d %s", rec.Code, rec.Body)
	}
	_, reader := f.makeCredential(f.s, `{"name":"reader","permissions":["suppliers.view"],"platform":true,
		"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"patrol only"}`)
	_, sited := f.makeCredential(f.s, `{"name":"sited","permissions":["suppliers.view","suppliers.manage"],
		"platform":false,"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"one site"}`)
	for _, c := range []struct {
		name, token, group, upstream, model string
		status                              int
		code                                errorCode
	}{
		{"a name the group does not list", token, group, "unlisted", model.Id, http.StatusConflict, codeConflict},
		{"a name the owner's supplier alias covers", token, group, "covered", model.Id, http.StatusConflict, codeConflict},
		{"a model the catalog lacks", token, group, "waiting", uuid.NewV7().String(), http.StatusUnprocessableEntity, codeUnprocessable},
		{"a retired model", token, group, "waiting", retired.Id, http.StatusUnprocessableEntity, codeUnprocessable},
		{"a group the supplier lacks", token, uuid.NewV7().String(), "waiting", model.Id, http.StatusNotFound, codeNotFound},
		{"a credential without suppliers.manage", reader, group, "waiting", model.Id, http.StatusForbidden, codeForbidden},
		{"a credential without the platform grant", sited, group, "waiting", model.Id, http.StatusNotFound, codeNotFound},
	} {
		expectAdmin(t, c.name, f.mapAs(c.token, supplier, c.group, c.upstream, c.model, newKey(), ""), c.status, c.code)
	}
	if got := f.text(`SELECT count(*)::text FROM operations.operations`); got != "0" {
		t.Errorf("%s operations after the refusals and the preview", got)
	}
	if names, _ := f.pendingNames("supplier_id=" + supplier); !slices.Equal(names, []string{"waiting"}) {
		t.Errorf("the queue after the refusals: %v", names)
	}
	if got := f.text(`SELECT string_agg(actor_kind, ',') FROM platform.audit_log WHERE action = 'supplier_alias.create'`); got != "user" {
		t.Errorf("the aliases were made by %s", got)
	}
}
