package main

import (
	"net/http"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

func TestSiteAPIBase(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, statement string
		present         bool
		want            any
	}{
		{"enabled API domain", "SELECT 1", true, "https://api.main.example"},
		{"no enabled API domain", "UPDATE accounts.site_domains SET disabled_at = now() WHERE surface = 'api'", false, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newConsoleFixture(t)
			f.run(tt.statement)
			f = f.restarted()
			var body map[string]any
			rec := f.do(http.MethodGet, "/site", "")
			expect(t, "the site", rec, http.StatusOK, "")
			decode(t, rec, &body)
			value, present := body["api_base_url"]
			if present != tt.present || value != tt.want {
				t.Errorf("API base = %v (present %t), want %v (present %t)", value, present, tt.want, tt.present)
			}
		})
	}
}

// TestGetSiteCarriesTheSignInConfiguration: the sign-in page cannot show provider buttons, a passkey option or the
// cross-domain redirect unless the public site response carries the settings the admin stored.
func TestGetSiteCarriesTheSignInConfiguration(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	site := uuid.MustParse(f.text("SELECT id::text FROM accounts.sites WHERE is_main"))
	domain := consoleHost
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		if err := f.svc.SetSiteSignIn(t.Context(), tx, audit.System("test"), accounts.SiteID(site), accounts.SiteSignInUpdate{
			LoginDomain: &domain, Passkeys: true,
		}, "test"); err != nil {
			return err
		}
		return f.svc.SetSiteOAuthProvider(t.Context(), tx, audit.System("test"), accounts.SiteID(site), "github", "client", "secret", "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	rec := f.do(http.MethodGet, "/site", "", atHost(consoleHost))
	if rec.Code != http.StatusOK {
		t.Fatalf("GetSite: %d %s", rec.Code, rec.Body)
	}
	var body consoleapi.Site
	decode(t, rec, &body)
	checkSiteSignIn(t, body)
}

func checkSiteSignIn(t *testing.T, body consoleapi.Site) {
	t.Helper()
	if body.LoginDomain == nil || *body.LoginDomain != consoleHost {
		t.Errorf("login domain: %+v", body.LoginDomain)
	}
	if body.LoginOrigin == nil || *body.LoginOrigin != consoleOrigin {
		t.Errorf("login origin: %+v", body.LoginOrigin)
	}
	if body.Passkeys == nil || !*body.Passkeys {
		t.Errorf("passkeys: %+v", body.Passkeys)
	}
	if body.OauthProviders == nil || len(*body.OauthProviders) != 1 || (*body.OauthProviders)[0] != consoleapi.OAuthProvider("github") {
		t.Errorf("providers: %+v", body.OauthProviders)
	}
}

// TestOAuthCallbackRedirectsIntoTheConsole: the console is mounted under /console; a callback that lands on the
// marketing site cannot show the sign-in error or finish the flow.
func TestOAuthCallbackRedirectsIntoTheConsole(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	rec := f.do(http.MethodGet, "/auth/oauth/github/callback", "", atHost(consoleHost))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/console/sign-in?oauth=failed" {
		t.Fatalf("callback failure: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
