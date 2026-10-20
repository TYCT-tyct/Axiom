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

// checkAPIBase checks the address the console shows a new key's owner: empty while the site registers no API domain, and
// the smallest origin once it registers two, whatever order the query returns them in.
func checkAPIBase(t *testing.T, s *accounts.Service, database pgtest.Database) {
	t.Helper()
	if site, _, err := s.SiteForHost("main.example"); err != nil || site.APIBase != "" {
		t.Errorf("no API domain: %+v %v, want no address", site, err)
	}
	if err := pgtest.Run(t.Context(), database.Superuser, `INSERT INTO accounts.site_domains VALUES
		('bbb.main.example', '`+s1.String()+`', 'api', 'https://bbb.main.example', NULL),
		('aaa.main.example', '`+s1.String()+`', 'api', 'https://aaa.main.example', NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if site, _, err := s.SiteForHost("bbb.main.example"); err != nil || site.APIBase != "https://aaa.main.example" {
		t.Errorf("two API domains: %+v %v, want the smallest origin", site, err)
	}
}

// TestSnapshot: a registered host names its site and surface whatever its case; an unknown one is refused; the site's
// API base follows its API domain; a snapshot not reloaded for 90 seconds refuses everything until a reload succeeds; a
// role holding a permission the program does not declare stops the start.
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
	checkAPIBase(t, s, database)
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
