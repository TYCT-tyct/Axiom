package accounts_test

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
)

// apiDomain is s1's API domain; second.example is s2's.
const apiDomain = "api.main.example"

var (
	client = netip.MustParseAddr("203.0.113.7")
	system = audit.System("accounts.test")
)

// hashKeys writes a key-hash file with a 32-byte key per version, each filled with its version number.
func hashKeys(t testing.TB, versions ...byte) *secret.MACKeys {
	t.Helper()
	var lines strings.Builder
	for _, v := range versions {
		lines.WriteString(string('0'+rune(v)) + ":" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(rune(v)), 32))) + "\n")
	}
	path := filepath.Join(t.TempDir(), "hash-keys")
	if err := os.WriteFile(path, []byte(lines.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := secret.LoadMAC(path)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// keyring is a master keyring with one key, for sealing TOTP secrets.
func keyring(t testing.TB) *secret.Keyring {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master-key")
	if err := os.WriteFile(path, []byte("1:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := secret.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// node is the module as one node of the program runs it, its snapshot loaded, on db; now is its clock.
func node(t testing.TB, db *pg.DB, hashes *secret.MACKeys, now func() time.Time) *accounts.Service {
	t.Helper()
	s, err := accounts.NewService(db, accounts.Config{Platform: platform, Hashes: hashes, Keyring: keyring(t), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	return s
}

// callSetup is the seeded database with s1's API domain, a node on it and a key made through it in a1's default
// workspace.
func callSetup(t testing.TB) (*pg.DB, pgtest.Database, *accounts.Service, string) {
	t.Helper()
	db, database := open(t)
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(
		`INSERT INTO accounts.site_domains VALUES ('`+apiDomain+`', :s1, 'api', 'https://`+apiDomain+`', NULL)`)); err != nil {
		t.Fatal(err)
	}
	s := node(t, db, hashKeys(t, 1), nil)
	return db, database, s, createKey(t, db, s)
}

// createKey makes a key in a1's default workspace, in a1's scope, which records the audit entry too.
func createKey(t testing.TB, db *pg.DB, s *accounts.Service) string {
	t.Helper()
	var created accounts.CreatedKey
	if err := db.Tx(t.Context(), pg.NewScope(false, nil, []uuid.UUID{a1}), func(tx pg.Tx) error {
		var err error
		created, err = s.CreateKey(t.Context(), tx, system, accounts.KeyRequest{
			Site: accounts.SiteID(s1), Account: accounts.AccountID(a1), Workspace: accounts.WorkspaceID(w1),
			CreatedBy: accounts.UserID(u1), Name: "Test",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return created.Text.Reveal()
}

// call authenticates key on site and admits it to domain.
func call(t *testing.T, s *accounts.Service, site uuid.UUID, key, domain string) error {
	t.Helper()
	c, err := s.AuthenticateKey(t.Context(), accounts.SiteID(site), key)
	if err != nil {
		return err
	}
	return s.Admit(t.Context(), c, accounts.AdmitRequest{Domain: domain, ClientIP: client},
		func(pg.Tx, accounts.Admission) error { return nil })
}

// TestKeyText: a new key is well formed, and every change to its shape or a single character is refused before
// anything is hashed.
func TestKeyText(t *testing.T) {
	t.Parallel()
	key := accounts.NewKeyText("sk-ax-v1-")
	if !accounts.WellFormedKey(key) || len(key) != len("sk-ax-v1-")+49 {
		t.Fatalf("a new key %q is not well formed", key)
	}
	flip := func(i int) string {
		b := []byte(key)
		b[i] = map[bool]byte{true: 'B', false: 'A'}[b[i] == 'A']
		return string(b)
	}
	for name, bad := range map[string]string{
		"a random character changed":   flip(20),
		"a checksum character changed": flip(len(key) - 1),
		"a character missing":          key[:len(key)-1],
		"a character more":             key + "A",
		"an upper-case prefix":         "SK-AX-V1-" + key[9:],
		"no prefix":                    key[9:],
		"a prefix of 17 characters":    "sk-ax-v1-abcdefg-" + key[9:],
		"not base62":                   key[:20] + "_" + key[21:],
		"empty":                        "",
	} {
		if accounts.WellFormedKey(bad) {
			t.Errorf("%s: %q accepted", name, bad)
		}
	}
}

// TestNextCallReadsTheDatabase: two nodes share a database and both have the key cached. A change made through one,
// or by anyone, refuses the other's very next call, while its cache still recognises the key; the identity is all
// the cache holds.
func TestNextCallReadsTheDatabase(t *testing.T) {
	t.Parallel()
	changes := map[string]func(t *testing.T, db *pg.DB, database pgtest.Database, a *accounts.Service, keyID accounts.KeyID){
		"the key disabled": func(t *testing.T, db *pg.DB, _ pgtest.Database, a *accounts.Service, keyID accounts.KeyID) {
			inPlatform(t, db, func(tx pg.Tx) error { return a.DisableKey(t.Context(), tx, system, keyID) })
		},
		"the key deleted": func(t *testing.T, db *pg.DB, _ pgtest.Database, a *accounts.Service, keyID accounts.KeyID) {
			inPlatform(t, db, func(tx pg.Tx) error { return a.DeleteKey(t.Context(), tx, system, keyID) })
		},
		"the key expired":           superuser("UPDATE accounts.api_keys SET expires_at = now() - interval '1 second'"),
		"the client's address left": superuser("UPDATE accounts.api_keys SET ip_allow = '{198.51.100.0/24}'"),
		"the workspace disabled":    superuser("UPDATE accounts.workspaces SET status = 'disabled' WHERE id = :w1"),
		"the account disabled":      superuser("UPDATE accounts.accounts SET status = 'disabled' WHERE id = :a1"),
		"the domain disabled":       superuser("UPDATE accounts.site_domains SET disabled_at = now() WHERE domain = '" + apiDomain + "'"),
		"the site disabled":         superuser("UPDATE accounts.sites SET status = 'disabled' WHERE id = :s1"),
		"the domain moved to s2":    superuser("UPDATE accounts.site_domains SET site_id = :s2 WHERE domain = '" + apiDomain + "'"),
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			nextCallRefused(t, change)
		})
	}
}

// nextCallRefused warms two nodes with a key, makes change, and expects the second node to still recognise the key
// from its cache and refuse the next call.
func nextCallRefused(t *testing.T, change func(*testing.T, *pg.DB, pgtest.Database, *accounts.Service, accounts.KeyID)) {
	t.Helper()
	db, database, a, key := callSetup(t)
	b := node(t, db, hashKeys(t, 1), nil)
	for _, n := range []*accounts.Service{a, b} {
		if err := call(t, n, s1, key, apiDomain); err != nil {
			t.Fatalf("before the change: %v", err)
		}
	}
	c, err := b.AuthenticateKey(t.Context(), accounts.SiteID(s1), key)
	if err != nil {
		t.Fatal(err)
	}
	change(t, db, database, a, c.KeyID())
	if again, err := b.AuthenticateKey(t.Context(), accounts.SiteID(s1), key); err != nil || again != c {
		t.Errorf("the cache no longer recognises the key: %v", err)
	}
	if err := b.Admit(t.Context(), c, accounts.AdmitRequest{Domain: apiDomain, ClientIP: client},
		func(pg.Tx, accounts.Admission) error { return nil }); !errors.Is(err, accounts.ErrUnknownKey) {
		t.Errorf("the next call: %v, want refused", err)
	}
}

// superuser is a change made from outside the program, with IDs spelled :name.
func superuser(statement string) func(*testing.T, *pg.DB, pgtest.Database, *accounts.Service, accounts.KeyID) {
	return func(t *testing.T, _ *pg.DB, database pgtest.Database, _ *accounts.Service, _ accounts.KeyID) {
		t.Helper()
		if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(statement)); err != nil {
			t.Fatal(err)
		}
	}
}

func inPlatform(t *testing.T, db *pg.DB, fn func(pg.Tx) error) {
	t.Helper()
	if err := db.Tx(t.Context(), platform, fn); err != nil {
		t.Fatal(err)
	}
}

// TestKnownKeyNeedsNoDatabase: once a key is known, recognising it again makes no query: it works with the pool
// closed, while a key not yet known needs the database.
func TestKnownKeyNeedsNoDatabase(t *testing.T) {
	t.Parallel()
	db, _, s, key := callSetup(t)
	if _, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key); err != nil {
		t.Errorf("a known key with the database closed: %v", err)
	}
	other := accounts.NewKeyText("sk-ax-v1-")
	if _, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), other); err == nil || errors.Is(err, accounts.ErrUnknownKey) {
		t.Errorf("an unknown key with the database closed: %v, want a database error", err)
	}
}

// TestMissesShareOneLookup: many requests with a key not yet known make one lookup between them. The key table is
// locked, so the lookup waits where it can be counted.
func TestMissesShareOneLookup(t *testing.T) {
	t.Parallel()
	_, database, s, key := callSetup(t)
	admin, release := lockTable(t, database, "accounts.api_keys")
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			_, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key)
			results <- err
		})
	}
	awaitLookup(t, admin)
	time.Sleep(300 * time.Millisecond) // time for any further lookup to reach the lock
	if n := lookupsWaiting(t, admin); n != "1" {
		t.Errorf("%s lookups waiting, want 1", n)
	}
	release()
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Error(err)
		}
	}
}

