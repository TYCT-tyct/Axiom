package accounts_test

import (
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/telemetry"
	"axiom/internal/platform/telemetry/telemetrytest"
)

// accountScope is the scope of the signed-in user's own account.
func accountScope(p accounts.Principal) pg.Scope {
	return pg.NewScope(false, nil, []uuid.UUID{uuid.UUID(p.AccountID())})
}

// inAccount runs fn in the user's account scope, committing.
func (x site) inAccount(t *testing.T, p accounts.Principal, fn func(tx pg.Tx) error) {
	t.Helper()
	if err := x.db.Tx(t.Context(), accountScope(p), fn); err != nil {
		t.Fatal(err)
	}
}

// makeKey creates a key for p's user with settings and returns its ID.
func (x site) makeKey(t *testing.T, p accounts.Principal, settings accounts.KeySettings) accounts.KeyID {
	t.Helper()
	var id accounts.KeyID
	x.inAccount(t, p, func(tx pg.Tx) error {
		created, err := x.s.CreateKey(t.Context(), tx, system, accounts.KeyRequest{
			Site: accounts.SiteID(s1), Account: p.AccountID(), CreatedBy: p.UserID(), KeySettings: settings,
		})
		id = created.ID
		return err
	})
	return id
}

func (x site) signedIn(t *testing.T) accounts.Principal {
	t.Helper()
	p, err := x.principal(t, console, x.signIn(t, console))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCreateKeySettings: a key made with no settings gets the site's daily limit for new keys, in the account's default
// workspace; one with a limit and no reset never resets; models and addresses are kept, and empty ones mean none.
func TestCreateKeySettings(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	p := x.signedIn(t)
	limit := int64(5)
	cases := []struct {
		name     string
		settings accounts.KeySettings
		want     string
	}{
		{"none", accounts.KeySettings{Name: "a"}, "a 10000000000 daily  "},
		{"a limit that never resets", accounts.KeySettings{Name: "b", SpendLimit: &limit}, "b 5   "},
		{"a weekly limit", accounts.KeySettings{Name: "c", SpendLimit: &limit, LimitReset: accounts.ResetWeekly}, "c 5 weekly  "},
		{"models and addresses", accounts.KeySettings{Name: "d", SpendLimit: &limit, Models: []string{"m1", "m2"}, IPAllow: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}, "d 5  m1,m2 10.0.0.0/8"},
		{"empty lists", accounts.KeySettings{Name: "e", SpendLimit: &limit, Models: []string{}, IPAllow: []netip.Prefix{}}, "e 5   "},
	}
	for _, c := range cases {
		id := x.makeKey(t, p, c.settings)
		got := texts(t, x.db, platform, "SELECT name || ' ' || spend_limit || ' ' || coalesce(limit_reset, '') || ' ' || coalesce(array_to_string(models, ','), '') || ' ' || coalesce(array_to_string(ip_allow, ','), '') "+
			"FROM accounts.api_keys WHERE id = '"+uuid.UUID(id).String()+"'")
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: %v, want %q", c.name, got, c.want)
		}
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.api_keys k JOIN accounts.workspaces w ON w.id = k.workspace_id WHERE w.is_default AND w.account_id = '"+
		uuid.UUID(p.AccountID()).String()+"'"); got[0] != "5" {
		t.Errorf("%s keys in the default workspace, want 5", got[0])
	}
}

func keyNames(keys []accounts.Key) []string {
	names := []string{}
	for _, k := range keys {
		names = append(names, k.Name)
	}
	return names
}

