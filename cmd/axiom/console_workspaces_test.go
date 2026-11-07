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

// TestWorkspaces: an account starts with its default workspace alone; it makes, renames, disables and enables others,
// with names unique ignoring case, the default never disabled, at most 50, another account's unseen, and every change
// audited once while refusals and no-op changes are not.
func TestWorkspaces(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	initial := f.listWorkspaces(s)
	if len(initial) != 1 || !initial[0].IsDefault || initial[0].Disabled || initial[0].Name != "Default" {
		t.Fatalf("a new account's workspaces: %+v", initial)
	}
	main := initial[0]
	prod := f.workspace(s, "Prod")
	if prod.IsDefault || prod.Disabled || !strings.HasPrefix(prod.Id, "ws_") {
		t.Errorf("a new workspace: %+v", prod)
	}
	expect(t, "a name taken, ignoring case", f.do(http.MethodPost, "/workspaces", `{"name":"prod"}`, as(s)), http.StatusConflict, consoleapi.ErrorCodeNameTaken)
	expect(t, "an empty name", f.do(http.MethodPost, "/workspaces", `{"name":""}`, as(s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)

	put := func(id, body string) consoleapi.Workspace {
		t.Helper()
		rec := f.do(http.MethodPut, "/workspaces/"+id, body, as(s))
		expect(t, "updateWorkspace "+body, rec, http.StatusOK, "")
		var w consoleapi.Workspace
		decode(t, rec, &w)
		return w
	}
	if got := put(prod.Id, `{"name":"Production","disabled":false}`); got.Name != "Production" || got.Disabled {
		t.Errorf("renamed: %+v", got)
	}
	put(prod.Id, `{"name":"Production","disabled":false}`) // changes nothing, so it is not audited
	if got := put(prod.Id, `{"name":"Production","disabled":true}`); !got.Disabled {
		t.Errorf("disabled: %+v", got)
	}
	if got := put(prod.Id, `{"name":"Production","disabled":false}`); got.Disabled {
		t.Errorf("enabled again: %+v", got)
	}
	expect(t, "renaming to the default's name", f.do(http.MethodPut, "/workspaces/"+prod.Id, `{"name":"DEFAULT","disabled":false}`, as(s)),
		http.StatusConflict, consoleapi.ErrorCodeNameTaken)
	expect(t, "disabling the default", f.do(http.MethodPut, "/workspaces/"+main.Id, `{"name":"Default","disabled":true}`, as(s)),
		http.StatusConflict, consoleapi.ErrorCodeConflict)
	if got := put(main.Id, `{"name":"Main","disabled":false}`); got.Name != "Main" || !got.IsDefault {
		t.Errorf("the default renamed: %+v", got)
	}

	var read consoleapi.Workspace
	decode(t, f.do(http.MethodGet, "/workspaces/"+prod.Id, "", as(s)), &read)
	if read.Name != "Production" || read.Disabled {
		t.Errorf("read back: %+v", read)
	}
	if diff := cmp.Diff([]string{"Main", "Production"}, workspaceNames(f.listWorkspaces(s))); diff != "" {
		t.Errorf("the list, default first (-want +got):\n%s", diff)
	}

	other := f.registered(s, "other@example.com")
	theirs := f.listWorkspaces(other)[0].Id
	f.assertNotFound(s, "/workspaces/"+theirs)
	expect(t, "changing another account's", f.do(http.MethodPut, "/workspaces/"+theirs, `{"name":"Mine","disabled":true}`, as(s)),
		http.StatusNotFound, consoleapi.ErrorCodeNotFound)
	f.assertNotFound(s, "/workspaces/ws_00000000000000000000000000")

	account, site := f.account(s)
	f.run(fmt.Sprintf(`INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default)
		SELECT gen_random_uuid(), '%s', '%s', 'Filler ' || n, 'disabled', false FROM generate_series(1, 48) AS n`, site, account))
	expect(t, "the 51st workspace", f.do(http.MethodPost, "/workspaces", `{"name":"One too many"}`, as(s)), http.StatusConflict, consoleapi.ErrorCodeConflict)

	audited := f.text(`SELECT string_agg(action || ' ' || detail::text, '; ' ORDER BY id) FROM platform.audit_log
		WHERE module = 'accounts' AND target_kind = 'workspace'`)
	for _, want := range []string{
		`workspace.create {"name": "Prod"}`,
		`workspace.update {"after": {"name": "Production"}, "before": {"name": "Prod"}, "changed": ["name"]}`,
		`workspace.update {"after": {"disabled": true}, "before": {"disabled": false}, "changed": ["disabled"]}`,
		`workspace.update {"after": {"disabled": false}, "before": {"disabled": true}, "changed": ["disabled"]}`,
		`workspace.update {"after": {"name": "Main"}, "before": {"name": "Default"}, "changed": ["name"]}`,
	} {
		if !strings.Contains(audited, want) {
			t.Errorf("audit %q lacks %q", audited, want)
		}
	}
	if n := strings.Count(audited, "workspace."); n != 5 {
		t.Errorf("%d audit entries, want 5: %s", n, audited)
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
