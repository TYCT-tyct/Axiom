package accounts_test

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// passkeySite turns passkeys on for the test's site and returns a node that has loaded the setting.
func passkeySite(t *testing.T) (site, *accounts.Service) {
	t.Helper()
	x := newTestSite(t)
	s := oauthService(t, x.db, egress.NewForTesting(egresstest.Loopback(t)))
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := console
		return s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{
			LoginDomain: &domain, Passkeys: true,
		}, "test")
	}); err != nil {
		t.Fatal(err)
	}
	x.s = s
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	return x, s
}

// storPasskey gives the test user a credential without a WebAuthn ceremony when only its existence matters.
func storePasskey(t *testing.T, x site) {
	t.Helper()
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`INSERT INTO accounts.passkey_credentials (id, site_id, account_id, user_id, credential_id, public_key, name)
		 SELECT :x, u.site_id, u.account_id, u.id, '\x01', '\x02', 'Test' FROM accounts.users u
		 WHERE u.email = 'someone@example.com'`)); err != nil {
		t.Fatal(err)
	}
}

// TestPasskeyBlocksTOTPEnrollmentFromAdmin: an admin password session cannot turn a passkey-only user into a TOTP
// user. Otherwise a password holder could replace the factor they cannot prove with one the admin listener can set up.
func TestPasskeyBlocksTOTPEnrollmentFromAdmin(t *testing.T) {
	t.Parallel()
	x, s := passkeySite(t)
	storePasskey(t, x)
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminSession := x.signIn(t, admin)
	p, err := s.Authenticate(t.Context(), x.request(t, admin, "https://"+admin, adminSession), client)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := s.BeginTOTP(p)
	if _, _, err := s.ConfirmTOTP(t.Context(), p, accounts.FactorChange{
		Secret: key, Password: password, Code: accounts.TOTPCode(key, x.clock.Now()),
	}); !errors.Is(err, accounts.ErrMFARequired) {
		t.Fatalf("TOTP on an admin password session with a passkey: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.users WHERE mfa_enabled_at IS NOT NULL"); got[0] != "0" {
		t.Errorf("TOTP was enabled without the passkey: %v", got)
	}
}

// TestDisablingPasskeysCanClearTheDomain: with no credentials, turning passkeys off and clearing the login domain is
// one allowed change; the asset checks already cover what must stay.
func TestDisablingPasskeysCanClearTheDomain(t *testing.T) {
	t.Parallel()
	x, s := passkeySite(t)
	empty := ""
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{
			LoginDomain: &empty, Passkeys: false,
		}, "test")
	}); err != nil {
		t.Fatalf("disabling passkeys and clearing the domain: %v", err)
	}
}

// TestPasskeyRegistrationRechecksTheSiteSetting: a ceremony begun before an operator turns passkeys off must not store
// a credential against the new setting, even when the node's snapshot is still the old one.
func TestPasskeyRegistrationRechecksTheSiteSetting(t *testing.T) {
	t.Parallel()
	x, s := passkeySite(t)
	session := x.signIn(t, console)
	p, err := s.Authenticate(t.Context(), x.request(t, console, "https://"+console, session), client)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := s.BeginPasskeyRegistration(t.Context(), passkeyRequest(t, nil), p, "Laptop", password)
	if err != nil {
		t.Fatal(err)
	}
	evidence := newSoftAuthenticator(t).register(t, registration.PublicKey)
	// Deliberately no Refresh: the node still believes passkeys are on.
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{
			LoginDomain: func() *string { d := console; return &d }(), Passkeys: false,
		}, "test")
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FinishPasskeyRegistration(t.Context(), passkeyRequest(t, evidence), p, registration.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeysOff) {
		t.Fatalf("registration after passkeys were turned off: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.passkey_credentials"); got[0] != "0" {
		t.Errorf("a disabled site stored a passkey: %v", got)
	}
}

// TestConsolePasswordSessionCannotSkipAPasskey: the database refuses a console password session for a user holding a
// passkey, so an older node that has not learned about passkeys cannot issue one during a rolling deployment.
func TestConsolePasswordSessionCannotSkipAPasskey(t *testing.T) {
	t.Parallel()
	x, s := passkeySite(t)
	storePasskey(t, x)
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	account := uuid.MustParse(texts(t, x.db, platform, "SELECT account_id::text FROM accounts.users WHERE email = 'someone@example.com'")[0])
	err := x.db.Tx(t.Context(), pg.AccountScope(account), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), ids.Replace(
			`INSERT INTO accounts.sessions (id, site_id, account_id, user_id, surface, token_hash, csrf_token, level,
			 factor_version, expires_at, idle_timeout)
			 SELECT :x, u.site_id, u.account_id, u.id, 'console', decode(repeat('11', 32), 'hex'), repeat('a', 32),
			 'password', u.factor_version, now() + interval '1 hour', interval '1 hour'
			 FROM accounts.users u WHERE u.email = 'someone@example.com'`))
		return err
	})
	if state(err) != "23514" {
		t.Fatalf("a console password session for a user with a passkey: %v", err)
	}
}
