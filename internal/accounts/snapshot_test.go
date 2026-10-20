package accounts_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/platform/pgtest"
)

// TestSnapshot: a registered host names its site and surface whatever its case; an unknown one is refused; a snapshot
// not reloaded for 90 seconds refuses everything until a reload succeeds; an undeclared role permission stops the start.
func TestSnapshot(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	s := node(t, db, hashKeys(t, 1), clock)
	if site, surface, err := s.SiteForHost("MAIN.example"); err != nil || site.ID != accounts.SiteID(s1) || !site.Main || surface != accounts.Console {
		t.Errorf("main.example: %+v %s %v", site, surface, err)
	}
	if _, _, err := s.SiteForHost("other.example"); !errors.Is(err, accounts.ErrUnknownHost) {
		t.Errorf("an unknown host: %v", err)
	}
	mu.Lock()
	now = now.Add(91 * time.Second)
	mu.Unlock()
	if _, _, err := s.SiteForHost("main.example"); !errors.Is(err, accounts.ErrStale) {
		t.Errorf("after 91 seconds: %v, want stale", err)
	}
	if err := s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SiteForHost("main.example"); err != nil {
		t.Errorf("after a reload: %v", err)
	}
	if err := pgtest.Run(t.Context(), database.Superuser, "UPDATE accounts.roles SET permissions = '{keys.create}' WHERE name = 'owner'"); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(t.Context()); err == nil || !strings.Contains(err.Error(), `role owner holds "keys.create"`) {
		t.Errorf("a role with an unknown permission: %v", err)
	}
}

func TestSnapshotAPIBase(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	s := node(t, db, hashKeys(t, 1), time.Now)
	// Each step changes the same site's domains, so reload must also discard an address that has been disabled.
	for _, tt := range []struct {
		name, statement, want string
	}{
		{"no API domain", "SELECT 1", ""},
		{"another site's API domain", `INSERT INTO accounts.site_domains VALUES
			('other-api.example', '` + s2.String() + `', 'api', 'https://other-api.example', NULL)`, ""},
		{"smallest origin", `INSERT INTO accounts.site_domains VALUES
			('bbb.main.example', '` + s1.String() + `', 'api', 'https://bbb.main.example', NULL),
			('aaa.main.example', '` + s1.String() + `', 'api', 'https://aaa.main.example', NULL)`, "https://aaa.main.example"},
		{"disabled domain", `UPDATE accounts.site_domains SET disabled_at = now() WHERE domain = 'aaa.main.example'`, "https://bbb.main.example"},
		{"all API domains disabled", `UPDATE accounts.site_domains SET disabled_at = now() WHERE domain = 'bbb.main.example'`, ""},
		{"re-enabled domain", `UPDATE accounts.site_domains SET disabled_at = NULL WHERE domain = 'aaa.main.example'`, "https://aaa.main.example"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := pgtest.Run(t.Context(), database.Superuser, tt.statement); err != nil {
				t.Fatal(err)
			}
			if err := s.Reload(t.Context()); err != nil {
				t.Fatal(err)
			}
			site, _, err := s.SiteForHost("main.example")
			if err != nil {
				t.Fatal(err)
			}
			if site.APIBase != tt.want {
				t.Errorf("API base = %q, want %q", site.APIBase, tt.want)
			}
		})
	}
}
