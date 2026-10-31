package accounts_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/pgtest"
)

// advance moves the test clock by one TOTP step and reloads the snapshot, whose data goes stale after three reload
// intervals.
func (x site) advance(t *testing.T) {
	t.Helper()
	x.clock.Advance(30 * time.Second)
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// mustPrincipal authenticates issued at host, failing the test when it cannot.
func mustPrincipal(t *testing.T, x site, host string, issued accounts.Issued) accounts.Principal {
	t.Helper()
	p, err := x.principal(t, host, issued)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// signInWith signs a user in at host from ip with userAgent, as a browser would.
func signInWith(t *testing.T, x site, host, email string, ip netip.Addr, userAgent string) accounts.Issued {
	t.Helper()
	issued, err := x.s.SignIn(t.Context(), accounts.SignInRequest{
		Host: host, Email: email, Password: password, ClientIP: ip, UserAgent: userAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

// TestClientSummary: the summary names a known browser and platform from the fixed tokens and never echoes anything
// else; the order handles agents that name several browsers or platforms.
func TestClientSummary(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36":                         "Chrome on Windows",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0":           "Edge on Windows",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 OPR/117.0.0.0":                     "Opera on Linux",
		"Mozilla/5.0 (Android 14; Mobile; rv:120.0) Gecko/120.0 Firefox/120.0":                                                                    "Firefox on Android",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Mobile/15E148 Safari/604.1": "Safari on iOS",
		"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36":                          "Chrome on ChromeOS",
		"Mozilla/5.0 (iPad; CPU OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/131.0.0.0 Mobile/15E148 Safari/604.1":       "Chrome on iOS",
		"curl/8.5.0":             "curl",
		"python-requests/2.31.0": "Python requests",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36": "Chrome on macOS",
		"":                          "Unknown client",
		"<script>alert(1)</script>": "Unknown client",
	}
	for ua, want := range cases {
		if got := accounts.ClientSummary(ua); got != want {
			t.Errorf("%q: %q, want %q", ua, got, want)
		}
	}
}

// TestIPGroup: the list shows the group the sign-in limits use, not the address; an address it cannot read is empty.
func TestIPGroup(t *testing.T) {
	t.Parallel()
	for addr, want := range map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"::ffff:203.0.113.7":   "203.0.113.7",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::",
		"2001:db8:1:2::":       "2001:db8:1:2::",
		"":                     "",
		"not-an-address":       "",
	} {
		if got := accounts.IPGroup(addr); got != want {
			t.Errorf("%q: %q, want %q", addr, got, want)
		}
	}
}

// deviceView is the part of a device the summary and the current mark decide; IDs and times are checked apart.
type deviceView struct {
	Current bool
	IPGroup string
	Client  string
}

func deviceViews(devices []accounts.SessionDevice) []deviceView {
	views := make([]deviceView, 0, len(devices))
	for _, device := range devices {
		views = append(views, deviceView{Current: device.Current, IPGroup: device.IPGroup, Client: device.Client})
	}
	return views
}

// TestSessionDevices: the list shows this user's live console sessions newest first, marks the asking one, groups the
// address as sign-in does, summarizes the User-Agent, and leaves another surface's session out.
func TestSessionDevices(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	chrome := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	first := signInWith(t, x, console, x.email, netip.MustParseAddr("2001:db8:1:2:3:4:5:6"), chrome)
	second := signInWith(t, x, console, x.email, netip.MustParseAddr("203.0.113.7"), "curl/8.5.0")
	signInWith(t, x, admin, x.email, netip.MustParseAddr("198.51.100.9"), "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Firefox/120.0")
	p := mustPrincipal(t, x, console, second)
	devices, err := x.s.Sessions(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("%d devices, want the two console sessions: %+v", len(devices), devices)
	}
	if devices[0].ID != p.SessionID() || devices[1].ID != first.SessionID() {
		t.Errorf("devices %v and %v, want the asking session and the first sign-in", devices[0].ID, devices[1].ID)
	}
	want := []deviceView{{IPGroup: "203.0.113.7", Client: "curl", Current: true}, {IPGroup: "2001:db8:1:2::", Client: "Chrome on macOS"}}
	if diff := cmp.Diff(want, deviceViews(devices)); diff != "" {
		t.Errorf("devices (-want +got):\n%s", diff)
	}
	if devices[0].CreatedAt.IsZero() || devices[0].LastSeenAt.Before(devices[0].CreatedAt) {
		t.Errorf("times of the newest session: %+v", devices[0])
	}
}

// TestSessionDevicesLeaveOutInvalid: an expired session and one the user ends disappear from the list and stop
// authenticating.
func TestSessionDevicesLeaveOutInvalid(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	first := signInWith(t, x, console, x.email, netip.MustParseAddr("203.0.113.1"), "")
	second := signInWith(t, x, console, x.email, netip.MustParseAddr("203.0.113.2"), "")
	p := mustPrincipal(t, x, console, second)
	third := signInWith(t, x, console, x.email, netip.MustParseAddr("203.0.113.8"), "")
	if err := pgtest.Run(t.Context(), x.database.Superuser,
		"UPDATE accounts.sessions SET expires_at = now() - interval '1 minute' WHERE id = '"+uuidString(third.SessionID())+"'"); err != nil {
		t.Fatal(err)
	}
	if err := x.s.RevokeSession(t.Context(), p, first.SessionID()); err != nil {
		t.Fatal(err)
	}
	devices, err := x.s.Sessions(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].ID != p.SessionID() {
		t.Errorf("devices after ending one and expiring one: %+v", devices)
	}
	if _, err := x.principal(t, console, first); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("the ended session: %v", err)
	}
}

// TestRevokeSession: one session can be ended by its owner and only its owner; the asking session, another user's,
// another surface's and one already ended are the same 404, and each change leaves an audit entry.
func TestRevokeSession(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	first := signInWith(t, x, console, x.email, netip.MustParseAddr("203.0.113.1"), "")
	second := signInWith(t, x, console, x.email, netip.MustParseAddr("203.0.113.2"), "")
	adminSession := signInWith(t, x, admin, x.email, netip.MustParseAddr("203.0.113.3"), "")
	p := mustPrincipal(t, x, console, second)
	if _, err := x.s.Register(t.Context(), accounts.RegisterRequest{
		Host: console, InviteCode: inviteCode(t, x.db, ":u1"), Email: "other@example.com", Password: password,
	}); err != nil {
		t.Fatal(err)
	}
	otherSession := signInWith(t, x, console, "other@example.com", netip.MustParseAddr("203.0.113.4"), "")
	otherPrincipal := mustPrincipal(t, x, console, otherSession)

	for name, id := range map[string]accounts.SessionID{
		"the asking session": p.SessionID(),
		"another user's":     otherPrincipal.SessionID(),
		"another surface's":  adminSession.SessionID(),
		"a nonexistent ID":   {},
	} {
		if err := x.s.RevokeSession(t.Context(), p, id); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("%s: %v, want %v", name, err, accounts.ErrNotFound)
		}
	}
	mustPrincipal(t, x, console, first)
	mustPrincipal(t, x, admin, adminSession)
	mustPrincipal(t, x, console, otherSession)
	if err := x.s.RevokeSession(t.Context(), p, first.SessionID()); err != nil {
		t.Fatal(err)
	}
	if _, err := x.principal(t, console, first); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("the ended session: %v", err)
	}
	if err := x.s.RevokeSession(t.Context(), p, first.SessionID()); !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("ending the same session again: %v", err)
	}
	if got := texts(t, x.db, platform,
		"SELECT action || ' ' || target_id || ' ' || actor_id FROM platform.audit_log WHERE action = 'session.revoke'"); len(got) != 1 ||
		got[0] != "session.revoke "+first.SessionID().TypeID()+" "+uuidString(x.user) {
		t.Errorf("the audit entry: %q", got)
	}
}

