package main

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
)

const (
	consoleHost = "console.main.example"
	secondPW    = "a second horse battery"
)

// keyFile writes a protected key file of one key whose bytes all are fill, as AXIOM_MASTER_KEY_FILE and
// AXIOM_KEY_HASH_FILE hold them.
func keyFile(t testing.TB, fill byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys")
	line := "1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32)) + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// commandLine runs `axiom site` and `axiom user` against one fresh database.
type commandLine struct {
	t       *testing.T
	environ []string
	db      *pg.DB
}

func newCommandLine(t *testing.T) commandLine {
	t.Helper()
	database := pgtest.New(t, modules()...)
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return commandLine{t, []string{"AXIOM_DATABASE_URL=" + database.App}, db}
}

// site runs `axiom site args...` and returns the line it printed.
func (c commandLine) site(args ...string) (map[string]any, error) {
	var out bytes.Buffer
	if err := siteCommand(c.t.Context(), c.environ, args, &out); err != nil {
		return nil, err
	}
	return c.line(out.Bytes()), nil
}

// user runs `axiom user args...` with stdin and returns the line it printed.
func (c commandLine) user(stdin string, args ...string) (map[string]any, error) {
	var out bytes.Buffer
	if err := userCommand(c.t.Context(), c.environ, args, strings.NewReader(stdin), &out); err != nil {
		return nil, err
	}
	return c.line(out.Bytes()), nil
}

func (c commandLine) line(out []byte) map[string]any {
	c.t.Helper()
	var line map[string]any
	if err := jsonx.Unmarshal(out, &line); err != nil {
		c.t.Fatalf("the command printed %q, which is not one JSON line: %v", out, err)
	}
	return line
}

// text is the first column of query's one row, read in the platform scope.
func (c commandLine) text(query string) string {
	c.t.Helper()
	var value string
	if err := c.db.Tx(c.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(c.t.Context(), query).Scan(&value)
	}); err != nil {
		c.t.Fatal(err)
	}
	return value
}

var siteArgs = []string{"init", "--name", "Main", "--currency", "USD", "--console", consoleHost, "--api", "api.main.example", "--admin", "admin.main.example"}

