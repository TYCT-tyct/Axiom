package main

import (
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/adminapi"
)

func (f adminFixture) accountWorkspaceLimit(s session, account string) *adminapi.WorkspaceLimitView {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/accounts/"+account, "", as(s))
	expectAdmin(f.t, "getAccount", rec, http.StatusOK, "")
	var detail adminapi.AccountDetail
	decode(f.t, rec, &detail)
	return detail.WorkspaceLimit
}

// TestWorkspaceLimits: a site's limit on the workspaces of an account, and an account's own over it, are read and
// replaced through their own operations, each change audited with its reason and the value it replaced; the key
// limits' operation, which replaces those whole, leaves them as they are; out of bounds and without a reason is a 400
// that changes nothing.
func TestWorkspaceLimits(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	site, account := f.siteID(), f.accountOf(ownerEmail)
	siteLimit := func() adminapi.WorkspaceLimitView {
		t.Helper()
		rec := f.do(http.MethodGet, "/sites/"+site+"/workspace-limit", "", as(s))
		expectAdmin(t, "getSiteWorkspaceLimit", rec, http.StatusOK, "")
		var view adminapi.WorkspaceLimitView
		decode(t, rec, &view)
		return view
	}
	if diff := cmp.Diff(adminapi.WorkspaceLimitView{Effective: 50}, siteLimit()); diff != "" {
		t.Errorf("a site that sets none (-want +got):\n%s", diff)
	}
	expectAdmin(t, "updateSiteWorkspaceLimit", f.change(s, http.MethodPut, "/sites/"+site+"/workspace-limit",
		`{"max_workspaces":10,"reason":"tighten"}`, newKey()), http.StatusOK, "")
	expectAdmin(t, "the key limits replaced whole", f.change(s, http.MethodPut, "/sites/"+site+"/key-limits",
		`{"limits":{},"reason":"older page"}`, newKey()), http.StatusOK, "")
	if diff := cmp.Diff(adminapi.WorkspaceLimitView{MaxWorkspaces: new(10), Effective: 10}, siteLimit()); diff != "" {
		t.Errorf("the site's limit after the change (-want +got):\n%s", diff)
	}
	f.expectAudit("site_workspace_limit.update", map[string]string{"reason": "tighten", "detail->>'after'": "10", "coalesce(detail->>'before', 'none')": "none"})

	if diff := cmp.Diff(&adminapi.WorkspaceLimitView{Effective: 10}, f.accountWorkspaceLimit(s, account)); diff != "" {
		t.Errorf("an account that sets none (-want +got):\n%s", diff)
	}
	expectAdmin(t, "updateAccountWorkspaceLimit", f.change(s, http.MethodPut, "/accounts/"+account+"/workspace-limit",
		`{"max_workspaces":200,"reason":"a big customer"}`, newKey()), http.StatusOK, "")
	if diff := cmp.Diff(&adminapi.WorkspaceLimitView{MaxWorkspaces: new(200), Effective: 200}, f.accountWorkspaceLimit(s, account)); diff != "" {
		t.Errorf("an account with a limit of its own (-want +got):\n%s", diff)
	}
	f.expectAudit("account_workspace_limit.update", map[string]string{"reason": "a big customer", "detail->>'after'": "200", "target_id": account})
	expectAdmin(t, "clearing the account's limit", f.change(s, http.MethodPut, "/accounts/"+account+"/workspace-limit", `{"reason":"back"}`, newKey()), http.StatusOK, "")
	if diff := cmp.Diff(&adminapi.WorkspaceLimitView{Effective: 10}, f.accountWorkspaceLimit(s, account)); diff != "" {
		t.Errorf("an account after its limit was cleared (-want +got):\n%s", diff)
	}

	for _, path := range []string{"/sites/" + site + "/workspace-limit", "/accounts/" + account + "/workspace-limit"} {
		for _, body := range []string{`{"max_workspaces":0,"reason":"r"}`, `{"max_workspaces":1001,"reason":"r"}`, `{"max_workspaces":5}`} {
			expectAdmin(t, body+" at "+path, f.change(s, http.MethodPut, path, body, newKey()), http.StatusBadRequest, codeInvalidRequest)
		}
	}
	if f.auditCount("site_workspace_limit.update") != "1" || f.auditCount("account_workspace_limit.update") != "2" {
		t.Error("a refused change was audited")
	}
}