// TestRevokeOtherSessions: every other console session ends, pending sign-ins included, the asking session and the
// admin's stay, the count is exact, and a second call changes nothing.
func TestRevokeOtherSessions(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	first := signInWith(t, x, console, x.email, netip.MustParseAddr("203.0.113.1"), "")
	second := signInWith(t, x, console, x.email, netip.MustParseAddr("203.0.113.2"), "")
	adminSession := signInWith(t, x, admin, x.email, netip.MustParseAddr("203.0.113.3"), "")
	p, err := x.principal(t, console, second)
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := x.s.RevokeOtherSessions(t.Context(), p)
	if err != nil || revoked != 1 {
		t.Fatalf("revoked %d, %v, want one", revoked, err)
	}
	if _, err := x.principal(t, console, first); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("the other session: %v", err)
	}
	if _, err := x.principal(t, console, second); err != nil {
		t.Errorf("the asking session: %v", err)
	}
	if _, err := x.principal(t, admin, adminSession); err != nil {
		t.Errorf("the admin session: %v", err)
	}
	if revoked, err := x.s.RevokeOtherSessions(t.Context(), p); err != nil || revoked != 0 {
		t.Errorf("a second call revoked %d, %v, want none", revoked, err)
	}
	if got := texts(t, x.db, platform,
		"SELECT action || ' ' || target_id || ' ' || (detail->>'revoked') FROM platform.audit_log WHERE action = 'session.revoke_others'"); len(got) != 1 ||
		got[0] != "session.revoke_others "+x.user.TypeID()+" 1" {
		t.Errorf("the audit entry: %q", got)
	}
}

