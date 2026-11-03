package accounts_test

import (
	"errors"
	"net/netip"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// TestPasskeyMFABudgetStopsBeforeWork: once the pending session has spent its challenge attempts, Begin creates no
// flow and Finish neither consumes the flow nor verifies the assertion.
func TestPasskeyMFABudgetStopsBeforeWork(t *testing.T) {
	t.Parallel()
	x, s := passkeySite(t)
	auth := newSoftAuthenticator(t)
	registerPasskeyForTest(t, x, s, auth)
	p, cookie := pendingPasskeySignIn(t, x, s)
	setPasskeyLimits(t, x, s, accounts.DefaultLoginLimits(), func(l *accounts.LoginLimits) {
		one := int32(1)
		l.ChallengeCodeAttempts = &one
	})

	first, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), p, client)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), p, client)
	if err != nil {
		t.Fatal(err)
	}

	bad := reSign(t, auth.assert(t, first.PublicKey), func(signature []byte) []byte {
		signature[len(signature)-1] ^= 0xff
		return signature
	})
	r := passkeyRequest(t, bad)
	r.AddCookie(cookie)
	if _, err := s.FinishPasskeyMFA(t.Context(), r, p, first.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Fatalf("a wrong assertion: %v", err)
	}
	assertPasskeyFailures(t, x, "1")

	// The second flow is still live and its assertion would verify, but the budget is spent: Finish must refuse
	// before consuming the flow or verifying, and add no failure.
	before := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.webauthn_flows")[0]
	r = passkeyRequest(t, auth.assert(t, second.PublicKey))
	r.AddCookie(cookie)
	if _, err := s.FinishPasskeyMFA(t.Context(), r, p, second.FlowID, client, "test"); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Fatalf("a finish after the budget: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.webauthn_flows WHERE id = '"+second.FlowID+"' AND consumed_at IS NULL"); got[0] != "1" {
		t.Errorf("the flow was consumed after the budget: %v", got)
	}
	assertPasskeyFailures(t, x, "1")

	if _, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), p, client); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Errorf("begin after the budget: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.webauthn_flows")[0]; got != before {
		t.Errorf("flows after a refused begin: %s, want %s", got, before)
	}
}

// TestPasskeyMFAIPLimitStopsBeforeWork: once the source address has spent its attempts, Begin creates no flow and
// Finish does not touch the flow or verify.
func TestPasskeyMFAIPLimitStopsBeforeWork(t *testing.T) {
	t.Parallel()
	x, s := passkeySite(t)
	auth := newSoftAuthenticator(t)
	registerPasskeyForTest(t, x, s, auth)
	p, cookie := pendingPasskeySignIn(t, x, s)
	setPasskeyLimits(t, x, s, accounts.DefaultLoginLimits(), func(l *accounts.LoginLimits) {
		one := int32(1)
		l.IPAttemptBurst, l.IPAttemptsPerMinute = &one, &one
	})

	limited := netip.MustParseAddr("203.0.113.9")
	flow, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), p, limited)
	if err != nil {
		t.Fatal(err)
	}
	before := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.webauthn_flows")[0]
	if _, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), p, limited); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Errorf("begin with the address budget spent: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.webauthn_flows")[0]; got != before {
		t.Errorf("flows after a refused begin: %s, want %s", got, before)
	}
	r := passkeyRequest(t, auth.assert(t, flow.PublicKey))
	r.AddCookie(cookie)
	if _, err := s.FinishPasskeyMFA(t.Context(), r, p, flow.FlowID, limited, "test"); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Errorf("finish with the address budget spent: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.webauthn_flows WHERE id = '"+flow.FlowID+"' AND consumed_at IS NULL"); got[0] != "1" {
		t.Errorf("the flow was consumed after the address budget: %v", got)
	}
	assertPasskeyFailures(t, x, "0")
}

// setPasskeyLimits replaces the site's login limits, after mutate has set the fields the test needs, and reloads the
// snapshot. The defaults keep every other budget out of the way.
func setPasskeyLimits(t *testing.T, x site, s *accounts.Service, defaults accounts.LoginLimits, mutate func(*accounts.LoginLimits)) {
	t.Helper()
	mutate(&defaults)
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return s.SetSiteLoginLimits(t.Context(), tx, system, accounts.SiteID(s1), defaults, "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
}