// TestEachWaiterKeepsItsDeadline: a shared lookup does not end when the request that started it leaves, and a request
// waiting on it stops at its own deadline, while the others still get the answer.
func TestEachWaiterKeepsItsDeadline(t *testing.T) {
	t.Parallel()
	_, database, s, key := callSetup(t)
	admin, release := lockTable(t, database, "accounts.api_keys")
	authenticate := func(ctx context.Context) chan error {
		result := make(chan error, 1)
		go func() {
			_, err := s.AuthenticateKey(ctx, accounts.SiteID(s1), key)
			result <- err
		}()
		return result
	}
	first, leave := context.WithCancel(t.Context())
	started := authenticate(first)
	awaitLookup(t, admin)
	patient := authenticate(t.Context())
	hurried, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	impatient := authenticate(hurried)
	time.Sleep(50 * time.Millisecond) // the others join the lookup
	leave()
	if err := <-started; !errors.Is(err, context.Canceled) {
		t.Errorf("the request that left: %v", err)
	}
	if err := <-impatient; !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the request whose deadline passed: %v", err)
	}
	release()
	if err := <-patient; err != nil {
		t.Errorf("the request still waiting: %v", err)
	}
}

// lockTable holds table locked from a superuser connection until release, which waits for the unlock.
func lockTable(t *testing.T, database pgtest.Database, table string) (*pg.DB, func()) {
	t.Helper()
	admin, err := pg.Open(t.Context(), database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	locked, unlock, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- admin.Tx(context.WithoutCancel(t.Context()), pg.Scope{}, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "LOCK TABLE "+table+" IN ACCESS EXCLUSIVE MODE"); err != nil {
				return err
			}
			close(locked)
			<-unlock
			return nil
		})
	}()
	<-locked
	return admin, func() {
		close(unlock)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// awaitLookup waits until a key lookup waits for the lock.
func awaitLookup(t *testing.T, admin *pg.DB) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); lookupsWaiting(t, admin) == "0" && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
}

