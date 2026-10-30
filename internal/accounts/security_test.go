package accounts_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestAccountSuspensionAdmission(t *testing.T) {
	t.Parallel()
	db, _, a, key := callSetup(t)
	b := node(t, db, hashKeys(t, 1), nil)
	for _, s := range []*accounts.Service{a, b} {
		if err := call(t, s, s1, key, apiDomain); err != nil {
			t.Fatal(err)
		}
	}
	for _, suspended := range []bool{true, true, false} {
		inPlatform(t, db, func(tx pg.Tx) error {
			return a.SetAccountSuspended(t.Context(), tx, system, accounts.AccountID(a1), suspended, "review")
		})
		for _, s := range []*accounts.Service{a, b} {
			err := call(t, s, s1, key, apiDomain)
			if suspended && !errors.Is(err, accounts.ErrAccountSuspended) || !suspended && err != nil {
				t.Fatalf("suspended=%v: %v", suspended, err)
			}
		}
	}
	if got := texts(t, db, platform, `SELECT count(*)::text FROM platform.audit_log WHERE action='account.status_change'`)[0]; got != "2" {
		t.Fatal(got)
	}
}

func TestAccountTierChangeIsAuditedOnce(t *testing.T) {
	t.Parallel()
	db, _, a, _ := callSetup(t)
	if _, err := exec(t, db, platform, member(":s1", ":a1", "personal", ":u1", ":a1")); err != nil {
		t.Fatal(err)
	}
	pro := uuid.NewV7()
	if _, err := exec(t, db, platform, `INSERT INTO accounts.tiers(id,site_id,name) VALUES ('`+uuid.UUID(pro).String()+`','`+uuid.UUID(s1).String()+`','Pro')`); err != nil {
		t.Fatal(err)
	}
	inPlatform(t, db, func(tx pg.Tx) error {
		return a.SetAccountTier(t.Context(), tx, system, accounts.AccountID(a1), accounts.TierID(pro), "upgrade")
	})
	inPlatform(t, db, func(tx pg.Tx) error {
		return a.SetAccountTier(t.Context(), tx, system, accounts.AccountID(a1), accounts.TierID(pro), "same again")
	})
	var got accounts.AccountDetail
	inPlatform(t, db, func(tx pg.Tx) (err error) {
		got, err = a.Account(t.Context(), tx, accounts.AccountID(a1))
		return err
	})
	if got.Tier.ID != accounts.TierID(pro) || got.Tier.Name != "Pro" {
		t.Fatalf("tier is %+v", got.Tier)
	}
	if n := texts(t, db, platform, `SELECT count(*)::text FROM platform.audit_log WHERE action='account.tier_change'`)[0]; n != "1" {
		t.Fatalf("%s audit entries, want 1", n)
	}
	if detail := texts(t, db, platform, `SELECT (detail->>'before') || ' ' || (detail->>'after') FROM platform.audit_log WHERE action='account.tier_change'`)[0]; detail != "Standard Pro" {
		t.Fatalf("audit detail %q", detail)
	}
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return a.SetAccountTier(t.Context(), tx, system, accounts.AccountID(a1), accounts.TierID(t2), "another site")
	})
	var setting *accounts.SettingError
	if !errors.As(err, &setting) || setting.Field != "tier_id" {
		t.Fatalf("foreign tier: %v", err)
	}
	inPlatform(t, db, func(tx pg.Tx) (err error) {
		got, err = a.Account(t.Context(), tx, accounts.AccountID(a1))
		return err
	})
	if got.Tier.ID != accounts.TierID(pro) {
		t.Fatal("a refused change moved the account")
	}
}

