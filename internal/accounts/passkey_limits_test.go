package accounts_test

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestPasskeyMFABudgetStopsBeforeWork: once the pending session has spent its challenge attempts, Begin creates no
// flow and Finish neither consumes the flow nor verifies the assertion. The session budget needs a new sign-in, so it
// is not a rate limit that would tell the page to wait.
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
	if _, err := s.FinishPasskeyMFA(t.Context(), r, p, second.FlowID, client, "test"); !errors.Is(err, accounts.ErrMFAExhausted) {
		t.Fatalf("a finish after the budget: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.webauthn_flows WHERE id = '"+second.FlowID+"' AND consumed_at IS NULL"); got[0] != "1" {
		t.Errorf("the flow was consumed after the budget: %v", got)
	}
	assertPasskeyFailures(t, x, "1")

	if _, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), p, client); !errors.Is(err, accounts.ErrMFAExhausted) {
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
	if _, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), p, limited); err == nil {
		t.Error("begin with the address budget spent succeeded")
	} else {
		requireRateLimit(t, err, "begin with the address budget spent")
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.webauthn_flows")[0]; got != before {
		t.Errorf("flows after a refused begin: %s, want %s", got, before)
	}
	r := passkeyRequest(t, auth.assert(t, flow.PublicKey))
	r.AddCookie(cookie)
	if _, err := s.FinishPasskeyMFA(t.Context(), r, p, flow.FlowID, limited, "test"); err == nil {
		t.Error("finish with the address budget spent succeeded")
	} else {
		requireRateLimit(t, err, "finish with the address budget spent")
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.webauthn_flows WHERE id = '"+flow.FlowID+"' AND consumed_at IS NULL"); got[0] != "1" {
		t.Errorf("the flow was consumed after the address budget: %v", got)
	}
	assertPasskeyFailures(t, x, "0")
}

// TestPasskeyMFABeginSharesTheBudgetLock: a finish cannot spend the last attempt while a begin that passed its
// budget check is still writing its flow. An advisory lock in an insert-only trigger holds the begin inside its
// transaction; the finish must wait on the same user lock rather than completing while the flow row is written.
func TestPasskeyMFABeginSharesTheBudgetLock(t *testing.T) {
	t.Parallel()
	x, s := passkeySite(t)
	auth := newSoftAuthenticator(t)
	registerPasskeyForTest(t, x, s, auth)
	p, cookie := pendingPasskeySignIn(t, x, s)
	setPasskeyLimits(t, x, s, accounts.DefaultLoginLimits(), func(l *accounts.LoginLimits) {
		one := int32(1)
		l.ChallengeCodeAttempts = &one
	})

	// The finish's flow exists before the trigger, so only the begin's insert is held.
	flow, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), p, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser,
		`CREATE FUNCTION accounts.hold_mfa_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN PERFORM pg_advisory_xact_lock(613); RETURN NEW; END $$`,
		`CREATE TRIGGER hold_mfa_insert BEFORE INSERT ON accounts.webauthn_flows
		 FOR EACH ROW WHEN (NEW.kind = 'mfa') EXECUTE FUNCTION accounts.hold_mfa_insert()`); err != nil {
		t.Fatal(err)
	}
	admin, release := lockAdvisory(t, x, 613)
	defer release()

	beginResult := make(chan error, 1)
	go func() {
		_, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), p, client)
		beginResult <- err
	}()
	awaitWaitingQuery(t, admin, "%webauthn_flows%", 1)

	bad := reSign(t, auth.assert(t, flow.PublicKey), func(signature []byte) []byte {
		signature[len(signature)-1] ^= 0xff
		return signature
	})
	r := passkeyRequest(t, bad)
	r.AddCookie(cookie)
	finishResult := finishMFAAsync(t.Context(), s, r, p, flow.FlowID)
	early, finished := awaitFinishOrWaiting(t, admin, finishResult)
	if finished {
		t.Errorf("the finish completed while the begin still held the budget lock: %v", early.err)
	}
	release()

	if err := <-beginResult; err != nil {
		t.Errorf("the begin held at its insert: %v", err)
	}
	if !finished {
		early = <-finishResult
	}
	if !errors.Is(early.err, accounts.ErrPasskeyFailed) {
		t.Errorf("the finish that spent the attempt after the begin: %v, want a failed assertion", early.err)
	}
	if got := texts(t, x.db, platform, "SELECT mfa_attempts::text FROM accounts.sessions WHERE id = '"+uuid.UUID(p.SessionID()).String()+"'"); got[0] != "1" {
		t.Errorf("attempts after the held begin and finish: %v", got)
	}
}

