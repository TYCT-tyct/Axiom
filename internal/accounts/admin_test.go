package accounts_test

import (
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var operatorActor = audit.User(uuid.NewV7())

// TestSiteSettingsBounds: every value has the bounds the table keeps and the contract states, and a value outside
// them is refused by name before anything is written; unset values and the edges pass.
func TestSiteSettingsBounds(t *testing.T) {
	t.Parallel()
	ok := accounts.SiteSettings{
		ConsoleIdle: 5 * time.Minute, ConsoleMaxAge: 90 * 24 * time.Hour, AdminIdle: 12 * time.Hour, AdminMaxAge: 24 * time.Hour,
		NewKeyDailyLimit: new(int64(1_000_000_000_000_000)),
	}
	for name, c := range map[string]struct {
		change func(*accounts.SiteSettings)
		field  string
	}{
		"nothing set":                   {func(v *accounts.SiteSettings) { *v = accounts.SiteSettings{} }, ""},
		"the edges":                     {func(*accounts.SiteSettings) {}, ""},
		"a limit of nothing":            {func(v *accounts.SiteSettings) { v.NewKeyDailyLimit = new(int64(0)) }, ""},
		"a console idle time too short": {func(v *accounts.SiteSettings) { v.ConsoleIdle = 299 * time.Second }, "console_idle_timeout_seconds"},
		"a console idle time too long":  {func(v *accounts.SiteSettings) { v.ConsoleIdle = 30*24*time.Hour + time.Second }, "console_idle_timeout_seconds"},
		"a console age too long":        {func(v *accounts.SiteSettings) { v.ConsoleMaxAge = 90*24*time.Hour + time.Second }, "console_max_age_seconds"},
		"an admin idle time too long":   {func(v *accounts.SiteSettings) { v.AdminIdle = 12*time.Hour + time.Second }, "admin_idle_timeout_seconds"},
		"an admin age too long":         {func(v *accounts.SiteSettings) { v.AdminMaxAge = 24*time.Hour + time.Second }, "admin_max_age_seconds"},
		"a negative limit":              {func(v *accounts.SiteSettings) { v.NewKeyDailyLimit = new(int64(-1)) }, "new_key_daily_limit"},
		"a limit over a million":        {func(v *accounts.SiteSettings) { v.NewKeyDailyLimit = new(int64(1_000_000_000_000_001)) }, "new_key_daily_limit"},
	} {
		v := ok
		c.change(&v)
		if err := v.Check(); !refuses(err, c.field) {
			t.Errorf("%s: %v, want a refusal of %q", name, err, c.field)
		}
	}
}

// refuses is whether err is a refusal of the setting called field, or, for an empty field, no error.
func refuses(err error, field string) bool {
	if field == "" {
		return err == nil
	}
	refusal, ok := errors.AsType[*accounts.SettingError](err)
	return ok && refusal.Field == field
}

// settingsOf is a node on a seeded database and the means to read and write site settings and count audit entries.
type settingsOf struct {
	t  *testing.T
	db *pg.DB
	s  *accounts.Service
}

func newSettingsOf(t *testing.T) settingsOf {
	t.Helper()
	db, _ := open(t)
	return settingsOf{t, db, node(t, db, hashKeys(t, 1), nil)}
}

func (o settingsOf) read(site uuid.UUID) (v accounts.SiteSettings, err error) {
	err = o.db.ReadTx(o.t.Context(), platform, func(tx pg.Tx) (err error) {
		v, err = o.s.SiteSettings(o.t.Context(), tx, accounts.SiteID(site))
		return err
	})
	return v, err
}

func (o settingsOf) write(site uuid.UUID, v accounts.SiteSettings) error {
	return o.db.Tx(o.t.Context(), platform, func(tx pg.Tx) error {
		return o.s.SetSiteSettings(o.t.Context(), tx, operatorActor, accounts.SiteID(site), v, "because")
	})
}

func (o settingsOf) entries() (n int) {
	o.t.Helper()
	if err := o.db.Tx(o.t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(o.t.Context(), `SELECT count(*) FROM platform.audit_log WHERE action = 'site_settings.update'`).Scan(&n)
	}); err != nil {
		o.t.Fatal(err)
	}
	return n
}

// mustRead reads the site's settings.
func (o settingsOf) mustRead(site uuid.UUID) accounts.SiteSettings {
	o.t.Helper()
	v, err := o.read(site)
	if err != nil {
		o.t.Fatal(err)
	}
	return v
}

// TestSiteSettingsAreWrittenWhole: a site's settings are read as they were set, a write replaces them all, and each
// change is audited.
func TestSiteSettingsAreWrittenWhole(t *testing.T) {
	t.Parallel()
	o := newSettingsOf(t)
	if diff := cmp.Diff(accounts.SiteSettings{}, o.mustRead(s1)); diff != "" {
		t.Fatalf("a site with no settings row (-want +got):\n%s", diff)
	}
	for _, set := range []accounts.SiteSettings{
		{ConsoleIdle: time.Hour, AdminMaxAge: 2 * time.Hour, NewKeyDailyLimit: new(int64(0))},
		{AdminIdle: time.Hour},
	} {
		if err := o.write(s1, set); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(set, o.mustRead(s1)); diff != "" {
			t.Errorf("settings as set (-want +got):\n%s", diff)
		}
	}
	if n := o.entries(); n != 2 {
		t.Errorf("%d audit entries, want 2", n)
	}
}

