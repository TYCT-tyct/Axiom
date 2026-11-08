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

// TestOperationsAuditDigests: the owner's daily digests list newest first, a page at a time by the cursor the last page
// gave, under the same grant as the audit; a cursor that is not a day is a 400.
func TestOperationsAuditDigests(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	_, token := f.makeCredential(owner, `{"name":"hermes","permissions":["audit.view"],"platform":true,
		"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)
	_, sites := f.makeCredential(owner, `{"name":"sites","permissions":["audit.view"],"platform":false,"sites":["`+f.siteID()+`"],
		"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)
	f.run(`INSERT INTO operations.audit_digests (day, body) SELECT d, jsonb_build_object('day', d::date::text,
		'made_at', '2026-11-08T00:01:00Z', 'operations', '[]'::jsonb, 'money', jsonb_build_array(jsonb_build_object(
		'scope', 'platform', 'category', 'money.top_up', 'currency', 'USD', 'reserved', '30', 'committed', '20', 'count', 2,
		'daily_max', '100', 'hard_daily_max', '200', 'daily_count_max', 5)), 'approvals', '[]'::jsonb,
		'refused', jsonb_build_array(jsonb_build_object('code', 'forbidden', 'count', 2)),
		'chain', jsonb_build_object('head_seq', 7, 'faults', 0))
		FROM generate_series(date '2026-11-05', date '2026-11-07', interval '1 day') d`)

	rec := f.agent(token, because("/audit/digests?limit=2", "read the digests"))
	expectAdmin(t, "auditDigestsList", rec, http.StatusOK, "")
	var page operationsapi.AuditDigestList
	decode(t, rec, &page)
	if len(page.Data) != 2 || page.Data[0].Day.String() != "2026-11-07" || page.NextCursor == nil || *page.NextCursor != "2026-11-06" ||
		page.Data[0].Money[0].Reserved != "30" || page.Data[0].Chain.HeadSeq != 7 {
		t.Fatalf("the first page is %s", rec.Body)
	}
	rec = f.agent(token, because("/audit/digests?limit=2&cursor="+*page.NextCursor, "read on"))
	expectAdmin(t, "auditDigestsList", rec, http.StatusOK, "")
	var last operationsapi.AuditDigestList
	decode(t, rec, &last)
	if len(last.Data) != 1 || last.Data[0].Day.String() != "2026-11-05" || last.NextCursor != nil {
		t.Errorf("the last page is %s", rec.Body)
	}
	expectAdmin(t, "a cursor that is not a day", f.agent(token, because("/audit/digests?cursor=nope", "x")), http.StatusBadRequest, codeInvalidRequest)
	expectAdmin(t, "the digests without the platform grant", f.agent(sites, because("/audit/digests", "x")), http.StatusNotFound, codeNotFound)
}