func TestStatusAuditRollbackAndCallBoundary(t *testing.T) {
	t.Parallel()
	db, _, a, key := callSetup(t)
	b := node(t, db, hashKeys(t, 1), nil)
	inPlatform(t, db, func(tx pg.Tx) error {
		return a.SetUserSignInDisabled(t.Context(), tx, system, accounts.UserID(u1), true, "sign-in only")
	})
	if err := call(t, b, s1, key, apiDomain); err != nil {
		t.Fatal("user disable paused calls", err)
	}
	// A rolled-back audit must roll back the account change too.
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return a.SetAccountSuspended(t.Context(), tx, system, accounts.AccountID(a1), true, strings.Repeat("x", 1001))
	})
	if err == nil {
		t.Fatal("invalid audit committed")
	}
	if err := call(t, b, s1, key, apiDomain); err != nil {
		t.Fatal("audit failure changed admission", err)
	}
}

func TestDisableUserNeverRestoresSessions(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	_, first, codes := x.enableTOTP(t, console, x.signIn(t, console))
	var wg sync.WaitGroup
	results := make(chan error, 3)
	for range 3 {
		wg.Go(func() {
			_, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: password})
			results <- err
		})
	}
	inPlatform(t, x.db, func(tx pg.Tx) error { return x.s.SetUserSignInDisabled(t.Context(), tx, system, x.user, true, "pause") })
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, accounts.ErrBadCredentials) {
			t.Error(err)
		}
	}
	if _, err := x.principal(t, console, first); !errors.Is(err, accounts.ErrNoSession) {
		t.Fatal(err)
	}
	inPlatform(t, x.db, func(tx pg.Tx) error {
		return x.s.SetUserSignInDisabled(t.Context(), tx, system, x.user, false, "restore")
	})
	if _, err := x.principal(t, console, first); !errors.Is(err, accounts.ErrNoSession) {
		t.Fatal("old session revived", err)
	}
	if _, err := x.verify(t, console, x.signIn(t, console), codes[0]); err != nil {
		t.Fatal("restoring sign-in invalidated factors", err)
	}
	got := texts(t, x.db, platform, `SELECT count(*)::text FROM accounts.sessions WHERE user_id='`+uuid.UUID(x.user).String()+`' AND revoked_at IS NOT NULL`)[0]
	if got == "0" {
		t.Fatal("no sessions revoked")
	}
}

func TestSecurityEventsAndSharedFailures(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	other := node(t, x.db, hashKeys(t, 1), x.clock.Now)
	for _, s := range []*accounts.Service{x.s, other} {
		_, err := s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: "private-wrong-password", UserAgent: "private-agent", ClientIP: client})
		if !errors.Is(err, accounts.ErrBadCredentials) {
			t.Fatal(err)
		}
	}
	_, err := x.s.Register(t.Context(), accounts.RegisterRequest{Host: console, InviteCode: "private-invite", Email: "private@example.com", Password: password})
	if !errors.Is(err, accounts.ErrInvalidInvite) {
		t.Fatal(err)
	}
	issued := x.signIn(t, console)
	_, mfa, codes := x.enableTOTP(t, console, issued)
	if _, err := x.principal(t, console, mfa); err != nil {
		t.Fatal(err)
	}
	pending := x.signIn(t, console)
	if _, err := x.verify(t, console, pending, "private-code"); !errors.Is(err, accounts.ErrBadCode) {
		t.Fatal(err)
	}
	if _, err := x.verify(t, console, pending, codes[0]); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(texts(t, x.db, platform, `SELECT row_to_json(a)::text FROM platform.audit_log a WHERE action LIKE 'security.%'`), "\n")
	for _, secret := range []string{strings.ToLower(x.email), "private", client.String(), issued.Token, codes[0]} {
		if strings.Contains(all, secret) {
			t.Errorf("security event leaked %q", secret)
		}
	}
	for _, action := range []string{"security.registration", "security.registration_failed", "security.sign_in", "security.sign_in_failed", "security.mfa_failed", "security.mfa"} {
		if !strings.Contains(all, action) {
			t.Error("missing event", action)
		}
	}
}

