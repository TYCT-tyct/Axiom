package accounts_test

import (
	"errors"
	"testing"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestALoosenedSiteDoesNotRevive: a site that shortens its sessions ends the ones the shorter lifetimes run out, for
// good: a session that makes no request while the site is strict is still ended after the site loosens again, by its
// idle time and by its age; a new sign-in gets the loosened lifetimes.
func TestALoosenedSiteDoesNotRevive(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		strict accounts.SiteSettings
	}{
		{"idle time", accounts.SiteSettings{ConsoleIdle: 30 * time.Minute}},
		{"age", accounts.SiteSettings{ConsoleMaxAge: time.Hour}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newTestSite(t)
			issued := x.signIn(t, console)
			x.setSite(t, c.strict)
			// Two hours pass with no request: the session is still within the strict lifetimes when they are set.
			if err := pgtest.Run(t.Context(), x.database.Superuser, `UPDATE accounts.sessions SET created_at = created_at - interval '2 hours',
				last_seen_at = last_seen_at - interval '2 hours', expires_at = expires_at - interval '2 hours'`); err != nil {
				t.Fatal(err)
			}
			x.setSite(t, accounts.SiteSettings{})
			if _, err := x.principal(t, console, issued); !errors.Is(err, accounts.ErrNoSession) {
				t.Errorf("the session after the site loosened its limit: %v", err)
			}
			if _, err := x.principal(t, console, x.signIn(t, console)); err != nil {
				t.Errorf("a new session after the site loosened its limit: %v", err)
			}
		})
	}
}

// setSite replaces the site's settings as the admin API does, and reloads the node's snapshot.
func (x site) setSite(t *testing.T, v accounts.SiteSettings) {
	t.Helper()
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return x.s.SetSiteSettings(t.Context(), tx, operatorActor, accounts.SiteID(s1), v, "because")
	}); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
}
