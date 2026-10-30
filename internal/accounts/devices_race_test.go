package accounts_test

import (
	"context"
	"net/netip"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// holdSecondFactorCommit holds the user lock and inserts the session a second-factor sign-in would issue, then waits
// for release: the caller starts a bulk revoke between the insert and the commit.
func holdSecondFactorCommit(ctx context.Context, holder *pg.DB, user uuid.UUID, pending accounts.SessionID, newSession uuid.UUID, ready, release chan struct{}) error {
	return holder.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT 1 FROM accounts.users WHERE id = $1 FOR UPDATE", user); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE accounts.sessions SET revoked_at = now() WHERE id = $1", uuid.UUID(pending)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO accounts.sessions (id, site_id, account_id, user_id, surface,
			token_hash, csrf_token, level, factor_version, expires_at, idle_timeout, ip, user_agent)
			SELECT $1, u.site_id, u.account_id, u.id, 'console', sha256(convert_to($2, 'UTF8')), md5($2),
				'mfa', u.factor_version, now() + interval '1 day', interval '1 hour', '203.0.113.21', 'Mozilla/5.0'
			FROM accounts.users u WHERE u.id = $3`, newSession, newSession.String(), user); err != nil {
			return err
		}
		close(ready)
		<-release
		return nil
	})
}

// waitedForLock reports whether a query of this database waits for a lock within ten seconds.
func waitedForLock(t *testing.T, watcher *pg.DB) bool {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if locksWaitedFor(t, watcher) > 0 {
			return true
		}
	}
	return false
}

// TestRevokeOtherSessionsWithMFACommit: a bulk revoke that starts while a second-factor sign-in holds the user lock
// must end the session that sign-in inserts after the bulk's statement snapshot, not return success with it alive.
func TestRevokeOtherSessionsWithMFACommit(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	_, mfa, _ := x.enableTOTP(t, console, x.signIn(t, console))
	p := mustPrincipal(t, x, console, mfa)
	pending := signInWith(t, x, console, x.email, netip.MustParseAddr("203.0.113.20"), "")
	watcher, holder := x.admin(t), x.admin(t)
	newSession := uuid.NewV7()
	ready, release := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- holdSecondFactorCommit(t.Context(), holder, uuid.UUID(x.user), pending.SessionID(), newSession, ready, release)
	}()
	select {
	case <-ready:
	case err := <-held:
		t.Fatalf("the holder transaction: %v", err)
	}
	revoking := make(chan struct {
		revoked int
		err     error
	}, 1)
	go func() {
		revoked, err := x.s.RevokeOtherSessions(t.Context(), p)
		revoking <- struct {
			revoked int
			err     error
		}{revoked, err}
	}()
	if !waitedForLock(t, watcher) {
		close(release)
		<-held
		t.Fatal("the bulk revoke did not wait for the user lock")
	}
	close(release)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	if got := <-revoking; got.err != nil || got.revoked != 1 {
		t.Fatalf("revoked %d, %v, want the session the sign-in inserted", got.revoked, got.err)
	}
	if got := texts(t, watcher, platform, "SELECT (revoked_at IS NOT NULL)::text FROM accounts.sessions WHERE id = '"+uuidString(newSession)+"'"); got[0] != "true" {
		t.Errorf("the session inserted by the sign-in has revoked_at %s, want set", got[0])
	}
}