// TestPasskeyMFAConcurrentFinishesShareOneAttempt: with one attempt left, two finishes cannot both consume their
// flows. The first is held on its flow row after its budget check, while the second starts; the first must still win
// the attempt, and the second must see the budget spent before it consumes its own flow.
func TestPasskeyMFAConcurrentFinishesShareOneAttempt(t *testing.T) {
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
	bad := func(flow accounts.PasskeyChallenge) *http.Request {
		assertion := reSign(t, auth.assert(t, flow.PublicKey), func(signature []byte) []byte {
			signature[len(signature)-1] ^= 0xff
			return signature
		})
		r := passkeyRequest(t, assertion)
		r.AddCookie(cookie)
		return r
	}

	admin, release := lockFlow(t, x, first.FlowID)
	defer release()
	firstResult := finishMFAAsync(t.Context(), s, bad(first), p, first.FlowID)
	awaitWaitingQuery(t, admin, "%webauthn_flows%", 1)
	secondResult := finishMFAAsync(t.Context(), s, bad(second), p, second.FlowID)
	early, finished := awaitFinishOrWaiting(t, admin, secondResult)
	release()

	firstOutcome := <-firstResult
	secondOutcome := early
	if !finished {
		secondOutcome = <-secondResult
	}
	if !errors.Is(firstOutcome.err, accounts.ErrPasskeyFailed) {
		t.Errorf("the finish that held the flow: %v, want a failed assertion", firstOutcome.err)
	}
	if !errors.Is(secondOutcome.err, accounts.ErrMFAExhausted) {
		t.Errorf("the finish that lost the attempt: %v, want the session budget", secondOutcome.err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.webauthn_flows WHERE id = '"+second.FlowID+"' AND consumed_at IS NULL"); got[0] != "1" {
		t.Errorf("the losing finish consumed its flow: %v", got)
	}
	if got := texts(t, x.db, platform, "SELECT mfa_attempts::text FROM accounts.sessions WHERE id = '"+uuid.UUID(p.SessionID()).String()+"'"); got[0] != "1" {
		t.Errorf("attempts after two finishes with one left: %v", got)
	}
	assertPasskeyFailures(t, x, "1")
}

type finishOutcome struct{ err error }

func finishMFAAsync(ctx context.Context, s *accounts.Service, r *http.Request, p accounts.Principal, flowID string) <-chan finishOutcome {
	out := make(chan finishOutcome, 1)
	go func() {
		_, err := s.FinishPasskeyMFA(ctx, r, p, flowID, client, "test")
		out <- finishOutcome{err: err}
	}()
	return out
}

// lockFlow holds one flow row on a superuser connection until release, which is also a cleanup so a test failure
// cannot leave the transaction open and block admin.Close.
func lockFlow(t *testing.T, x site, flowID string) (*pg.DB, func()) {
	t.Helper()
	admin, err := pg.Open(t.Context(), x.database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	locked, unlock, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- admin.Tx(context.WithoutCancel(t.Context()), pg.Scope{}, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "SELECT 1 FROM accounts.webauthn_flows WHERE id = $1::uuid FOR UPDATE", flowID); err != nil {
				return err
			}
			close(locked)
			<-unlock
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-done:
		t.Fatalf("lock the flow: %v", err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			close(unlock)
			if err := <-done; err != nil {
				t.Errorf("unlock the flow: %v", err)
			}
		})
	}
	t.Cleanup(release)
	return admin, release
}

// awaitWaitingQuery waits for count backends to block on a query matching pattern, and fails rather than guessing.
func awaitWaitingQuery(t *testing.T, admin *pg.DB, pattern string, count int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; {
		waiting, err := strconv.Atoi(waitingQuery(t, admin, pattern))
		if err != nil {
			t.Fatal(err)
		}
		if waiting >= count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fewer than %d backends waited on %q", count, pattern)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitFinishOrWaiting reports a finish that completed before the other released its flow, or waits until this finish
// is blocked on the user lock. It fails on a timeout instead of hanging.
func awaitFinishOrWaiting(t *testing.T, admin *pg.DB, result <-chan finishOutcome) (finishOutcome, bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		select {
		case out := <-result:
			return out, true
		default:
		}
		if waitingQuery(t, admin, "%accounts.users u WHERE u.id%") != "0" {
			return finishOutcome{}, false
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the second finish neither completed nor waited on the user lock")
	return finishOutcome{}, false
}

// lockAdvisory holds a transaction-level advisory lock on a superuser connection until release. Its cleanup runs
// before admin.Close, so a failed assertion cannot leave the transaction open.
func lockAdvisory(t *testing.T, x site, key int64) (*pg.DB, func()) {
	t.Helper()
	admin, err := pg.Open(t.Context(), x.database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	locked, unlock, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- admin.Tx(context.WithoutCancel(t.Context()), pg.Scope{}, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "SELECT pg_advisory_xact_lock($1)", key); err != nil {
				return err
			}
			close(locked)
			<-unlock
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-done:
		t.Fatalf("take advisory lock %d: %v", key, err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			close(unlock)
			if err := <-done; err != nil {
				t.Errorf("release advisory lock %d: %v", key, err)
			}
		})
	}
	t.Cleanup(release)
	return admin, release
}

func waitingQuery(t *testing.T, admin *pg.DB, pattern string) string {
	t.Helper()
	return texts(t, admin, pg.Scope{}, `SELECT count(*)::text FROM pg_stat_activity
		WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '`+pattern+`'`)[0]
}

// requireRateLimit checks that err is a source-address limit with a wait the page can show.
func requireRateLimit(t *testing.T, err error, what string) {
	t.Helper()
	var many *accounts.TooManyAttempts
	if !errors.As(err, &many) || many.RetryAfter <= 0 {
		t.Fatalf("%s: %v, want *accounts.TooManyAttempts with a positive RetryAfter", what, err)
	}
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