func TestSecuritySharedFailureCounts(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	other := node(t, x.db, hashKeys(t, 1), x.clock.Now)
	for _, service := range []*accounts.Service{x.s, other} {
		_, err := service.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: "wrong"})
		if !errors.Is(err, accounts.ErrBadCredentials) {
			t.Fatal(err)
		}
	}
	if got := texts(t, x.db, platform, `SELECT (failures+completed_peak)::text FROM accounts.security_failures`)[0]; got != "2" {
		t.Fatalf("shared failures=%s want 2", got)
	}
	if got := texts(t, x.db, platform, `SELECT count(*)::text FROM platform.alert_signals`)[0]; got != "0" {
		t.Fatal("request persisted a signal", got)
	}
}

func TestSecurityCompletedWindows(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	other := node(t, x.db, hashKeys(t, 1), x.clock.Now)
	if _, err := exec(t, x.db, platform, ids.Replace(`INSERT INTO accounts.security_failures(site_id,failures) VALUES(:s1,2)`)); err != nil {
		t.Fatal(err)
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, `UPDATE accounts.security_failures SET started_at=date_trunc('minute',now())-interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	// The first failure in a new window cannot erase the completed window.
	_, err := other.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: "wrong"})
	if !errors.Is(err, accounts.ErrBadCredentials) {
		t.Fatal(err)
	}
	// Keep this fixture's unfinished window open across a real clock boundary.
	if err := pgtest.Run(t.Context(), x.database.Superuser, `UPDATE accounts.security_failures SET started_at=now()+interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	sample := func() {
		t.Helper()
		inPlatform(t, x.db, func(tx pg.Tx) error { return accounts.SampleSecurityAlerts(t.Context(), tx) })
	}
	sample()
	if got := texts(t, x.db, platform, `SELECT coalesce((SELECT body->'fields'->>'value' FROM platform.alert_signals ORDER BY seq DESC LIMIT 1),'missing')`)[0]; got != "2" {
		t.Fatal("completed window lost", got)
	}
	sample()
	if got := texts(t, x.db, platform, `SELECT count(*)::text FROM platform.alert_signals`)[0]; got != "1" {
		t.Fatal("duplicate sample", got)
	}
	if got := texts(t, x.db, platform, `SELECT failures::text FROM accounts.security_failures`)[0]; got != "1" {
		t.Fatal("current window lost", got)
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, `UPDATE accounts.security_failures SET started_at=now()-interval '2 minutes',sampled_at=now()-interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	sample()
	if got := texts(t, x.db, platform, `SELECT coalesce((SELECT body->'fields'->>'value' FROM platform.alert_signals ORDER BY seq DESC LIMIT 1),'missing')`)[0]; got != "1" {
		t.Fatal("second window lost", got)
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, `UPDATE accounts.security_failures SET started_at=now()-interval '2 minutes',sampled_at=now()-interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	sample()
	sample()
	if got := texts(t, x.db, platform, `SELECT count(*)::text FROM platform.alert_signals WHERE body->>'resolved'='true'`)[0]; got != "1" {
		t.Fatal("missing or duplicate recovery", got)
	}
}

func TestSecurityFailureScope(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	if _, err := exec(t, db, platform, ids.Replace(`INSERT INTO accounts.security_failures(site_id,failures) VALUES(:s1,1),(:s2,1)`)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		scope   pg.Scope
		visible string
		changed int64
	}{
		{platform, "2", 1},
		{pg.NewScope(false, []uuid.UUID{s1}, nil), "1", 0},
		{pg.NewScope(false, nil, []uuid.UUID{a1}), "0", 0},
		{pg.Scope{}, "0", 0},
	} {
		if got := texts(t, db, c.scope, `SELECT count(*)::text FROM accounts.security_failures`)[0]; got != c.visible {
			t.Fatal("scope read", got, c.visible)
		}
		if changed, err := exec(t, db, c.scope, ids.Replace(`UPDATE accounts.security_failures SET failures=failures+1 WHERE site_id=:s1`)); err != nil || changed != c.changed {
			t.Fatal("scope write", changed, err)
		}
	}
}
