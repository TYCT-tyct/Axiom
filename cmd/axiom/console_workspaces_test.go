package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/consoleapi"
)

// workspace makes a workspace named name and returns it.
func (f consoleFixture) workspace(s session, name string) consoleapi.Workspace {
	f.t.Helper()
	rec := f.do(http.MethodPost, "/workspaces", `{"name":"`+name+`"}`, as(s))
	expect(f.t, "createWorkspace "+name, rec, http.StatusCreated, "")
	var w consoleapi.Workspace
	decode(f.t, rec, &w)
	return w
}

func (f consoleFixture) listWorkspaces(s session) []consoleapi.Workspace {
	f.t.Helper()
	var list consoleapi.WorkspaceList
	rec := f.do(http.MethodGet, "/workspaces", "", as(s))
	expect(f.t, "listWorkspaces", rec, http.StatusOK, "")
	decode(f.t, rec, &list)
	return list.Data
}

// putWorkspace replaces a workspace's settings with body and returns it as changed.
func (f consoleFixture) putWorkspace(s session, id, body string) consoleapi.Workspace {
	f.t.Helper()
	rec := f.do(http.MethodPut, "/workspaces/"+id, body, as(s))
	expect(f.t, "updateWorkspace "+body, rec, http.StatusOK, "")
	var w consoleapi.Workspace
	decode(f.t, rec, &w)
	return w
}

