package accounts_test

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
)

// loginKey is what secret derives from the master key in production, the same for every node: one that differed would
// count one email's failures twice (internal/platform/kv/README.md「键」).
var loginKey = filledLoginKey('k')

func filledLoginKey(fill byte) (key secret.LoginKey) {
	for i := range key {
		key[i] = fill
	}
	return key
}

// clientClock is the clock a test's cache client measures its offset from: the test's own frozen clock, moved up by how
// long the test's setup took, so that the client opens within the second of the server's time that the startup check
// allows (internal/platform/kv/README.md「时钟」). The test's own advances still move it.
func clientClock(c *clock) func() time.Time {
	offset := time.Since(c.Now())
	return func() time.Time { return c.Now().Add(offset) }
}

// cachedNode is node with the attempts before sign-in counted in cache, as serve wires it.
func cachedNode(t *testing.T, db *pg.DB, cache *kv.Client, now func() time.Time) *accounts.Service {
	t.Helper()
	s, err := accounts.NewService(db, accounts.Config{
		Platform: platform, Hashes: hashKeys(t, 1), Keyring: keyring(t), Now: now, Cache: cache, LoginKey: loginKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	return s
}

// deadCache is a client of a server that is not there, with nodes in the configuration: a node starts through an
// outage with the breaker open, and counts with its share of the limits (internal/platform/kv/README.md「不可用」).
func deadCache(t *testing.T, nodes int, now func() time.Time) *kv.Client {
	t.Helper()
	file := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(file, []byte(strings.Repeat("p", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A refused connection exercises startup fallback without racing a healthy server against the production budget.
	cache, err := kv.Open(t.Context(), kv.Config{URL: "valkey://axiom@127.0.0.1:1", PasswordFile: file, Nodes: nodes},
		kvtest.Deps(kv.Deps{Now: now}))
	if err != nil {
		t.Fatalf("a node must start with its shared store away: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
		defer cancel()
		if err := cache.Close(ctx); err != nil {
			t.Errorf("close the cache: %v", err)
		}
	})
	return cache
}

// TestFailedSignInsAreCountedAcrossNodes: two nodes of one deployment count one email's failed sign-ins together, and a
// sign-in that succeeds gives its attempt back to the store it took, so the limit is the platform's and does not grow
// with the nodes (internal/platform/kv/README.md「多节点」, the acceptance of #321's K2).
func TestFailedSignInsAreCountedAcrossNodes(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(`INSERT INTO accounts.site_settings
		(site_id, email_sign_in_failures) VALUES (:s1, 2)`)); err != nil {
		t.Fatal(err)
	}
	namespace := kvtest.Namespace()
	server := kvtest.New(t)
	a := cachedNode(t, x.db, server.OpenIn(t, namespace, kv.Deps{Now: clientClock(x.clock)}), x.clock.Now)
	b := cachedNode(t, x.db, server.OpenIn(t, namespace, kv.Deps{Now: clientClock(x.clock)}), x.clock.Now)
	signIn := func(s *accounts.Service, from byte, pw string) error {
		_, err := s.SignIn(t.Context(), accounts.SignInRequest{
			Host: console, Email: x.email, Password: pw, ClientIP: netip.AddrFrom4([4]byte{198, 51, 100, from}),
		})
		return err
	}
	const wrong = "wrong password!"
	for i, c := range []struct {
		name string
		s    *accounts.Service
		from byte
		pw   string
		want error
	}{
		{"a wrong password on the first node", a, 1, wrong, accounts.ErrBadCredentials},
		{"the right one on the second, which gives its attempt back", b, 2, password, nil},
		{"another wrong password on the first", a, 3, wrong, accounts.ErrBadCredentials},
		{"the right one on the second, both failures counted", b, 4, password, accounts.ErrTooManyAttempts},
	} {
		if err := signIn(c.s, c.from, c.pw); !errors.Is(err, c.want) || c.want == nil && err != nil {
			t.Fatalf("%d %s: %v, want %v", i, c.name, err, c.want)
		}
	}
	// A second more than one failure's refill, so that the two nodes' clocks, milliseconds apart, cannot decide it.
	x.clock.Advance(emailFailureInterval + time.Second)
	if err := b.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := signIn(b, 5, password); err != nil {
		t.Errorf("the right password once one failure has refilled: %v", err)
	}
}

// TestOneIPv6PrefixIsOneBucket: the /64 one user holds is one bucket, in this node's buckets and in the shared store
// alike, so another address inside it buys no attempt while another prefix does
// (internal/platform/kv/README.md「键」).
func TestOneIPv6PrefixIsOneBucket(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(`INSERT INTO accounts.site_settings
		(site_id, ip_attempt_burst, ip_attempts_per_minute) VALUES (:s1, 3, 1)`)); err != nil {
		t.Fatal(err)
	}
	namespace := kvtest.Namespace()
	server := kvtest.New(t)
	a := cachedNode(t, x.db, server.OpenIn(t, namespace, kv.Deps{Now: clientClock(x.clock)}), x.clock.Now)
	b := cachedNode(t, x.db, server.OpenIn(t, namespace, kv.Deps{Now: clientClock(x.clock)}), x.clock.Now)
	signIn := func(s *accounts.Service, from string) error {
		_, err := s.SignIn(t.Context(), accounts.SignInRequest{
			Host: console, Email: x.email, Password: password, ClientIP: netip.MustParseAddr(from),
		})
		return err
	}
	for i, c := range []struct {
		name string
		s    *accounts.Service
		from string
		want error
	}{
		{"the first address", a, "2001:db8:1:2::1", nil},
		{"another address in the same /64", a, "2001:db8:1:2::2", nil},
		{"a third, on the other node", b, "2001:db8:1:2:ffff::3", nil},
		{"a fourth in the same /64", b, "2001:db8:1:2::4", accounts.ErrTooManyAttempts},
		{"the first address of another /64", b, "2001:db8:1:3::1", nil},
	} {
		if err := signIn(c.s, c.from); !errors.Is(err, c.want) || c.want == nil && err != nil {
			t.Fatalf("%d %s: %v, want %v", i, c.name, err, c.want)
		}
	}
}

// TestLoginLimitsFallBackToTheShare: with the shared store away, sign-in still works and is limited by this node's share
// of the site's limits: the burst divided by the nodes, the interval multiplied by them, and nothing refused that the
// share leaves (internal/platform/kv/README.md「不可用」).
func TestLoginLimitsFallBackToTheShare(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(`INSERT INTO accounts.site_settings
		(site_id, ip_attempt_burst, ip_attempts_per_minute, email_sign_in_failures) VALUES (:s1, 3, 1, 2)`)); err != nil {
		t.Fatal(err)
	}
	s := cachedNode(t, x.db, deadCache(t, 2, x.clock.Now), x.clock.Now)
	signIn := func(from byte, pw string) error {
		_, err := s.SignIn(t.Context(), accounts.SignInRequest{
			Host: console, Email: x.email, Password: pw, ClientIP: netip.AddrFrom4([4]byte{198, 51, 100, from}),
		})
		return err
	}
	for i, c := range []struct {
		name string
		from byte
		pw   string
		want error
	}{
		{"a sign-in with the store away, which works", 1, password, nil},
		{"a second from the same IP, with a share of the burst that is one", 1, password, accounts.ErrTooManyAttempts},
		{"a wrong password from another IP, with a share of the email's failures that is one", 2, "wrong password!", accounts.ErrBadCredentials},
		{"the right password from a third IP", 3, password, accounts.ErrTooManyAttempts},
	} {
		if err := signIn(c.from, c.pw); !errors.Is(err, c.want) || c.want == nil && err != nil {
			t.Errorf("%d %s: %v, want %v", i, c.name, err, c.want)
		}
	}
	x.clock.Advance(emailFailureInterval)
	if err := s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := signIn(1, password); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Fatalf("one global refill interval must not refill a node's share: %v", err)
	}
	x.clock.Advance(emailFailureInterval) // a fallback token refills after N global intervals
	if err := s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := signIn(1, password); err != nil {
		t.Fatalf("fallback refills under the controlled clock: %v", err)
	}
}

// TestLoginKeysCarryNoPersonalData: what the shared store keeps of a sign-in names neither the email nor the address; a
// scan of it finds the site and an HMAC, and no personal data (internal/platform/kv/README.md「键」).
func TestLoginKeysCarryNoPersonalData(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t) // the read-only admin user, to read every key
	x := newTestSite(t)
	namespace := kvtest.Namespace()
	s := cachedNode(t, x.db, server.OpenIn(t, namespace, kv.Deps{Now: clientClock(x.clock)}), x.clock.Now)
	const from = "203.0.113.9"
	if _, err := s.SignIn(t.Context(), accounts.SignInRequest{
		Host: console, Email: x.email, Password: "wrong password!", ClientIP: netip.MustParseAddr(from),
	}); !errors.Is(err, accounts.ErrBadCredentials) {
		t.Fatalf("a wrong password: %v", err)
	}
	var keys []string
	var cursor uint64
	for {
		page, err := server.Read(t, t.Context(), server.Admin.B().Scan().Cursor(cursor).Match("ax:"+namespace+":*").Build())[0].AsScanEntry()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, page.Elements...)
		if cursor = page.Cursor; cursor == 0 {
			break
		}
	}
	if len(keys) != 2 {
		t.Fatalf("the store holds %d keys, want the email's bucket and the IP's: %v", len(keys), keys)
	}
	for _, key := range keys {
		checkLoginKey(t, key, namespace, from)
	}
}

// checkLoginKey is one of the store's login buckets: ax:<ns>:login:<site>:<kind>:<h>, and nothing personal in it.
func checkLoginKey(t *testing.T, key, namespace, from string) {
	t.Helper()
	for _, fragment := range []string{"@", "example", from, "203.0.113"} {
		if strings.Contains(strings.ToLower(key), fragment) {
			t.Errorf("%q carries %q", key, fragment)
		}
	}
	fields := strings.Split(key, ":")
	if len(fields) != 6 || fields[0] != "ax" || fields[1] != namespace || fields[2] != "login" || len(fields[5]) != 32 {
		t.Errorf("%q is not a login bucket's key", key)
		return
	}
	if fields[4] != "email" && fields[4] != "ip" {
		t.Errorf("%q is neither an email's bucket nor an IP's", key)
	}
}
