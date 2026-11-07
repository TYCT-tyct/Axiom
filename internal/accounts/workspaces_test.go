package accounts_test

import (
	"errors"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// inA1 runs fn in a1's scope, as the console does.
func inA1(t *testing.T, db *pg.DB, fn func(tx pg.Tx) error) error {
	t.Helper()
	return db.Tx(t.Context(), pg.NewScope(false, nil, []uuid.UUID{a1}), fn)
}

// TestWorkspaceLifecycle: an account makes, reads, lists, renames, disables and enables workspaces; a name taken
// ignoring case, disabling the default and another account's workspace are refused; a key goes to the workspace
// named, and an unknown, the zero or a disabled one is refused rather than taken for the default.
func TestWorkspaceLifecycle(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	s := node(t, db, hashKeys(t, 1), nil)
	account, site := accounts.AccountID(a1), accounts.SiteID(s1)
	var prod accounts.WorkspaceInfo
	if err := inA1(t, db, func(tx pg.Tx) (err error) {
		prod, err = s.CreateWorkspace(t.Context(), tx, system, site, account, "Prod")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err := inA1(t, db, func(tx pg.Tx) error {
		_, err := s.CreateWorkspace(t.Context(), tx, system, site, account, "PROD")
		return err
	})
	if !errors.Is(err, accounts.ErrWorkspaceNameTaken) {
		t.Errorf("a name taken: %v", err)
	}

	update := func(id accounts.WorkspaceID, name string, disabled bool) (accounts.WorkspaceInfo, error) {
		var w accounts.WorkspaceInfo
		err := inA1(t, db, func(tx pg.Tx) (err error) {
			w, err = s.UpdateWorkspace(t.Context(), tx, system, account, id, name, disabled)
			return err
		})
		return w, err
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
		got, err := update(c.id, c.name, c.disabled)
		if !errors.Is(err, c.err) || (c.err == nil && err != nil) {
			t.Errorf("%s disabled=%v: %v, want %v", c.name, c.disabled, err, c.err)
		}
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s disabled=%v (-want +got):\n%s", c.name, c.disabled, diff)
		}
	}

	var list []accounts.WorkspaceInfo
	var named, fallback accounts.WorkspaceID
	if err := inA1(t, db, func(tx pg.Tx) (err error) {
		if list, err = s.ListWorkspaces(t.Context(), tx, account); err != nil {
			return err
		}
		if named, err = s.Workspace(t.Context(), tx, account, &prod.ID); err != nil {
			return err
		}
		fallback, err = s.Workspace(t.Context(), tx, account, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err = inA1(t, db, func(tx pg.Tx) error {
		_, err := s.Workspace(t.Context(), tx, account, new(accounts.WorkspaceID(w2)))
		return err
	})
	if !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("another account's workspace by ID: %v", err)
	}
	want := []accounts.WorkspaceInfo{{ID: accounts.WorkspaceID(w1), Name: "Default", Default: true}, {ID: prod.ID, Name: "Production", Disabled: true}}
	if diff := cmp.Diff(want, list); diff != "" || named != prod.ID || fallback != accounts.WorkspaceID(w1) {
		t.Errorf("the list (-want +got):\n%s\nnamed %v, default %v", diff, named, fallback)
	}

	for _, c := range []struct {
		name string
		id   accounts.WorkspaceID
		want error
	}{
		{"a disabled workspace", prod.ID, accounts.ErrWorkspaceDisabled},
		{"the zero ID", accounts.WorkspaceID{}, accounts.ErrWorkspaceNotFound},
		{"another account's", accounts.WorkspaceID(w2), accounts.ErrWorkspaceNotFound},
	} {
		err := inA1(t, db, func(tx pg.Tx) error {
			_, err := s.CreateKey(t.Context(), tx, system, accounts.KeyRequest{
				Site: site, Account: account, Workspace: &c.id, CreatedBy: accounts.UserID(u1), Name: c.name,
			})
			return err
		})
		if !errors.Is(err, c.want) {
			t.Errorf("a key in %s: %v, want %v", c.name, err, c.want)
		}
	}
}

// TestWorkspaceLimitLevels: the limit is the account's own over the site's over 50; each level is set within 1 to
// 1,000 and refused outside it; a site or account that is not there is ErrNotFound; making a workspace meets the limit in force,
// and lowering it below what the account holds takes none away.
func TestWorkspaceLimitLevels(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	s := node(t, db, hashKeys(t, 1), nil)
	account, site := accounts.AccountID(a1), accounts.SiteID(s1)
	read := func() (siteLimit, accountLimit accounts.WorkspaceLimit) {
		t.Helper()
		if err := db.Tx(t.Context(), platform, func(tx pg.Tx) (err error) {
			if siteLimit, err = s.SiteWorkspaceLimit(t.Context(), tx, site); err != nil {
				return err
			}
			accountLimit, err = s.AccountWorkspaceLimit(t.Context(), tx, account)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return siteLimit, accountLimit
	}
	set := func(siteLimit, accountLimit *int32) error {
		return db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			if err := s.SetSiteWorkspaceLimit(t.Context(), tx, system, site, siteLimit, "r"); err != nil {
				return err
			}
			return s.SetAccountWorkspaceLimit(t.Context(), tx, system, account, accountLimit, "r")
		})
	}
	if siteLimit, accountLimit := read(); siteLimit != (accounts.WorkspaceLimit{Effective: 50}) || accountLimit != (accounts.WorkspaceLimit{Effective: 50}) {
		t.Errorf("nothing set: site %+v, account %+v", siteLimit, accountLimit)
	}
	if err := set(new(int32(2)), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, accountLimit := read(); accountLimit.Own != nil || accountLimit.Effective != 2 {
		t.Errorf("the account under a site limit of 2: %+v", accountLimit)
	}
	makeOne := func(name string) error {
		return inA1(t, db, func(tx pg.Tx) error {
			_, err := s.CreateWorkspace(t.Context(), tx, system, site, account, name)
			return err
		})
	}
	if err := makeOne("Second"); err != nil {
		t.Fatalf("the second of two: %v", err)
	}
	if err := makeOne("Third"); !errors.Is(err, accounts.ErrTooManyWorkspaces) {
		t.Errorf("the third under the site's 2: %v", err)
	}
	if err := set(new(int32(2)), new(int32(3))); err != nil {
		t.Fatal(err)
	}
	if err := makeOne("Third"); err != nil {
		t.Errorf("the third under the account's 3: %v", err)
	}
	if err := set(nil, new(int32(1))); err != nil {
		t.Fatal(err)
	}
	if siteLimit, accountLimit := read(); siteLimit.Own != nil || *accountLimit.Own != 1 || accountLimit.Effective != 1 {
		t.Errorf("after lowering: site %+v, account %+v", siteLimit, accountLimit)
	}
	var held []accounts.WorkspaceInfo
	if err := inA1(t, db, func(tx pg.Tx) (err error) {
		held, err = s.ListWorkspaces(t.Context(), tx, account)
		return err
	}); err != nil || len(held) != 3 {
		t.Errorf("a lowered limit kept %d workspaces (%v), want all 3", len(held), err)
	}

	for _, c := range []struct {
		name               string
		siteLimit, account *int32
	}{{"a site limit of 0", new(int32(0)), nil}, {"an account limit of 1001", nil, new(int32(1001))}} {
		if _, ok := errors.AsType[*accounts.SettingError](set(c.siteLimit, c.account)); !ok {
			t.Errorf("%s was not refused as a setting", c.name)
		}
	}
	nowhere, nobody := accounts.SiteID(uuid.NewV7()), accounts.AccountID(uuid.NewV7())
	for name, fn := range map[string]func(tx pg.Tx) error{
		"reading a site's":     func(tx pg.Tx) error { _, err := s.SiteWorkspaceLimit(t.Context(), tx, nowhere); return err },
		"setting a site's":     func(tx pg.Tx) error { return s.SetSiteWorkspaceLimit(t.Context(), tx, system, nowhere, nil, "r") },
		"reading an account's": func(tx pg.Tx) error { _, err := s.AccountWorkspaceLimit(t.Context(), tx, nobody); return err },
		"setting an account's": func(tx pg.Tx) error { return s.SetAccountWorkspaceLimit(t.Context(), tx, system, nobody, nil, "r") },
	} {
		if err := db.Tx(t.Context(), platform, fn); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("%s limit that is not there: %v", name, err)
		}
	}
	var audited string
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT string_agg(action, ',' ORDER BY id) FROM platform.audit_log WHERE action LIKE '%workspace_limit%'`).Scan(&audited)
	}); err != nil {
		t.Fatal(err)
	}
	if want := "site_workspace_limit.update,account_workspace_limit.update,site_workspace_limit.update,account_workspace_limit.update," +
		"site_workspace_limit.update,account_workspace_limit.update"; audited != want {
		t.Errorf("audited %s, want %s", audited, want)
	}
}