// withOwner is a command line whose site and first user exist, the user made as the README's order has it.
func withOwner(t *testing.T) commandLine {
	t.Helper()
	c := newCommandLine(t)
	if _, err := c.site(siteArgs...); err != nil {
		t.Fatal(err)
	}
	if _, err := c.user("a first horse battery\n", "create", "--email", "Owner@Example.com"); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestSiteInitAndUserCreate: the site is made with the currency and key prefix, and the user it makes, whose email is
// stored in lower case, has a balance row, which the command's wiring of billing.OpenBalance makes.
func TestSiteInitAndUserCreate(t *testing.T) {
	c := newCommandLine(t)
	site, err := c.site(siteArgs...)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := site["site"].(string); !strings.HasPrefix(id, "site_") {
		t.Errorf("site init printed %v", site)
	}
	if got := c.text("SELECT currency || ' ' || key_prefix FROM accounts.sites"); got != "USD sk-ax-v1-" {
		t.Errorf("the site: %q", got)
	}
	created, err := c.user("a first horse battery\n", "create", "--email", "Owner@Example.com")
	if err != nil {
		t.Fatal(err)
	}
	account, _ := created["account"].(string)
	parsed, err := accounts.ParseAccountID(account)
	if err != nil {
		t.Fatalf("user create printed %v: %v", created, err)
	}
	if got := c.text("SELECT floor::text || ' ' || balance::text || ' ' || reserved::text FROM billing.balances WHERE account_id = '" + uuid.UUID(parsed).String() + "'"); got != "0 0 0" {
		t.Errorf("the new account's balance row: %q", got)
	}
	if got := c.text("SELECT email FROM accounts.users"); got != "owner@example.com" {
		t.Errorf("the stored email: %q", got)
	}
}

// TestUserVerbs: grant-platform, set-password and reset-mfa each report that they are done; the password
// set-password read from standard input, less its line ending, is the one that signs in afterwards.
func TestUserVerbs(t *testing.T) {
	c := withOwner(t)
	if done, err := c.user("", "grant-platform", "--email", "owner@example.com"); err != nil || done["done"] != "grant-platform" {
		t.Errorf("grant-platform: %v, %v", done, err)
	}
	if got := c.text("SELECT count(*)::text FROM accounts.platform_grants"); got != "1" {
		t.Errorf("%s grants", got)
	}
	if done, err := c.user(secondPW+"\r\n", "set-password", "--email", "owner@example.com"); err != nil || done["done"] != "set-password" {
		t.Errorf("set-password: %v, %v", done, err)
	}
	if done, err := c.user("", "reset-mfa", "--email", "owner@example.com"); err != nil || done["done"] != "reset-mfa" {
		t.Errorf("reset-mfa: %v, %v", done, err)
	}
	s, err := accounts.NewService(c.db, accounts.Config{Platform: pg.PlatformScope(), Hashes: macKeys(t), Keyring: masterKeys(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	for password, ok := range map[string]bool{"a first horse battery": false, secondPW: true} {
		_, err := s.SignIn(t.Context(), accounts.SignInRequest{
			Host: consoleHost, Email: "owner@example.com", Password: password, ClientIP: netip.MustParseAddr("203.0.113.9"),
		})
		if (err == nil) != ok {
			t.Errorf("signing in with %q: %v, want success %v", password, err, ok)
		}
	}
}

func macKeys(t testing.TB) *secret.MACKeys {
	t.Helper()
	keys, err := secret.LoadMAC(keyFile(t, 'h'))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func masterKeys(t testing.TB) *secret.Keyring {
	t.Helper()
	keys, err := secret.Load(keyFile(t, 'm'))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// TestOperateCommandRefusals: what a command cannot do it says, and writes nothing.
func TestOperateCommandRefusals(t *testing.T) {
	c := newCommandLine(t)
	if _, err := c.user("a first horse battery", "create", "--email", "owner@example.com"); err == nil || !strings.Contains(err.Error(), "axiom site init") {
		t.Errorf("a user before the site: %v", err)
	}
	if _, err := c.site(siteArgs...); err != nil {
		t.Fatal(err)
	}
	if _, err := c.site("init", "--name", "Again", "--currency", "USD", "--console", "c.other.example", "--api", "a.other.example"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("a second main site: %v", err)
	}
	long := strings.Repeat("p", 2000)
	for _, f := range []struct {
		name  string
		stdin string
		args  []string
		want  string
	}{
		{"no verb", "", nil, "usage"},
		{"an unknown verb", "", []string{"delete", "--email", "a@b.c"}, "unknown verb"},
		{"no email", "", []string{"reset-mfa"}, "--email is required"},
		{"an argument that is no flag", "", []string{"reset-mfa", "--email", "a@b.c", "extra"}, "unexpected argument"},
		{"an unknown flag", "", []string{"reset-mfa", "--email", "a@b.c", "--force"}, "force"},
		{"an empty password", "\n", []string{"create", "--email", "a@b.c"}, "empty"},
		{"a password that is too long", long, []string{"create", "--email", "a@b.c"}, "too long"},
		{"a weak password", "short\n", []string{"create", "--email", "a@b.c"}, "12 to 256"},
		{"a user who does not exist", "", []string{"reset-mfa", "--email", "nobody@example.com"}, "not found"},
	} {
		if _, err := c.user(f.stdin, f.args...); err == nil || !strings.Contains(err.Error(), f.want) {
			t.Errorf("%s: %v, want %q", f.name, err, f.want)
		}
	}
	for name, args := range map[string][]string{
		"no operation":          nil,
		"another operation":     {"drop"},
		"a lower-case currency": {"init", "--name", "N", "--currency", "usd", "--console", "c.x.example", "--api", "a.x.example"},
		"a missing console":     {"init", "--name", "N", "--currency", "USD", "--api", "a.x.example"},
	} {
		if _, err := c.site(args...); err == nil {
			t.Errorf("site %s: no error", name)
		}
	}
	if got := c.text("SELECT count(*)::text FROM accounts.users"); got != "0" {
		t.Errorf("%s users after the refusals, want none", got)
	}
}