// TestSiteSettingsRefusals: a refused change writes neither the settings nor the entry, and a site that is not there is
// ErrNotFound for the read and the write alike.
func TestSiteSettingsRefusals(t *testing.T) {
	t.Parallel()
	o := newSettingsOf(t)
	if err := o.write(s1, accounts.SiteSettings{AdminIdle: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := o.write(s1, accounts.SiteSettings{AdminIdle: time.Second}); !refuses(err, "admin_idle_timeout_seconds") || o.entries() != 1 {
		t.Errorf("a value out of bounds: %v, %d entries", err, o.entries())
	}
	if got := o.mustRead(s1); got.AdminIdle != time.Hour {
		t.Errorf("a refused write changed the settings: %+v", got)
	}
	if _, err := o.read(uuid.NewV7()); !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("reading a site that is not there: %v", err)
	}
	if err := o.write(uuid.NewV7(), accounts.SiteSettings{}); !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("writing a site that is not there: %v", err)
	}
}

// adminView is a node on a seeded database that also has a user x in the account p, made after u1 and u2.
type adminView struct {
	t  *testing.T
	db *pg.DB
	s  *accounts.Service
}

func newAdminView(t *testing.T) adminView {
	t.Helper()
	db, database := open(t)
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(user(":x", ":s1", ":p", "NULL"))); err != nil {
		t.Fatal(err)
	}
	return adminView{t, db, node(t, db, hashKeys(t, 1), nil)}
}

func (v adminView) list(q accounts.UserQuery) (users []accounts.UserSummary, more bool) {
	v.t.Helper()
	if err := v.db.ReadTx(v.t.Context(), platform, func(tx pg.Tx) (err error) {
		users, more, err = v.s.Users(v.t.Context(), tx, q)
		return err
	}); err != nil {
		v.t.Fatal(err)
	}
	return users, more
}

func idsOf(users []accounts.UserSummary) []uuid.UUID {
	var out []uuid.UUID
	for _, u := range users {
		out = append(out, uuid.UUID(u.ID))
	}
	return out
}

// TestUsersInPages: users newest first, in pages that follow each other, and by site. The seed made u1, then u2, then
// x: newest first is x, u2, u1.
func TestUsersInPages(t *testing.T) {
	t.Parallel()
	v := newAdminView(t)
	all, more := v.list(accounts.UserQuery{PageSize: 10})
	if !slices.Equal(idsOf(all), []uuid.UUID{x, u2, u1}) || more {
		t.Errorf("all the users: %v more=%v", idsOf(all), more)
	}
	first, more := v.list(accounts.UserQuery{PageSize: 2})
	rest, moreRest := v.list(accounts.UserQuery{PageSize: 2, After: first[1].ID})
	if !slices.Equal(idsOf(first), []uuid.UUID{x, u2}) || !more || !slices.Equal(idsOf(rest), []uuid.UUID{u1}) || moreRest {
		t.Errorf("pages of 2: %v more=%v, then %v more=%v", idsOf(first), more, idsOf(rest), moreRest)
	}
}

// TestUsersOfASite: by site, each with its account, site, tier and status.
func TestUsersOfASite(t *testing.T) {
	t.Parallel()
	v := newAdminView(t)
	s2Only, _ := v.list(accounts.UserQuery{PageSize: 10, Site: new(accounts.SiteID(s2))})
	if !slices.Equal(idsOf(s2Only), []uuid.UUID{u2}) {
		t.Fatalf("the users of s2: %v", idsOf(s2Only))
	}
	if u := s2Only[0]; u.Site != accounts.SiteID(s2) || u.Account != accounts.AccountID(a2) || u.Tier.Name != "Retail" || u.Status != "active" || u.MFA ||
		time.Since(u.CreatedAt) > time.Hour {
		t.Errorf("u2 as listed: %+v", u)
	}
}

// TestUsersByEmailPrefix: the prefix is matched literally, whatever its case, and a wildcard is just a character.
func TestUsersByEmailPrefix(t *testing.T) {
	t.Parallel()
	v := newAdminView(t)
	for prefix, want := range map[string]int{"user": 3, "USER": 3, "user%": 0, "%": 0, "_ser": 0, "user_": 0, "nobody": 0} {
		if got, _ := v.list(accounts.UserQuery{PageSize: 10, EmailPrefix: prefix}); len(got) != want {
			t.Errorf("the prefix %q matched %d users, want %d", prefix, len(got), want)
		}
	}
}

// TestAccountAndTiers: an account with its tier and owner, tiers by name; an account that is not there is ErrNotFound.
func TestAccountAndTiers(t *testing.T) {
	t.Parallel()
	v := newAdminView(t)
	var account accounts.AccountDetail
	if err := v.db.ReadTx(t.Context(), platform, func(tx pg.Tx) (err error) {
		account, err = v.s.Account(t.Context(), tx, accounts.AccountID(a2))
		return err
	}); err != nil || account.Owner.ID != accounts.UserID(u2) || account.Tier.Name != "Retail" || account.Kind != "personal" || account.Site != accounts.SiteID(s2) {
		t.Errorf("the account of u2: %+v %v", account, err)
	}
	if err := v.db.ReadTx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := v.s.Account(t.Context(), tx, accounts.AccountID(uuid.NewV7()))
		return err
	}); !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("an account that is not there: %v", err)
	}
	var tiers []accounts.Tier
	if err := v.db.ReadTx(t.Context(), platform, func(tx pg.Tx) (err error) {
		tiers, err = v.s.Tiers(t.Context(), tx, accounts.SiteID(s2))
		return err
	}); err != nil || len(tiers) != 1 || tiers[0].Name != "Retail" || tiers[0].ID != accounts.TierID(t2) {
		t.Errorf("the tiers of s2: %+v %v", tiers, err)
	}
}
