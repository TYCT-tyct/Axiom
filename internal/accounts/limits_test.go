package accounts_test

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
	"axiom/internal/platform/pgtest"
)

// TestSiteAttemptLimits: a site's own limits hold sign-ins and registrations per IP and failed sign-ins per email;
// waiting lifts them, a success does not count against the email, and other IPs and emails are not held up. The same
// with the counts in the shared store, in a namespace of its own: one node's worth of the site's limits, whatever holds
// them (internal/platform/kv/README.md「登录前的尝试次数」).
func TestSiteAttemptLimits(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(`INSERT INTO accounts.site_settings
		(site_id, ip_attempt_burst, ip_attempts_per_minute, email_sign_in_failures) VALUES (:s1, 3, 1, 2)`)); err != nil {
		t.Fatal(err)
	}
	namespace := kvtest.Namespace()
	server := kvtest.New(t)
	for _, mode := range []struct {
		name  string
		cache *kv.Client
	}{
		{"this node's buckets", nil},
		{"the shared store", server.OpenIn(t, namespace, kv.Deps{Now: clientClock(x.clock)})},
	} {
		s := x.s
		if mode.cache != nil {
			s = cachedNode(t, x.db, mode.cache, x.clock.Now)
		}
		t.Run(mode.name, func(t *testing.T) { attemptLimits(t, x.email, x.clock, s) })
	}
}

// attemptLimits is the table of TestSiteAttemptLimits, over the service that holds the counts.
func attemptLimits(t *testing.T, email string, clock *clock, s *accounts.Service) {
	t.Helper()
	ip := func(n byte) netip.Addr { return netip.AddrFrom4([4]byte{198, 51, 100, n}) }
	signIn := func(from netip.Addr, email, pw string) error {
		_, err := s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: email, Password: pw, ClientIP: from})
		return err
	}
	for i, c := range []struct {
		name    string
		advance time.Duration
		from    netip.Addr
		email   string
		pw      string
		want    error
	}{
		{"1st from an IP", 0, ip(1), email, password, nil},
		{"2nd from the IP", 0, ip(1), email, password, nil},
		{"3rd from the IP, a success not counted against the email", 0, ip(1), email, password, nil},
		{"4th from the IP", 0, ip(1), email, password, accounts.ErrTooManyAttempts},
		{"another IP", 0, ip(2), email, password, nil},
		{"the IP a minute later", time.Minute, ip(1), email, password, nil},
		{"a wrong password", 0, ip(3), email, "wrong password!", accounts.ErrBadCredentials},
		{"a second wrong password", 0, ip(4), email, "wrong password!", accounts.ErrBadCredentials},
		{"the right password after two failures", 0, ip(5), email, password, accounts.ErrTooManyAttempts},
		{"another email", 0, ip(5), "nobody@example.com", password, accounts.ErrBadCredentials},
		{"the right password once one failure has refilled", emailFailureInterval, ip(6), email, password, nil},
		{"1st from a fresh IP", 0, ip(7), email, password, nil},
		{"2nd from that IP", 0, ip(7), email, password, nil},
		{"3rd from that IP", 0, ip(7), email, password, nil},
		{"a registration from that IP", 0, ip(7), "", "", accounts.ErrTooManyAttempts},
	} {
		clock.Advance(c.advance)
		if err := s.Reload(t.Context()); err != nil { // as the reload loop keeps the snapshot fresh
			t.Fatal(err)
		}
		var err error
		if c.email == "" {
			_, err = s.Register(t.Context(), accounts.RegisterRequest{Host: console, InviteCode: "nosuchcode0000", Email: "new@example.com", Password: password, ClientIP: c.from})
		} else {
			err = signIn(c.from, c.email, c.pw)
		}
		if !errors.Is(err, c.want) || c.want == nil && err != nil {
			t.Errorf("%d %s: %v, want %v", i, c.name, err, c.want)
		}
	}
}

// emailFailureInterval is one refill under the tests' two-failure limit.
const emailFailureInterval = 15 * time.Minute / 2

// TestCodeFailuresSpanChallenges: wrong codes count against the user across pending sessions, so signing in again
// does not buy more guesses; at the default ten an hour even a right code waits, until one has refilled.
func TestCodeFailuresSpanChallenges(t *testing.T) {
	t.Parallel()
	for _, shared := range []bool{false, true} {
		name := "local"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) { codeFailuresSpanChallenges(t, shared) })
	}
}

func codeFailuresSpanChallenges(t *testing.T, shared bool) {
	t.Helper()
	x := newTestSite(t)
	if shared {
		x.s = cachedNode(t, x.db, kvtest.New(t).Open(t, kv.Deps{Now: clientClock(x.clock)}), x.clock.Now)
	}
	_, _, codes := x.enableTOTP(t, console, x.signIn(t, console))
	// Turning it on tried a wrong password and a wrong code, which count against the user as wrong codes do (2 of the
	// 10); two failures refill in 12 minutes.
	x.clock.Advance(2 * time.Hour / 10)
	if err := x.s.Reload(t.Context()); err != nil { // as the reload loop keeps the snapshot fresh
		t.Fatal(err)
	}
	for range 2 {
		challenge := x.signIn(t, console)
		for range 5 {
			if _, err := x.verify(t, console, challenge, "000000"); !errors.Is(err, accounts.ErrBadCode) {
				t.Fatalf("a wrong code: %v", err)
			}
		}
	}
	if _, err := x.verify(t, console, x.signIn(t, console), codes[0]); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Errorf("a right code in a third challenge after ten failures: %v, want %v", err, accounts.ErrTooManyAttempts)
	}
	x.clock.Advance(6 * time.Minute)
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := x.verify(t, console, x.signIn(t, console), codes[0]); err != nil {
		t.Errorf("a right code once one failure has refilled: %v", err)
	}
}

// TestHashingWaitsItsTurn: password hashes take turns, as many at once as the node allows; one that waits too long is
// refused as busy, one whose request ends while it waits stops with it, and turns come back when a hash is done.
func TestHashingWaitsItsTurn(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	const wait = 200 * time.Millisecond
	s, err := accounts.NewService(x.db, accounts.Config{
		Platform: platform, Hashes: hashKeys(t, 1), Keyring: keyring(t), Now: x.clock.Now, HashConcurrency: 1, HashWait: wait,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	release, err := s.HashTurn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: password}); !errors.Is(err, accounts.ErrBusy) || time.Since(start) < wait {
		t.Errorf("a sign-in while the only turn is taken: %v after %v, want %v after %v", err, time.Since(start), accounts.ErrBusy, wait)
	}
	ctx, cancel := context.WithTimeout(t.Context(), wait/4)
	defer cancel()
	if err := s.Hash(ctx, func() { t.Error("hashed without a turn") }); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a hash whose request ends while it waits: %v, want the request's own error", err)
	}
	release()
	if _, err := s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: password}); err != nil {
		t.Errorf("a sign-in once the turn is back: %v", err)
	}
	if _, err := s.Register(t.Context(), accounts.RegisterRequest{Host: console, InviteCode: inviteCode(t, x.db, ":u1"), Email: "second@example.com", Password: password}); err != nil {
		t.Errorf("a registration once the turn is back: %v", err)
	}
}