// TestListKeys: keys come newest first, a page at a time by the last key's ID, disabled ones included and deleted ones
// not.
func TestListKeys(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	p := x.signedIn(t)
	var ids []accounts.KeyID
	for _, name := range []string{"one", "two", "three", "four"} {
		ids = append(ids, x.makeKey(t, p, accounts.KeySettings{Name: name}))
	}
	x.inAccount(t, p, func(tx pg.Tx) error {
		if err := disableKey(t.Context(), tx, x.s, ids[0]); err != nil {
			return err
		}
		return x.s.DeleteKey(t.Context(), tx, system, ids[1])
	})
	var first, second []accounts.Key
	var more, moreAfter bool
	x.inAccount(t, p, func(tx pg.Tx) (err error) {
		if first, more, err = x.s.ListKeys(t.Context(), tx, p.AccountID(), accounts.KeyID{}, 1); err != nil {
			return err
		}
		second, moreAfter, err = x.s.ListKeys(t.Context(), tx, p.AccountID(), first[0].ID, 5)
		return err
	})
	if diff := cmp.Diff([]string{"four"}, keyNames(first)); diff != "" || !more {
		t.Errorf("the first page (-want +got):\n%s, more %v", diff, more)
	}
	if diff := cmp.Diff([]string{"three", "one"}, keyNames(second)); diff != "" || moreAfter || !second[1].Disabled {
		t.Errorf("the second page (-want +got):\n%s, more %v", diff, moreAfter)
	}
}

// TestGetKey: a key reads with the time its ID carries; a deleted key and another account's key are not found.
func TestGetKey(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	p := x.signedIn(t)
	before := time.Now().Add(-time.Second)
	id, doomed := x.makeKey(t, p, accounts.KeySettings{Name: "kept"}), x.makeKey(t, p, accounts.KeySettings{Name: "doomed"})
	x.inAccount(t, p, func(tx pg.Tx) error { return x.s.DeleteKey(t.Context(), tx, system, doomed) })
	x.inAccount(t, p, func(tx pg.Tx) error {
		key, err := x.s.GetKey(t.Context(), tx, id)
		if err != nil || key.Name != "kept" || key.CreatedAt.Before(before) || key.CreatedAt.After(time.Now().Add(time.Second)) {
			t.Errorf("a key: %+v, %v, created at the ID's time %v, want since %v", key, err, key.CreatedAt, before)
		}
		return nil
	})
	for name, missing := range map[string]accounts.KeyID{"a deleted key": doomed, "another account's key": accounts.KeyID(k1)} {
		x.inAccount(t, p, func(tx pg.Tx) error {
			if _, err := x.s.GetKey(t.Context(), tx, missing); !errors.Is(err, accounts.ErrKeyNotFound) {
				t.Errorf("%s: %v", name, err)
			}
			return nil
		})
	}
}