// TestRevokeOtherSessionsEndsPending: a sign-in that has the password but not the second factor is not listed, and
// ending the other sessions ends it too.
func TestRevokeOtherSessionsEndsPending(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	key, mfa, _ := x.enableTOTP(t, console, x.signIn(t, console))
	p, err := x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	pending := signInWith(t, x, console, x.email, netip.MustParseAddr("203.0.113.5"), "")
	if !pending.Pending {
		t.Fatal("the sign-in of a user with two-step verification is not pending")
	}
	if devices, err := x.s.Sessions(t.Context(), p); err != nil || len(devices) != 1 {
		t.Errorf("the list with a pending sign-in: %+v, %v", devices, err)
	}
	if err := x.s.RevokeSession(t.Context(), p, pending.SessionID()); !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("ending a pending sign-in by ID: %v", err)
	}
	if revoked, err := x.s.RevokeOtherSessions(t.Context(), p); err != nil || revoked != 1 {
		t.Fatalf("revoked %d, %v, want the pending sign-in", revoked, err)
	}
	x.advance(t)
	if _, err := x.verify(t, console, pending, accounts.TOTPCode(key, x.clock.Now())); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("a right code of the ended pending sign-in: %v", err)
	}
}

// TestReplaceTOTPWithCurrentCode: a current TOTP code lets the user put a new secret in place; the old secret, all old
// recovery codes and the old session end, and the asking device gets a working session.
func TestReplaceTOTPWithCurrentCode(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	key, mfa, codes := x.enableTOTP(t, console, x.signIn(t, console))
	p := mustPrincipal(t, x, console, mfa)
	enabledAt := texts(t, x.db, platform, "SELECT mfa_enabled_at::text FROM accounts.users WHERE id = '"+uuidString(x.user)+"'")
	secret, _ := x.s.BeginTOTP(p)
	x.advance(t)
	issued, renewed, err := x.s.ReplaceTOTP(t.Context(), p, accounts.FactorChange{
		Secret: secret, CurrentCode: accounts.TOTPCode(key, x.clock.Now()), Code: accounts.TOTPCode(secret, x.clock.Now()),
	})
	if err != nil || len(renewed) != 10 {
		t.Fatalf("replace with a current code: %v, %d codes", err, len(renewed))
	}
	if _, err := x.principal(t, console, mfa); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("the session that replaced the factor: %v", err)
	}
	if _, err := x.principal(t, console, issued); err != nil {
		t.Errorf("the session ReplaceTOTP issued: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT mfa_enabled_at::text FROM accounts.users WHERE id = '"+uuidString(x.user)+"'"); got[0] != enabledAt[0] {
		t.Errorf("mfa_enabled_at %s after the replacement, was %s", got[0], enabledAt[0])
	}
	x.advance(t)
	if _, err := x.verify(t, console, x.signIn(t, console), accounts.TOTPCode(key, x.clock.Now())); !errors.Is(err, accounts.ErrBadCode) {
		t.Errorf("a code of the old secret: %v", err)
	}
	if _, err := x.verify(t, console, x.signIn(t, console), codes[0]); !errors.Is(err, accounts.ErrBadCode) {
		t.Errorf("an old recovery code: %v", err)
	}
}