// TestWorkspaces: an account starts with its default workspace alone; it makes, renames, disables and enables others,
// the default listed first; every change is audited once, in a key change's shape, and a change of nothing is not.
func TestWorkspaces(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	initial := f.listWorkspaces(s)
	if len(initial) != 1 || !initial[0].IsDefault || initial[0].Disabled || initial[0].Name != "Default" {
		t.Fatalf("a new account's workspaces: %+v", initial)
	}
	prod := f.workspace(s, "Prod")
	if prod.IsDefault || prod.Disabled || !strings.HasPrefix(prod.Id, "ws_") {
		t.Errorf("a new workspace: %+v", prod)
	}
	for _, step := range []struct {
		id, body string
		want     consoleapi.Workspace
	}{
		{prod.Id, `{"name":"Production","disabled":false}`, consoleapi.Workspace{Id: prod.Id, Name: "Production"}},
		{prod.Id, `{"name":"Production","disabled":false}`, consoleapi.Workspace{Id: prod.Id, Name: "Production"}},
		{prod.Id, `{"name":"Production","disabled":true}`, consoleapi.Workspace{Id: prod.Id, Name: "Production", Disabled: true}},
		{prod.Id, `{"name":"Production","disabled":false}`, consoleapi.Workspace{Id: prod.Id, Name: "Production"}},
		{initial[0].Id, `{"name":"Main","disabled":false}`, consoleapi.Workspace{Id: initial[0].Id, Name: "Main", IsDefault: true}},
	} {
		if diff := cmp.Diff(step.want, f.putWorkspace(s, step.id, step.body)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", step.body, diff)
		}
	}
	var read consoleapi.Workspace
	decode(t, f.do(http.MethodGet, "/workspaces/"+prod.Id, "", as(s)), &read)
	if diff := cmp.Diff(consoleapi.Workspace{Id: prod.Id, Name: "Production"}, read); diff != "" {
		t.Errorf("read back (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"Main", "Production"}, workspaceNames(f.listWorkspaces(s))); diff != "" {
		t.Errorf("the list, default first (-want +got):\n%s", diff)
	}
	audited := f.text(`SELECT string_agg(action || ' ' || detail::text, '; ' ORDER BY id) FROM platform.audit_log
		WHERE module = 'accounts' AND target_kind = 'workspace'`)
	want := strings.Join([]string{
		`workspace.create {"name": "Prod"}`,
		`workspace.update {"after": {"name": "Production"}, "before": {"name": "Prod"}, "changed": ["name"]}`,
		`workspace.update {"after": {"disabled": true}, "before": {"disabled": false}, "changed": ["disabled"]}`,
		`workspace.update {"after": {"disabled": false}, "before": {"disabled": true}, "changed": ["disabled"]}`,
		`workspace.update {"after": {"name": "Main"}, "before": {"name": "Default"}, "changed": ["name"]}`,
	}, "; ")
	if audited != want {
		t.Errorf("audited\n%s\nwant\n%s", audited, want)
	}
}

// TestWorkspaceRefusals: a name taken ignoring case, an empty one, disabling the default, another account's workspace
// and a 51st workspace are refused, and none of them is audited.
func TestWorkspaceRefusals(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	main, prod := f.listWorkspaces(s)[0].Id, f.workspace(s, "Prod").Id
	theirs := f.listWorkspaces(f.registered(s, "other@example.com"))[0].Id
	account, site := f.account(s)
	for _, r := range []struct {
		what, method, path, body string
		status                   int
		code                     consoleapi.ErrorCode
	}{
		{"a name taken, ignoring case", http.MethodPost, "/workspaces", `{"name":"prod"}`, http.StatusConflict, consoleapi.ErrorCodeNameTaken},
		{"an empty name", http.MethodPost, "/workspaces", `{"name":""}`, http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest},
		{"renaming to the default's name", http.MethodPut, "/workspaces/" + prod, `{"name":"DEFAULT","disabled":false}`, http.StatusConflict, consoleapi.ErrorCodeNameTaken},
		{"disabling the default", http.MethodPut, "/workspaces/" + main, `{"name":"Default","disabled":true}`, http.StatusConflict, consoleapi.ErrorCodeConflict},
		{"reading another account's", http.MethodGet, "/workspaces/" + theirs, "", http.StatusNotFound, consoleapi.ErrorCodeNotFound},
		{"changing another account's", http.MethodPut, "/workspaces/" + theirs, `{"name":"Mine","disabled":true}`, http.StatusNotFound, consoleapi.ErrorCodeNotFound},
		{"reading one that is none", http.MethodGet, "/workspaces/ws_00000000000000000000000000", "", http.StatusNotFound, consoleapi.ErrorCodeNotFound},
	} {
		expect(t, r.what, f.do(r.method, r.path, r.body, as(s)), r.status, r.code)
	}
	f.run(fmt.Sprintf(`INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default)
		SELECT gen_random_uuid(), '%s', '%s', 'Filler ' || n, 'disabled', false FROM generate_series(1, 48) AS n`, site, account))
	expect(t, "the 51st workspace", f.do(http.MethodPost, "/workspaces", `{"name":"One too many"}`, as(s)), http.StatusConflict, consoleapi.ErrorCodeConflict)
	if got := f.text(`SELECT count(*)::text FROM platform.audit_log WHERE target_kind = 'workspace'`); got != "1" {
		t.Errorf("%s workspace audit entries, want only the one made", got)
	}
}

func workspaceNames(list []consoleapi.Workspace) []string {
	names := make([]string, len(list))
	for i, w := range list {
		names[i] = w.Name
	}
	return names
}

// TestWorkspaceKeys: a key goes to the workspace named, or the default one; the list keeps one workspace's keys and
// says so; a disabled workspace takes no new key, keeps its keys listed, and its keys' row says disabled for admission;
// another account's workspace is a 400.
func TestWorkspaceKeys(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	main := f.listWorkspaces(s)[0].Id
	prod := f.workspace(s, "Prod").Id
	plain := f.createKey(s, `{"name":"plain"}`).Key
	inProd := f.createKey(s, `{"name":"in prod","workspace_id":"`+prod+`"}`).Key
	if plain.WorkspaceId != main || inProd.WorkspaceId != prod {
		t.Fatalf("keys made in %s and %s, want %s and %s", plain.WorkspaceId, inProd.WorkspaceId, main, prod)
	}
	var list consoleapi.KeyList
	decode(t, f.do(http.MethodGet, "/keys?workspace_id="+prod, "", as(s)), &list)
	if diff := cmp.Diff([]string{inProd.Id}, idsOf(list)); diff != "" || list.Filters == nil || list.Filters.WorkspaceId == nil || *list.Filters.WorkspaceId != prod {
		t.Errorf("the workspace's keys (-want +got):\n%s\nfilters %+v", diff, list.Filters)
	}
	decode(t, f.do(http.MethodGet, "/keys", "", as(s)), &list)
	if len(list.Data) != 2 {
		t.Errorf("every workspace's keys: %d", len(list.Data))
	}

	expect(t, "disabling", f.do(http.MethodPut, "/workspaces/"+prod, `{"name":"Prod","disabled":true}`, as(s)), http.StatusOK, "")
	expect(t, "a key in a disabled workspace", f.do(http.MethodPost, "/keys", `{"name":"x","workspace_id":"`+prod+`"}`, as(s)),
		http.StatusConflict, consoleapi.ErrorCodeConflict)
	decode(t, f.do(http.MethodGet, "/keys?workspace_id="+prod, "", as(s)), &list)
	if diff := cmp.Diff([]string{inProd.Id}, idsOf(list)); diff != "" {
		t.Errorf("a disabled workspace's keys stay listed (-want +got):\n%s", diff)
	}
	if got := f.text(`SELECT w.status FROM accounts.api_keys k JOIN accounts.workspaces w ON w.id = k.workspace_id
		WHERE k.last4 = '` + inProd.Last4 + `' AND k.name = 'in prod'`); got != "disabled" {
		t.Errorf("the state admission reads for the key's workspace: %q", got)
	}
	expect(t, "enabling", f.do(http.MethodPut, "/workspaces/"+prod, `{"name":"Prod","disabled":false}`, as(s)), http.StatusOK, "")
	f.createKey(s, `{"name":"again","workspace_id":"`+prod+`"}`)

	theirs := f.listWorkspaces(f.registered(s, "other@example.com"))[0].Id
	expect(t, "another account's workspace", f.do(http.MethodPost, "/keys", `{"name":"x","workspace_id":"`+theirs+`"}`, as(s)),
		http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	decode(t, f.do(http.MethodGet, "/keys?workspace_id="+theirs, "", as(s)), &list)
	if len(list.Data) != 0 {
		t.Errorf("another account's workspace lists %d keys", len(list.Data))
	}
}

// TestWorkspacePermissions: a member, who sees and makes keys, lists workspaces and makes keys in them, but cannot make,
// rename or disable a workspace, which needs workspaces.manage.
func TestWorkspacePermissions(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	prod := f.workspace(s, "Prod").Id
	f.run(`UPDATE accounts.members SET role_id = (SELECT id FROM accounts.roles WHERE name = 'member')`)
	if got := len(f.listWorkspaces(s)); got != 2 {
		t.Errorf("a member lists %d workspaces", got)
	}
	expect(t, "a member reads one", f.do(http.MethodGet, "/workspaces/"+prod, "", as(s)), http.StatusOK, "")
	f.createKey(s, `{"name":"member's","workspace_id":"`+prod+`"}`)
	expect(t, "a member makes one", f.do(http.MethodPost, "/workspaces", `{"name":"Mine"}`, as(s)), http.StatusForbidden, consoleapi.ErrorCodeForbidden)
	expect(t, "a member disables one", f.do(http.MethodPut, "/workspaces/"+prod, `{"name":"Prod","disabled":true}`, as(s)),
		http.StatusForbidden, consoleapi.ErrorCodeForbidden)
}