// TestUpdateKey: the settings are replaced whole, and a disabled key is enabled again.
func TestUpdateKey(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	p := x.signedIn(t)
	limit := int64(9)
	id := x.makeKey(t, p, accounts.KeySettings{Name: "before", SpendLimit: &limit, Models: []string{"m"}, IPAllow: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	update := func(settings accounts.KeySettings) (updated accounts.Key) {
		x.inAccount(t, p, func(tx pg.Tx) (err error) {
			updated, err = x.s.UpdateKey(t.Context(), tx, system, id, settings)
			return err
		})
		return updated
	}
	replaced := accounts.KeySettings{Name: "after", Disabled: true}
	if diff := cmp.Diff(replaced, update(replaced).KeySettings); diff != "" {
		t.Errorf("after replacing (-want +got):\n%s", diff)
	}
	again := accounts.KeySettings{Name: "again", SpendLimit: &limit, LimitReset: accounts.ResetMonthly}
	if diff := cmp.Diff(again, update(again).KeySettings); diff != "" {
		t.Errorf("enabled again (-want +got):\n%s", diff)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'api_key.update'"); got[0] != "2" {
		t.Errorf("%s audit entries for two updates", got[0])
	}
}

// TestUpdateKeyOfAnotherAccount: another account's key is not found, and stays as it was.
func TestUpdateKeyOfAnotherAccount(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	p := x.signedIn(t)
	x.inAccount(t, p, func(tx pg.Tx) error {
		if _, err := x.s.UpdateKey(t.Context(), tx, system, accounts.KeyID(k1), accounts.KeySettings{Name: "mine"}); !errors.Is(err, accounts.ErrKeyNotFound) {
			t.Errorf("another account's key: %v", err)
		}
		return nil
	})
	if got := texts(t, x.db, platform, "SELECT name FROM accounts.api_keys WHERE id = '"+k1.String()+"'"); got[0] == "mine" {
		t.Error("another account's key was changed")
	}
}

// sessionInfoOf reads the session of p.
func (x site) sessionInfoOf(t *testing.T, p accounts.Principal) accounts.SessionInfo {
	t.Helper()
	info, err := x.s.SessionInfo(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// ownerHolds makes the owner role hold exactly these permissions, and reloads the snapshot.
func (x site) ownerHolds(t *testing.T, permissions string) {
	t.Helper()
	if err := pgtest.Run(t.Context(), x.database.Superuser,
		"ALTER TABLE accounts.roles NO FORCE ROW LEVEL SECURITY",
		"UPDATE accounts.roles SET permissions = '{"+permissions+"}' WHERE name = 'owner'",
		"ALTER TABLE accounts.roles FORCE ROW LEVEL SECURITY"); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestSessionInfo: a session reads itself, with the permissions its level allows: a password session lacks the
// sensitive ones, and a verified one has them.
func TestSessionInfo(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	x.ownerHolds(t, "api_keys.view,calls.view,sites.manage")
	first := x.signIn(t, console)
	p, err := x.principal(t, console, first)
	if err != nil {
		t.Fatal(err)
	}
	info := x.sessionInfoOf(t, p)
	want := accounts.SessionInfo{
		User: x.user, Account: p.AccountID(), Email: "someone@example.com", InviteCode: info.InviteCode, Level: "password",
		CSRFToken: first.CSRFToken, ExpiresAt: info.ExpiresAt, Permissions: []string{"api_keys.view", "calls.view"},
	}
	if diff := cmp.Diff(want, info); diff != "" || len(info.InviteCode) < 6 || !info.ExpiresAt.After(time.Now()) {
		t.Errorf("a password session (-want +got):\n%s", diff)
	}
	_, verified, _ := x.enableTOTP(t, console, first)
	if p, err = x.principal(t, console, verified); err != nil {
		t.Fatal(err)
	}
	if info = x.sessionInfoOf(t, p); info.Level != "mfa" || !info.MFAEnabled || !slices.Equal(info.Permissions, []string{"api_keys.view", "calls.view", "sites.manage"}) {
		t.Errorf("a verified session: %+v", info)
	}
}

// TestPendingSessionInfo: a session that waits for its second factor may read itself, and holds no permission; it is
// not signed in for anything else.
func TestPendingSessionInfo(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	x.enableTOTP(t, console, x.signIn(t, console))
	pending := x.signIn(t, console)
	p, err := x.s.AuthenticateAny(t.Context(), x.request(t, console, "https://"+console, pending))
	if err != nil || !p.Pending() {
		t.Fatalf("a pending session through AuthenticateAny: %v, %v", p, err)
	}
	if info := x.sessionInfoOf(t, p); info.Level != "pending_mfa" || info.Permissions == nil || len(info.Permissions) != 0 {
		t.Errorf("a pending session: %+v", info)
	}
	if _, err := x.s.Authenticate(t.Context(), x.request(t, console, "https://"+console, pending)); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("a pending session through Authenticate: %v", err)
	}
}

// TestRefusalsSayHowLongToWait: a refused attempt knows when the bucket lets the next through.
func TestRefusalsSayHowLongToWait(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	var last error
	for range 21 { // the burst is 20, and a token comes back every 6 seconds
		_, last = x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: "nobody" + uuid.NewV7().String() + "@example.com", Password: password, ClientIP: client})
	}
	var tooMany *accounts.TooManyAttempts
	if !errors.As(last, &tooMany) || !errors.Is(last, accounts.ErrTooManyAttempts) || tooMany.RetryAfter <= 0 || tooMany.RetryAfter > 6*time.Second {
		t.Fatalf("the attempt past the burst: %v, want to wait up to 6 seconds", last)
	}
	x.clock.Advance(tooMany.RetryAfter)
	if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: password, ClientIP: client}); err != nil {
		t.Errorf("after waiting as told: %v", err)
	}
}