func lookupsWaiting(t *testing.T, admin *pg.DB) string {
	t.Helper()
	return texts(t, admin, pg.Scope{}, `SELECT count(*)::text FROM pg_stat_activity
		WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%key_tenant%'`)[0]
}

// TestReloadGivesUpAndRecovers: a reload the database does not answer gives up within its limit instead of holding
// the reload loop, and the next one succeeds once the database answers.
func TestReloadGivesUpAndRecovers(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	s := node(t, db, hashKeys(t, 1), nil)
	_, release := lockTable(t, database, "accounts.sites")
	began := time.Now()
	if err := s.Reload(t.Context()); err == nil || time.Since(began) > 8*time.Second {
		t.Errorf("a reload against a locked table: %v after %v", err, time.Since(began))
	}
	release()
	if err := s.Reload(t.Context()); err != nil {
		t.Errorf("the next reload: %v", err)
	}
}

// BenchmarkAdmit measures Admit's query against an empty transaction in the same account scope on the same database,
// the increment README.md「认出与准入」 asks for.
func BenchmarkAdmit(b *testing.B) {
	db, _, s, key := callSetup(b)
	c, err := s.AuthenticateKey(b.Context(), accounts.SiteID(s1), key)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("empty transaction", func(b *testing.B) {
		for b.Loop() {
			if err := db.Tx(b.Context(), pg.NewScope(false, nil, []uuid.UUID{a1}), func(pg.Tx) error { return nil }); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Admit", func(b *testing.B) {
		for b.Loop() {
			if err := s.Admit(b.Context(), c, accounts.AdmitRequest{Domain: apiDomain, ClientIP: client},
				func(pg.Tx, accounts.Admission) error { return nil }); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// TestLookupsStayInTheirSite: the definer functions, which read past every policy, answer only for the site or
// domain asked about, so a key of one site is not found from another.
func TestLookupsStayInTheirSite(t *testing.T) {
	t.Parallel()
	db, _, s, key := callSetup(t)
	for site, want := range map[string]string{":s1": "1", ":s2": "0"} {
		got := texts(t, db, platform, ids.Replace(`SELECT count(*)::text FROM accounts.key_tenant(`+site+`,
			ARRAY(SELECT secret_hash FROM accounts.api_keys WHERE site_id = :s1))`))[0]
		if got != want {
			t.Errorf("keys of site %s: %s, want %s", site, got, want)
		}
	}
	for _, c := range []struct{ domain, surface, want string }{
		{apiDomain, "api", s1.String()}, {"second.example", "api", s2.String()}, {"main.example", "api", ""},
	} {
		got := texts(t, db, pg.Scope{}, "SELECT coalesce(accounts.domain_site('"+c.domain+"', '"+c.surface+"')::text, '')")[0]
		if got != c.want {
			t.Errorf("domain_site(%s, %s) = %q, want %q", c.domain, c.surface, got, c.want)
		}
	}
	if err := call(t, s, s2, key, "second.example"); !errors.Is(err, accounts.ErrUnknownKey) {
		t.Errorf("s1's key on s2: %v, want refused", err)
	}
	if err := call(t, s, s1, key, "second.example"); !errors.Is(err, accounts.ErrUnknownKey) {
		t.Errorf("s1's key on s2's API domain: %v, want refused", err)
	}
}

// TestAdmissionStaysInTheKeysAccount: fn's transaction sees and writes only the key's account, not even another
// account of its site, its error undoes what it wrote, and Admission carries the database's values and the account's
// scope.
func TestAdmissionStaysInTheKeysAccount(t *testing.T) {
	t.Parallel()
	db, _, s, key := callSetup(t)
	if _, err := exec(t, db, platform, `INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default)
		VALUES (:y, :s1, :p, 'P', 'active', true)`); err != nil {
		t.Fatal(err)
	}
	c, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key)
	if err != nil {
		t.Fatal(err)
	}
	admitted, seen, foreign, err := probe(t, s, c)
	if !errors.Is(err, errUndo) {
		t.Fatalf("Admit: %v", err)
	}
	if seen != a1.String() || state(foreign) != "42501" {
		t.Errorf("fn saw workspaces of %v and wrote to a2: %v; want only a1's, refused", seen, foreign)
	}
	if got := texts(t, db, platform, ids.Replace("SELECT id::text FROM accounts.workspaces WHERE id = :x")); len(got) != 0 {
		t.Errorf("fn's error left its workspace behind")
	}
	if got := tenants(t, db, admitted.Scope(), "SELECT account_id::text FROM accounts.workspaces"); len(got) != 1 || got[0] != "a1" {
		t.Errorf("Admission.Scope sees %v, want a1", got)
	}
	checkAdmission(t, admitted, c)
}

var errUndo = errors.New("undo")

// probe admits c, and in fn reads which accounts' workspaces it sees, tries to write a workspace of a2 (the error,
// kept) and one of a1, then fails with errUndo.
func probe(t *testing.T, s *accounts.Service, c accounts.Caller) (admitted accounts.Admission, seen string, foreign, err error) {
	t.Helper()
	err = s.Admit(t.Context(), c, accounts.AdmitRequest{Domain: "API.Main.Example", ClientIP: client}, func(tx pg.Tx, a accounts.Admission) error {
		admitted = a
		if err := tx.QueryRow(t.Context(), "SELECT string_agg(account_id::text, ',') FROM accounts.workspaces").Scan(&seen); err != nil {
			return err
		}
		for _, statement := range []string{
			"SAVEPOINT other",
			strings.NewReplacer(":s1", ":s2", ":a1", ":a2").Replace(newWorkspace),
			"ROLLBACK TO SAVEPOINT other",
			newWorkspace,
		} {
			if _, err := tx.Exec(t.Context(), ids.Replace(statement)); err != nil && foreign == nil {
				foreign = err
			}
		}
		return errUndo
	})
	return admitted, seen, foreign, err
}

// checkAdmission expects what a1's new key in its default workspace gets: the account's tier, no preset, model list or
// budget, and the default daily limit.
func checkAdmission(t *testing.T, admitted accounts.Admission, c accounts.Caller) {
	t.Helper()
	limit, reset, ok := admitted.SpendLimit()
	_, _, budget := admitted.Budget()
	_, preset := admitted.Preset()
	if admitted.Caller() != c || admitted.Tier() != accounts.TierID(t1) || admitted.Models() != nil || preset || budget ||
		!ok || limit != 10_000_000_000 || reset != "daily" {
		t.Errorf("Admission: %+v", admitted)
	}
}

// TestKeyChangesAreRecorded: making, disabling and deleting a key each leave an audit entry with the change; a key
// already disabled or deleted is not found again. A new key's daily limit is its site's, or the default.
func TestKeyChangesAreRecorded(t *testing.T) {
	t.Parallel()
	db, database, s, key := callSetup(t)
	c, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		change func(context.Context, pg.Tx, audit.Actor, accounts.KeyID) error
		want   error
	}{{s.DisableKey, nil}, {s.DisableKey, accounts.ErrKeyNotFound}, {s.DeleteKey, nil}, {s.DeleteKey, accounts.ErrKeyNotFound}}
	for i, step := range steps {
		if err := db.Tx(t.Context(), pg.NewScope(false, nil, []uuid.UUID{a1}), func(tx pg.Tx) error { return step.change(t.Context(), tx, system, c.KeyID()) }); !errors.Is(err, step.want) {
			t.Errorf("step %d: %v, want %v", i, err, step.want)
		}
	}
	got := texts(t, db, platform, "SELECT action || ' ' || target_id FROM platform.audit_log ORDER BY id")
	id := c.KeyID().TypeID()
	if strings.Join(got, "; ") != "api_key.create "+id+"; api_key.disable "+id+"; api_key.delete "+id {
		t.Errorf("audit entries: %v", got)
	}
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(`INSERT INTO accounts.site_settings (site_id, new_key_daily_limit) VALUES (:s1, 5)`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	createKey(t, db, s)
	if got := texts(t, db, platform, "SELECT spend_limit::text FROM accounts.api_keys WHERE name = 'Test' AND deleted_at IS NULL"); len(got) != 1 || got[0] != "5" {
		t.Errorf("the new key's limit: %v, want the site's 5", got)
	}
}

// TestSnapshot: a registered host names its site and surface whatever its case; an unknown one is refused; a
// snapshot not reloaded for 90 seconds refuses everything until a reload succeeds; a role holding a permission the
// program does not declare stops the start.
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

// TestHashKeyRotation: a key made under one version is still found when a newer version is current, and more than
// two versions are refused.
func TestHashKeyRotation(t *testing.T) {
	t.Parallel()
	db, _, _, key := callSetup(t)
	rotated := node(t, db, hashKeys(t, 1, 2), nil)
	if err := call(t, rotated, s1, key, apiDomain); err != nil {
		t.Errorf("a version-1 key under version 2: %v", err)
	}
	if _, err := accounts.NewService(db, accounts.Config{Platform: platform, Hashes: hashKeys(t, 1, 2, 3)}); err == nil {
		t.Error("three versions accepted")
	}
}

// BenchmarkKnownKey is the hit path: one HMAC and one map lookup, no query.
func BenchmarkKnownKey(b *testing.B) {
	_, _, s, key := callSetup(b)
	site := accounts.SiteID(s1)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.AuthenticateKey(b.Context(), site, key); err != nil {
			b.Fatal(err)
		}
	}
}
