package accounts_test

import (
	"errors"
	"net/netip"
	"regexp"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// events is s1's security events, oldest first, as one line each: the kind, how it ended, the reason, and whether it
// names a user, an account and a session.
func events(t *testing.T, db *pg.DB) []string {
	t.Helper()
	return texts(t, db, platform, ids.Replace(`SELECT format('%s %s %s user=%s account=%s session=%s', kind,
		CASE WHEN refused THEN 'refused' ELSE 'ok' END, coalesce(nullif(reason, ''), '-'),
		user_id IS NOT NULL, account_id IS NOT NULL, session_id IS NOT NULL)
		FROM accounts.security_events WHERE site_id = :s1 ORDER BY id`))
}

// hashes is s1's events' address hashes, oldest first.
func hashes(t *testing.T, db *pg.DB) []string {
	t.Helper()
	return texts(t, db, platform, ids.Replace(`SELECT ip_hash FROM accounts.security_events
		WHERE site_id = :s1 ORDER BY id`))
}

// TestEveryAttemptIsOneEvent: a registration, a sign-in and a two-step verification are each one row, refused or not,
// with the reason, and with the user, the account and the session the attempt learned. A refusal is one row, not two,
// and the answer to the attempt is the one the module gives without events.
func TestEveryAttemptIsOneEvent(t *testing.T) {
	t.Parallel()
	x := newTestSite(t) // the registration that made the user
	invite := inviteCode(t, x.db, ":u1")
	signIn := func(password, email string) error {
		_, err := x.s.SignIn(t.Context(), accounts.SignInRequest{
			Host: console, Email: email, Password: password, ClientIP: client, UserAgent: "test",
		})
		return err
	}
	register := func(email, password string) error {
		_, err := x.s.Register(t.Context(), accounts.RegisterRequest{
			Host: console, InviteCode: invite, Email: email, Password: password, ClientIP: client,
		})
		return err
	}
	for _, c := range []struct {
		name string
		err  error
		want error
	}{
		{"a wrong password", signIn("wrong password", x.email), accounts.ErrBadCredentials},
		{"an unknown email", signIn(password, "nobody@example.com"), accounts.ErrBadCredentials},
		{"a taken email", register(x.email, password), accounts.ErrEmailTaken},
		{"no such invite", register("new@example.com", password), accounts.ErrInvalidInvite},
		{"a malformed email", register("no at sign", password), accounts.ErrBadEmail},
		{"a short password", register("new@example.com", "short"), accounts.ErrWeakPassword},
	} {
		if !errors.Is(c.err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, c.err, c.want)
		}
	}
	key, _, codes := x.enableTOTP(t, console, x.signIn(t, console))
	pending := x.signIn(t, console) // a sign-in that waits for the code, so an event with its session
	if _, err := x.verify(t, console, pending, accounts.TOTPCode(key, x.clock.Now().Add(30*time.Second))); !errors.Is(err, accounts.ErrBadCode) {
		t.Errorf("a wrong code: %v", err)
	}
	if _, err := x.verify(t, console, pending, codes[0]); err != nil {
		t.Fatalf("a recovery code: %v", err)
	}
	want := []string{
		"sign_up ok - user=true account=true session=false",
		"sign_in refused bad_credentials user=true account=true session=false",
		"sign_in refused bad_credentials user=false account=false session=false",
		"sign_up refused email_taken user=false account=false session=false",
		"sign_up refused invalid_invite user=false account=false session=false",
		"sign_up refused bad_email user=false account=false session=false",
		"sign_up refused weak_password user=false account=false session=false",
		"sign_in ok - user=true account=true session=true",
		"sign_in ok - user=true account=true session=true", // the pending sign-in
		"mfa refused bad_code user=true account=true session=true",
		"mfa ok - user=true account=true session=true",
	}
	if diff := cmp.Diff(want, events(t, x.db)); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
}

// TestTooManyAttemptsIsAnEvent: once the email's failures are spent, the refusal is its own reason, with no user,
// because the attempt never got as far as looking one up.
func TestTooManyAttemptsIsAnEvent(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	for range 10 {
		_, _ = x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: "wrong", ClientIP: client}) //nolint:errcheck // refused, once each: the events below are the point
	}
	if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: "wrong", ClientIP: client}); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Fatalf("the eleventh attempt: %v, want too many attempts", err)
	}
	got := events(t, x.db)
	if last := got[len(got)-1]; last != "sign_in refused too_many_attempts user=false account=false session=false" {
		t.Errorf("the last event: %q", last)
	}
	if len(got) != 12 { // the registration, ten wrong passwords and the refusal
		t.Errorf("%d events, want 12", len(got))
	}
}

// TestEventsHoldNoPersonalData: an event keeps the address of the client only as a keyed hash — the same address as
// one hash, another address as another — and keeps neither the address, nor the email, nor the user agent.
func TestEventsHoldNoPersonalData(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	signIn := func(ip netip.Addr) {
		t.Helper()
		_, _ = x.s.SignIn(t.Context(), accounts.SignInRequest{ //nolint:errcheck // refused: what it stored is the point
			Host: console, Email: x.email, Password: "wrong", ClientIP: ip, UserAgent: "a very personal browser",
		})
	}
	before := hashes(t, x.db)
	signIn(client)
	once := hashes(t, x.db)
	if once[len(once)-1] != before[len(before)-1] {
		t.Errorf("the same address hashed as %q and then %q", before[len(before)-1], once[len(once)-1])
	}
	signIn(netip.MustParseAddr("198.51.100.9"))
	if twice := hashes(t, x.db); twice[len(twice)-1] == once[len(once)-1] {
		t.Errorf("another address hashed the same, %q", twice[len(twice)-1])
	}
	hash := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, h := range hashes(t, x.db) {
		if !hash.MatchString(h) {
			t.Errorf("an address stored as %q, want a keyed hash", h)
		}
	}
	// Nothing in a row is the address, the email or the user agent, as text of any column.
	personal := texts(t, x.db, platform, `SELECT count(*)::text FROM accounts.security_events
		WHERE row_to_json(security_events)::text ~ '203\.0\.113\.7|@example\.com|personal browser'`)
	if personal[0] != "0" {
		t.Errorf("%s events hold the client's address, its email or its user agent", personal[0])
	}
}

// TestEventsAreBestEffort: an event that cannot be written is no reason to change the answer to the attempt: the
// refusal is still the same refusal, and a sign-in still signs in.
func TestEventsAreBestEffort(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	if err := pgtest.Run(t.Context(), x.database.Superuser, "DROP POLICY inserts ON accounts.security_events"); err != nil {
		t.Fatal(err)
	}
	before := len(events(t, x.db))
	if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{
		Host: console, Email: x.email, Password: "wrong", ClientIP: client,
	}); !errors.Is(err, accounts.ErrBadCredentials) {
		t.Errorf("a refusal that cannot be recorded: %v", err)
	}
	if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{
		Host: console, Email: x.email, Password: password, ClientIP: client,
	}); err != nil {
		t.Errorf("a sign-in that cannot be recorded: %v", err)
	}
	if got := events(t, x.db); len(got) != before {
		t.Errorf("%d events, want the %d before", len(got), before)
	}
}
