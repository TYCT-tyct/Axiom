package accounts_test

import (
	"errors"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/pgtest"
)

// TestALoosenedSiteDoesNotRevive: a session the site's shortened idle time has cut stays ended when the site lifts the
// limit again.
func TestALoosenedSiteDoesNotRevive(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	issued := x.signIn(t, console)
	setIdle := func(value string) {
		t.Helper()
		if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(`INSERT INTO accounts.site_settings (site_id, console_idle_timeout)
			VALUES (:s1, `+value+`) ON CONFLICT (site_id) DO UPDATE SET console_idle_timeout = excluded.console_idle_timeout`)); err != nil {
			t.Fatal(err)
		}
		if err := x.s.Reload(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, "UPDATE accounts.sessions SET last_seen_at = now() - interval '1 hour'"); err != nil {
		t.Fatal(err)
	}
	setIdle("interval '5 minutes'")
	if _, err := x.principal(t, console, issued); !errors.Is(err, accounts.ErrNoSession) {
		t.Fatalf("a session idle past the shortened limit: %v", err)
	}
	setIdle("NULL")
	if _, err := x.principal(t, console, issued); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("the same session after the site loosened its limit: %v", err)
	}
}