// TestReplaceTOTPWithRecoveryCode: an unused recovery code is proof enough for a second replacement; it and the other
// old recovery codes stop working, the new secret works, and each change leaves an audit entry.
func TestReplaceTOTPWithRecoveryCode(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	key, mfa, _ := x.enableTOTP(t, console, x.signIn(t, console))
	p := mustPrincipal(t, x, console, mfa)
	firstSecret, _ := x.s.BeginTOTP(p)
	x.advance(t)
	issued, renewed, err := x.s.ReplaceTOTP(t.Context(), p, accounts.FactorChange{
		Secret: firstSecret, CurrentCode: accounts.TOTPCode(key, x.clock.Now()), Code: accounts.TOTPCode(firstSecret, x.clock.Now()),
	})
	if err != nil {
		t.Fatal(err)
	}
	p = mustPrincipal(t, x, console, issued)
	nextSecret, _ := x.s.BeginTOTP(p)
	x.advance(t)
	issuedAgain, renewedAgain, err := x.s.ReplaceTOTP(t.Context(), p, accounts.FactorChange{
		Secret: nextSecret, CurrentCode: renewed[0], Code: accounts.TOTPCode(nextSecret, x.clock.Now()),
	})
	if err != nil || len(renewedAgain) != 10 {
		t.Fatalf("replace with a recovery code: %v, %d codes", err, len(renewedAgain))
	}
	if _, err := x.verify(t, console, x.signIn(t, console), renewed[1]); !errors.Is(err, accounts.ErrBadCode) {
		t.Errorf("a recovery code replaced by the second change: %v", err)
	}
	x.advance(t)
	if _, err := x.verify(t, console, x.signIn(t, console), accounts.TOTPCode(nextSecret, x.clock.Now())); err != nil {
		t.Errorf("a code of the secret from the second change: %v", err)
	}
	if issuedAgain.SessionID() == issued.SessionID() {
		t.Errorf("the second replacement reused the session %v", issued.SessionID())
	}
	got := texts(t, x.db, platform,
		"SELECT action || ' ' || target_id || ' ' || actor_kind || ':' || actor_id FROM platform.audit_log WHERE action = 'user.totp_replace'")
	if len(got) != 2 || got[0] != "user.totp_replace "+x.user.TypeID()+" user:"+uuidString(x.user) {
		t.Errorf("the audit entries: %q", got)
	}
}

