package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers"
)

// TestKeyringRefusesLifecycleWhileANodeIsAlive: activation and retirement are maintenance-window operations, and a
// recent heartbeat means a serve that was missed may still write envelopes under the version being moved away from.
func TestKeyringRefusesLifecycleWhileANodeIsAlive(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("add", "--version", "2")
	f.run("add", "--version", "3")
	if err := pgtest.Run(t.Context(), f.database.Superuser,
		"INSERT INTO platform.nodes (id, seen_at) VALUES ('rotation-test', now())"); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Activate(t.Context(), 2); err == nil || !strings.Contains(err.Error(), "heartbeat") {
		t.Fatalf("activated while a node was alive: %v", err)
	}
	if err := f.r.Retire(t.Context(), 3); err == nil || !strings.Contains(err.Error(), "heartbeat") {
		t.Fatalf("retired while a node was alive: %v", err)
	}
	// The window is kv's: a row older than the three beats no longer counts as a running node.
	if err := pgtest.Run(t.Context(), f.database.Superuser,
		"UPDATE platform.nodes SET seen_at = now() - make_interval(secs => 31)"); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Activate(t.Context(), 2); err != nil {
		t.Fatalf("activation after the heartbeat expired: %v", err)
	}
	if err := f.r.Retire(t.Context(), 3); err != nil {
		t.Fatalf("retirement after the heartbeat expired: %v", err)
	}
}

// TestKeyringRewrapsModuleEnvelopes: the TOTP secret and the supplier login are written by accounts and suppliers
// themselves, not by this test's copy of their locations. Rewrapping them proves the inventory's AAD is the one the
// modules write, and leaves the ciphertext and nonce untouched.
func TestKeyringRewrapsModuleEnvelopes(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	if _, err := f.user("", "grant-platform", "--email", ownerEmail, "--role", "admin"); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Now()}
	node := startAdminNode(f.commandLine, clock)
	node.addUser("module@example.com", "admin")
	passed := node.verified("module@example.com")

	made := supplierFixture{node, passed}.connect()
	login := "SELECT key_version::text || ':' || encode(ciphertext, 'hex') FROM suppliers.supplier_logins WHERE supplier_id = '" + made.Id + "'"
	totp := "SELECT totp_key_version::text || ':' || encode(totp_ciphertext, 'hex') FROM accounts.users WHERE email = 'module@example.com'"
	beforeLogin, beforeTOTP := f.text(login), f.text(totp)

	f.run("add", "--version", "2")
	f.run("activate", "--version", "2")
	f.makeCredentialRoutable()
	f.run("rewrap", "--batch-size", "1")
	f.run("retire", "--version", "1")
	ring, _, err := f.r.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	f.checkCredentialLoads(ring)

	if got := f.text(login); !strings.HasPrefix(got, "2:") || strings.SplitN(got, ":", 2)[1] != strings.SplitN(beforeLogin, ":", 2)[1] {
		t.Fatalf("supplier login after rewrap: %s, before %s", got, beforeLogin)
	}
	if got := f.text(totp); !strings.HasPrefix(got, "2:") || strings.SplitN(got, ":", 2)[1] != strings.SplitN(beforeTOTP, ":", 2)[1] {
		t.Fatalf("TOTP after rewrap: %s, before %s", got, beforeTOTP)
	}
}

// TestKeyringRemovesLeftoverReplacements: a replacement killed between its write and its rename leaves a file with the
// whole ring in the key directory, so the next edit removes it before writing its own.
func TestKeyringRemovesLeftoverReplacements(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	contents, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(filepath.Dir(f.path), ".keyring-crashed")
	for _, verb := range []string{"add", "add", "retire", "retire"} {
		if err := os.WriteFile(leftover, contents, 0o600); err != nil { //nolint:gosec // the path is the fixture's own temp file
			t.Fatal(err)
		}
		f.run(verb, "--version", "2")
		if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("leftover replacement after %s: %v", verb, err)
		}
	}
}

// makeCredentialRoutable lets the real suppliers loader authenticate the third inventory entry. Its key currently
// has no production sealing path; the existing supplier fixtures seed it, just as the sync's binding tests do.
func (f rotationFixture) makeCredentialRoutable() {
	f.t.Helper()
	ctx := f.t.Context()
	if err := f.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		var supplier, group uuid.UUID
		if err := tx.QueryRow(ctx, "SELECT supplier_id, supplier_group_id FROM suppliers.credentials WHERE id=$1",
			f.before[2].id).Scan(&supplier, &group); err != nil {
			return err
		}
		stmts := append(endpointStatements(supplier, group, uuid.NewV7(), "rotation", "chat"),
			statement{"UPDATE suppliers.suppliers SET status='active', balance_native=100 WHERE id=$1", []any{supplier}},
			statement{"INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ($1, 1, 'test', 'rotation')", []any{supplier}})
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, stmt.sql, stmt.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	f.checkCredentialLoads(masterKeys(f.t))
}

func (f rotationFixture) checkCredentialLoads(keys *secret.Keyring) {
	f.t.Helper()
	svc, err := suppliers.New(suppliers.Config{DB: f.db, Scope: pg.PlatformScope(), Keys: keys})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := svc.Reload(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
	if endpoints, err := svc.Routable(); err != nil || len(endpoints) != 1 {
		f.t.Fatalf("supplier credential could not load: %d endpoints, %v", len(endpoints), err)
	}
}
