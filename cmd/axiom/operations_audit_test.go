package main

import (
	"net/http"
	"testing"

	"axiom/internal/platform/operationsapi"
)

// TestOperationsAuditReads: a credential holding audit.view with the platform grant reads the platform's audit as the
// admin's search does, filtered exactly and one entry by its ID; without the grant the audit is out of its scope, and
// without the permission it is forbidden.
func TestOperationsAuditReads(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	id, token := f.makeCredential(owner, `{"name":"hermes","permissions":["audit.view"],"platform":true,
		"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"start the agent"}`)
	_, sites := f.makeCredential(owner, `{"name":"sites","permissions":["audit.view"],"platform":false,"sites":["`+f.siteID()+`"],
		"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)
	_, blind := f.makeCredential(owner, `{"name":"blind","permissions":[],"platform":true,
		"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)

	rec := f.agent(token, because("/audit?action=operations_credential.create&target_id="+id, "who made me"))
	expectAdmin(t, "auditList", rec, http.StatusOK, "")
	var list operationsapi.AuditEntryList
	decode(t, rec, &list)
	if list.Data == nil || len(*list.Data) != 1 || list.Since == nil || list.Until == nil {
		t.Fatalf("the filtered audit is %s", rec.Body)
	}
	made := (*list.Data)[0]
	if deref(made.TargetId) != id || deref(made.Reason) != "start the agent" || deref(made.ActorKind) != "user" {
		t.Errorf("the credential's creation is listed as %s", rec.Body)
	}

	rec = f.agent(token, because("/audit/"+deref(made.Id), "read it"))
	expectAdmin(t, "auditGet", rec, http.StatusOK, "")
	var entry operationsapi.AuditEntry
	decode(t, rec, &entry)
	if deref(entry.Id) != deref(made.Id) || deref(entry.Action) != "operations_credential.create" || entry.Detail == nil {
		t.Errorf("the entry is %s", rec.Body)
	}

	for _, c := range []struct {
		what, token, path string
		status            int
		code              errorCode
	}{
		{"an entry that does not exist", token, "/audit/0198c0a8-0000-7000-8000-000000000003", http.StatusNotFound, codeNotFound},
		{"an entry ID that is not one", token, "/audit/nope", http.StatusNotFound, codeNotFound},
		{"a window longer than 31 days", token, "/audit?since=2026-01-01T00:00:00Z&until=2026-03-01T00:00:00Z", http.StatusBadRequest, codeInvalidRequest},
		{"the audit without the platform grant", sites, "/audit", http.StatusNotFound, codeNotFound},
		{"an entry without the platform grant", sites, "/audit/" + deref(made.Id), http.StatusNotFound, codeNotFound},
		{"the audit without audit.view", blind, "/audit", http.StatusForbidden, codeForbidden},
	} {
		expectAdmin(t, c.what, f.agent(c.token, because(c.path, "x")), c.status, c.code)
	}
}
