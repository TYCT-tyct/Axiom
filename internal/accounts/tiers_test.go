package accounts_test

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// TestTierAdministration: an operator adds, renames, disables and chooses the default tier; the default is never a
// disabled tier, nothing moves onto a disabled one, and a new account joins the default chosen last.
func TestTierAdministration(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	site := accounts.SiteID(s1)
	do := func(f func(tx pg.Tx) error) error {
		return x.db.Tx(t.Context(), platform, f)
	}
	var pro accounts.Tier
	if err := do(func(tx pg.Tx) (err error) {
		pro, err = x.s.CreateTier(t.Context(), tx, system, site, "  Pro ", "a paid tier")
		return err
	}); err != nil || pro.Name != "Pro" {
		t.Fatalf("create Pro: %+v, %v", pro, err)
	}
	defaultTier := x.defaultTier(t, site)
	disable, enable, rename := true, false, "Plus"
	for _, c := range []struct {
		name string
		do   func(tx pg.Tx) error
		want error
	}{
		{"a name another tier has", func(tx pg.Tx) error {
			_, err := x.s.CreateTier(t.Context(), tx, system, site, "pro", "again")
			return err
		}, accounts.ErrTierNameTaken},
		{"an empty name", func(tx pg.Tx) error {
			_, err := x.s.CreateTier(t.Context(), tx, system, site, " ", "blank")
			return err
		}, &accounts.SettingError{}},
		{"disable the default", func(tx pg.Tx) error {
			_, err := x.s.UpdateTier(t.Context(), tx, system, defaultTier, accounts.TierChange{Disabled: &disable}, "retire")
			return err
		}, accounts.ErrDefaultTier},
		{"rename and disable Pro", func(tx pg.Tx) error {
			_, err := x.s.UpdateTier(t.Context(), tx, system, pro.ID, accounts.TierChange{Name: &rename, Disabled: &disable}, "retire")
			return err
		}, nil},
		{"make a disabled tier the default", func(tx pg.Tx) error {
			return x.s.SetDefaultTier(t.Context(), tx, system, pro.ID, "promote")
		}, &accounts.SettingError{}},
		{"move an account onto a disabled tier", func(tx pg.Tx) error {
			return x.s.SetAccountTier(t.Context(), tx, system, accounts.AccountID(a1), pro.ID, "upgrade")
		}, &accounts.SettingError{}},
		{"enable Pro again", func(tx pg.Tx) error {
			_, err := x.s.UpdateTier(t.Context(), tx, system, pro.ID, accounts.TierChange{Disabled: &enable}, "back")
			return err
		}, nil},
		{"make Pro the default", func(tx pg.Tx) error {
			return x.s.SetDefaultTier(t.Context(), tx, system, pro.ID, "promote")
		}, nil},
		{"disable the old default", func(tx pg.Tx) error {
			_, err := x.s.UpdateTier(t.Context(), tx, system, defaultTier, accounts.TierChange{Disabled: &disable}, "retire")
			return err
		}, nil},
	} {
		if err := do(c.do); !refusedAs(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	user, err := x.s.Register(t.Context(), accounts.RegisterRequest{
		Host: console, InviteCode: inviteCode(t, x.db, ":u1"), Email: "late@example.com", Password: password,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT t.name FROM accounts.users u JOIN accounts.accounts a ON a.id = u.account_id "+
		"JOIN accounts.tiers t ON t.id = a.tier_id WHERE u.id = '"+uuid.UUID(user.User).String()+"'"); got[0] != "Plus" {
		t.Errorf("a new account joined %v, want the default chosen last, Plus", got)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action IN ('tier.create','tier.update','site.default_tier_change')"); got[0] != "5" {
		t.Errorf("tier audit entries = %v, want 5", got)
	}
}

// defaultTier is the site's default tier, as the tier list marks it.
func (x site) defaultTier(t *testing.T, site accounts.SiteID) accounts.TierID {
	t.Helper()
	var tiers []accounts.Tier
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) (err error) {
		tiers, err = x.s.Tiers(t.Context(), tx, site)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tier := range tiers {
		if tier.Default {
			return tier.ID
		}
	}
	t.Fatal("the site's tiers name no default")
	return accounts.TierID{}
}

// refusedAs reports whether err is want: any *SettingError when want is one, else errors.Is.
func refusedAs(err, want error) bool {
	if _, setting := want.(*accounts.SettingError); setting {
		_, ok := errors.AsType[*accounts.SettingError](err)
		return ok
	}
	return errors.Is(err, want)
}