// TestConfirmTOTPCountsWrongGuessesOnly: wrong codes spend the user's code failures, so the endpoint is no way to guess
// a password; a refusal that guesses nothing, such as turning on what is on, spends none.
func TestConfirmTOTPCountsWrongGuessesOnly(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	p := x.signedIn(t)
	key, _ := x.s.BeginTOTP(p)
	right := accounts.TOTPCode(key, x.clock.Now())
	wrong := "000000"
	if right == wrong {
		wrong = "000001"
	}
	for i := range 10 {
		if _, _, err := x.s.ConfirmTOTP(t.Context(), p, accounts.FactorChange{Secret: key, Password: password, Code: wrong}); !errors.Is(err, accounts.ErrBadCode) {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	if _, _, err := x.s.ConfirmTOTP(t.Context(), p, accounts.FactorChange{Secret: key, Password: password, Code: right}); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Errorf("the right code after ten wrong ones: %v", err)
	}
}

func TestRefusalsThatGuessNothingAreNotCounted(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	_, verified, _ := x.enableTOTP(t, console, x.signIn(t, console))
	p, err := x.principal(t, console, verified)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		key, _ := x.s.BeginTOTP(p)
		if _, _, err := x.s.ConfirmTOTP(t.Context(), p, accounts.FactorChange{Secret: key, Password: password, Code: accounts.TOTPCode(key, x.clock.Now())}); !errors.Is(err, accounts.ErrTOTPOn) {
			t.Fatalf("turning it on again, time %d: %v", i, err)
		}
	}
}

// TestKeyCacheMissesAreCounted: a lookup that goes to the database is counted, one answered from memory is not, and
// the count reaches the scrape under its Prometheus name through the process's metrics base.
func TestKeyCacheMissesAreCounted(t *testing.T) {
	t.Parallel()
	db, _, _, key := callSetup(t)
	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	s, err := accounts.NewService(db, accounts.Config{
		Platform: platform, Hashes: hashKeys(t, 1), Keyring: keyring(t), Meter: tel.Metrics.Meter("axiom/accounts"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), accounts.NewKeyText("sk-ax-v1-")); !errors.Is(err, accounts.ErrUnknownKey) {
		t.Fatal(err)
	}
	// The name is the one an operator's scrape of the ops listener sees.
	if misses := telemetrytest.Metric(t, tel, "axiom_accounts_key_cache_misses_total"); misses != "2" {
		t.Errorf("the scrape counts %s misses for one known key looked up three times and one unknown, want 2", misses)
	}
}

// TestPermissionsAreDeclared: the permissions the console's interface names are the ones this program declares, held by
// the built-in owner after the migrations, and a name nobody declared is none.
func TestPermissionsAreDeclared(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"api_keys.view", "api_keys.manage", "calls.view", "balance.view"} {
		if _, ok := accounts.PermissionNamed(name); !ok {
			t.Errorf("%s is not declared", name)
		}
	}
	if _, ok := accounts.PermissionNamed("balance.wipe"); ok {
		t.Error("an undeclared permission was found")
	}
	x := newTestSite(t)
	if got := texts(t, x.db, platform, "SELECT array_to_string(permissions, ',') FROM accounts.roles WHERE name = 'owner'"); len(got) != 1 ||
		got[0] != "api_keys.view,api_keys.manage,workspaces.manage,calls.view,balance.view" {
		t.Errorf("the owner's permissions: %v", got)
	}
}