// TestReplaceTOTPRefusals: a wrong current code, a wrong new code, a malformed secret and a user without the factor
// each change nothing, and only the two wrong codes spend a code failure.
func TestReplaceTOTPRefusals(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	key, mfa, _ := x.enableTOTP(t, console, x.signIn(t, console))
	p, err := x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	version := x.factorVersion(t)
	secret, _ := x.s.BeginTOTP(p)
	x.clock.Advance(30 * time.Second)
	code := accounts.TOTPCode(secret, x.clock.Now())
	for name, c := range map[string]struct {
		request accounts.FactorChange
		want    error
	}{
		"a wrong current code": {accounts.FactorChange{Secret: secret, CurrentCode: "000000", Code: code}, accounts.ErrBadCode},
		"a wrong new code":     {accounts.FactorChange{Secret: secret, CurrentCode: accounts.TOTPCode(key, x.clock.Now()), Code: "000000"}, accounts.ErrBadCode},
		"a malformed secret":   {accounts.FactorChange{Secret: "not base32", CurrentCode: accounts.TOTPCode(key, x.clock.Now()), Code: code}, accounts.ErrBadSecret},
	} {
		if _, _, err := x.s.ReplaceTOTP(t.Context(), p, c.request); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if got := x.factorVersion(t); got != version {
		t.Errorf("factor version %s after the refusals, was %s", got, version)
	}
	if _, err := x.principal(t, console, mfa); err != nil {
		t.Errorf("the session after the refusals: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'user.totp_replace'"); got[0] != "0" {
		t.Errorf("%s audit entries after the refusals", got[0])
	}

	y := newTestSite(t)
	q, err := y.principal(t, console, y.signIn(t, console))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := y.s.BeginTOTP(q)
	if _, _, err := y.s.ReplaceTOTP(t.Context(), q, accounts.FactorChange{
		Secret: other, CurrentCode: "123456", Code: accounts.TOTPCode(other, y.clock.Now()),
	}); !errors.Is(err, accounts.ErrTOTPOff) {
		t.Errorf("replacing a factor that is off: %v", err)
	}
}

// TestReplaceTOTPAuditIsAtomic: an audit entry the database refuses undoes the new secret, the recovery codes and the
// session with it, so the old factor still works.
func TestReplaceTOTPAuditIsAtomic(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	key, mfa, codes := x.enableTOTP(t, console, x.signIn(t, console))
	p, err := x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser,
		`ALTER TABLE platform.audit_log ADD CONSTRAINT reject_replace_audit CHECK (action <> 'user.totp_replace') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	version := x.factorVersion(t)
	secret, _ := x.s.BeginTOTP(p)
	x.clock.Advance(30 * time.Second)
	if _, _, err := x.s.ReplaceTOTP(t.Context(), p, accounts.FactorChange{
		Secret: secret, CurrentCode: accounts.TOTPCode(key, x.clock.Now()), Code: accounts.TOTPCode(secret, x.clock.Now()),
	}); err == nil {
		t.Fatal("the replacement committed without its audit entry")
	}
	if got := x.factorVersion(t); got != version {
		t.Errorf("factor version %s after the refused audit, was %s", got, version)
	}
	if _, err := x.principal(t, console, mfa); err != nil {
		t.Errorf("the session after the refused audit: %v", err)
	}
	if _, err := x.verify(t, console, x.signIn(t, console), codes[0]); err != nil {
		t.Errorf("a recovery code after the refused audit: %v", err)
	}
}

// TestReplaceTOTPAtOnce: two replacements with the same proof give one, and the factor version rises once.
func TestReplaceTOTPAtOnce(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	key, mfa, _ := x.enableTOTP(t, console, x.signIn(t, console))
	p, err := x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	version := x.factorVersion(t)
	x.clock.Advance(30 * time.Second)
	code := accounts.TOTPCode(key, x.clock.Now())
	results := make(chan error, 2)
	for range 2 {
		go func() {
			secret, _ := x.s.BeginTOTP(p)
			_, _, err := x.s.ReplaceTOTP(context.WithoutCancel(t.Context()), p, accounts.FactorChange{
				Secret: secret, CurrentCode: code, Code: accounts.TOTPCode(secret, x.clock.Now()),
			})
			results <- err
		}()
	}
	won := 0
	for range 2 {
		if err := <-results; err == nil {
			won++
		} else if !errors.Is(err, accounts.ErrBadCredentials) && !errors.Is(err, accounts.ErrBadCode) {
			t.Errorf("the losing replacement: %v", err)
		}
	}
	if won != 1 {
		t.Errorf("%d of two replacements at once succeeded", won)
	}
	if got := x.factorVersion(t); got == version {
		t.Errorf("factor version %s after one replacement, was %s", got, version)
	}
}

// uuidString spells a typed ID as the database stores it.
func uuidString[ID ~[16]byte](id ID) string { return uuid.UUID(id).String() }
