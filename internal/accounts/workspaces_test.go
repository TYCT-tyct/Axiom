package accounts_test

import (
	"errors"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// workspaceRig is a1 of the seeded database with a node on it, where the console's calls run in a1's scope.
type workspaceRig struct {
	t  *testing.T
	db *pg.DB
	s  *accounts.Service
}

func newWorkspaceRig(t *testing.T) workspaceRig {
	t.Helper()
	db, _ := open(t)
	return workspaceRig{t, db, node(t, db, hashKeys(t, 1), nil)}
}

func (r workspaceRig) inA1(fn func(tx pg.Tx) error) error {
	return r.db.Tx(r.t.Context(), pg.NewScope(false, nil, []uuid.UUID{a1}), fn)
}

func (r workspaceRig) create(name string) (accounts.WorkspaceInfo, error) {
	var w accounts.WorkspaceInfo
	err := r.inA1(func(tx pg.Tx) (err error) {
		w, err = r.s.CreateWorkspace(r.t.Context(), tx, system, accounts.SiteID(s1), accounts.AccountID(a1), name)
		return err
	})
	return w, err
}

func (r workspaceRig) update(id accounts.WorkspaceID, name string, disabled bool) (accounts.WorkspaceInfo, error) {
	var w accounts.WorkspaceInfo
	err := r.inA1(func(tx pg.Tx) (err error) {
		w, err = r.s.UpdateWorkspace(r.t.Context(), tx, system, accounts.AccountID(a1), id, name, disabled)
		return err
	})
	return w, err
}

func (r workspaceRig) list() []accounts.WorkspaceInfo {
	r.t.Helper()
	var list []accounts.WorkspaceInfo
	if err := r.inA1(func(tx pg.Tx) (err error) {
		list, err = r.s.ListWorkspaces(r.t.Context(), tx, accounts.AccountID(a1))
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
	return list
}

// inPlatform runs fn in the platform scope, as the admin API does.
func (r workspaceRig) inPlatform(fn func(tx pg.Tx) error) error {
	return r.db.Tx(r.t.Context(), platform, fn)
}

// setLimits sets s1's and a1's own workspace limits, nil for none.
func (r workspaceRig) setLimits(site, account *int32) error {
	return r.inPlatform(func(tx pg.Tx) error {
		if err := r.s.SetSiteWorkspaceLimit(r.t.Context(), tx, system, accounts.SiteID(s1), site, "r"); err != nil {
			return err
		}
		return r.s.SetAccountWorkspaceLimit(r.t.Context(), tx, system, accounts.AccountID(a1), account, "r")
	})
}

func (r workspaceRig) limits() (site, account accounts.WorkspaceLimit) {
	r.t.Helper()
	if err := r.inPlatform(func(tx pg.Tx) (err error) {
		if site, err = r.s.SiteWorkspaceLimit(r.t.Context(), tx, accounts.SiteID(s1)); err != nil {
			return err
		}
		account, err = r.s.AccountWorkspaceLimit(r.t.Context(), tx, accounts.AccountID(a1))
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
	return site, account
}

// TestWorkspaceUpdates: a workspace is made, renamed, changed to nothing new, disabled; a name taken ignoring case,
// disabling the default and another account's workspace are refused.
func TestWorkspaceUpdates(t *testing.T) {
	t.Parallel()
	r := newWorkspaceRig(t)
	prod, err := r.create("Prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.create("PROD"); !errors.Is(err, accounts.ErrWorkspaceNameTaken) {
		t.Errorf("a name taken: %v", err)
	}
	for _, c := range []struct {
		id       accounts.WorkspaceID
		name     string
		disabled bool
		want     accounts.WorkspaceInfo
		err      error
	}{
		{prod.ID, "Production", false, accounts.WorkspaceInfo{ID: prod.ID, Name: "Production"}, nil},
		{prod.ID, "Production", false, accounts.WorkspaceInfo{ID: prod.ID, Name: "Production"}, nil},
		{prod.ID, "Production", true, accounts.WorkspaceInfo{ID: prod.ID, Name: "Production", Disabled: true}, nil},
		{prod.ID, "default", true, accounts.WorkspaceInfo{}, accounts.ErrWorkspaceNameTaken},
		{accounts.WorkspaceID(w1), "Default", true, accounts.WorkspaceInfo{}, accounts.ErrDefaultWorkspace},
		{accounts.WorkspaceID(w2), "Theirs", false, accounts.WorkspaceInfo{}, accounts.ErrNotFound},
	} {
		got, err := r.update(c.id, c.name, c.disabled)
		if !errors.Is(err, c.err) {
			t.Errorf("%s disabled=%v: %v, want %v", c.name, c.disabled, err, c.err)
		}
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s disabled=%v (-want +got):\n%s", c.name, c.disabled, diff)
		}
	}
	want := []accounts.WorkspaceInfo{{ID: accounts.WorkspaceID(w1), Name: "Default", Default: true}, {ID: prod.ID, Name: "Production", Disabled: true}}
	if diff := cmp.Diff(want, r.list()); diff != "" {
		t.Errorf("the list, default first (-want +got):\n%s", diff)
	}
}

// TestWorkspaceOfKeys: a workspace is resolved by ID or as the default, another account's is not found, and a key is
// refused in a disabled, an unknown or the zero workspace rather than made in the default one.
func TestWorkspaceOfKeys(t *testing.T) {
	t.Parallel()
	r := newWorkspaceRig(t)
	prod, err := r.create("Prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.update(prod.ID, "Prod", true); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id   *accounts.WorkspaceID
		want accounts.WorkspaceID
		err  error
	}{
		{&prod.ID, prod.ID, nil},
		{nil, accounts.WorkspaceID(w1), nil},
		{new(accounts.WorkspaceID(w2)), accounts.WorkspaceID{}, accounts.ErrNotFound},
	} {
		var got accounts.WorkspaceID
		err := r.inA1(func(tx pg.Tx) (err error) {
			got, err = r.s.Workspace(t.Context(), tx, accounts.AccountID(a1), c.id)
			return err
		})
		if got != c.want || !errors.Is(err, c.err) {
			t.Errorf("the workspace %v: %v, %v; want %v, %v", c.id, got, err, c.want, c.err)
		}
	}
	for name, c := range map[string]struct {
		id   accounts.WorkspaceID
		want error
	}{
		"a disabled workspace": {prod.ID, accounts.ErrWorkspaceDisabled},
		"the zero ID":          {accounts.WorkspaceID{}, accounts.ErrWorkspaceNotFound},
		"another account's":    {accounts.WorkspaceID(w2), accounts.ErrWorkspaceNotFound},
	} {
		err := r.inA1(func(tx pg.Tx) error {
			_, err := r.s.CreateKey(t.Context(), tx, system, accounts.KeyRequest{
				Site: accounts.SiteID(s1), Account: accounts.AccountID(a1), Workspace: &c.id, CreatedBy: accounts.UserID(u1), Name: name,
			})
			return err
		})
		if !errors.Is(err, c.want) {
			t.Errorf("a key in %s: %v, want %v", name, err, c.want)
		}
	}
}

// TestWorkspaceLimitLevels: the limit is the account's own over the site's over 50, and making a workspace meets the
// limit in force, the site's read from the node's snapshot.
func TestWorkspaceLimitLevels(t *testing.T) {
	t.Parallel()
	r := newWorkspaceRig(t)
	if site, account := r.limits(); site != (accounts.WorkspaceLimit{Effective: 50}) || account != (accounts.WorkspaceLimit{Effective: 50}) {
		t.Errorf("nothing set: site %+v, account %+v", site, account)
	}
	if err := r.setLimits(new(int32(2)), nil); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, account := r.limits(); account != (accounts.WorkspaceLimit{Effective: 2}) {
		t.Errorf("the account under a site limit of 2: %+v", account)
	}
	if _, err := r.create("Second"); err != nil {
		t.Fatalf("the second of two: %v", err)
	}
	if _, err := r.create("Third"); !errors.Is(err, accounts.ErrTooManyWorkspaces) {
		t.Errorf("the third under the site's 2: %v", err)
	}
	if err := r.setLimits(new(int32(2)), new(int32(3))); err != nil {
		t.Fatal(err)
	}
	if _, err := r.create("Third"); err != nil {
		t.Errorf("the third under the account's 3: %v", err)
	}
}

// TestWorkspaceLimitLowered: lowering an account's limit below what it holds takes no workspace away, and each change
// of a level is audited.
func TestWorkspaceLimitLowered(t *testing.T) {
	t.Parallel()
	r := newWorkspaceRig(t)
	for _, name := range []string{"Second", "Third"} {
		if _, err := r.create(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.setLimits(nil, new(int32(1))); err != nil {
		t.Fatal(err)
	}
	site, account := r.limits()
	if diff := cmp.Diff([2]accounts.WorkspaceLimit{{Effective: 50}, {Own: new(int32(1)), Effective: 1}}, [2]accounts.WorkspaceLimit{site, account}); diff != "" {
		t.Errorf("after lowering, site and account (-want +got):\n%s", diff)
	}
	if held := len(r.list()); held != 3 {
		t.Errorf("a lowered limit kept %d workspaces, want all 3", held)
	}
	var audited string
	if err := r.inPlatform(func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT string_agg(action, ',' ORDER BY id) FROM platform.audit_log WHERE action LIKE '%workspace_limit.update'`).Scan(&audited)
	}); err != nil || audited != "site_workspace_limit.update,account_workspace_limit.update" {
		t.Errorf("audited %q (%v), want one change of each level", audited, err)
	}
}

// TestWorkspaceLimitRefusals: a limit outside 1 to 1,000 is a setting refused, and a site or account that is not there
// is ErrNotFound.
func TestWorkspaceLimitRefusals(t *testing.T) {
	t.Parallel()
	r := newWorkspaceRig(t)
	for name, set := range map[string][2]*int32{"a site limit of 0": {new(int32(0)), nil}, "an account limit of 1001": {nil, new(int32(1001))}} {
		if refused, ok := errors.AsType[*accounts.SettingError](r.setLimits(set[0], set[1])); !ok || refused.Field != "max_workspaces" {
			t.Errorf("%s was not refused as a setting", name)
		}
	}
	nowhere, nobody := accounts.SiteID(uuid.NewV7()), accounts.AccountID(uuid.NewV7())
	for name, fn := range map[string]func(tx pg.Tx) error{
		"reading a site's":     func(tx pg.Tx) error { _, err := r.s.SiteWorkspaceLimit(t.Context(), tx, nowhere); return err },
		"setting a site's":     func(tx pg.Tx) error { return r.s.SetSiteWorkspaceLimit(t.Context(), tx, system, nowhere, nil, "r") },
		"reading an account's": func(tx pg.Tx) error { _, err := r.s.AccountWorkspaceLimit(t.Context(), tx, nobody); return err },
		"setting an account's": func(tx pg.Tx) error { return r.s.SetAccountWorkspaceLimit(t.Context(), tx, system, nobody, nil, "r") },
	} {
		if err := r.inPlatform(fn); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("%s limit that is not there: %v", name, err)
		}
	}
}
