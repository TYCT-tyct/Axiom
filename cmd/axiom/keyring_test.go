package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
)

type rotationFixture struct {
	commandLine
	path   string
	r      secret.Rotation
	before []rotationEnvelope
}

type rotationEnvelope struct {
	store  secret.EnvelopeStore
	id     string
	sealed secret.Sealed
}

func newRotationFixture(t *testing.T) rotationFixture {
	t.Helper()
	c := withOwner(t)
	path := keyFile(t, 'm')
	f := rotationFixture{commandLine: c, path: path, r: keyRotation(c.db, path)}
	f.environ = append(f.environ, masterKeyFileVar+"="+path)
	f.seed()
	return f
}

func (f rotationFixture) seed() {
	f.t.Helper()
	ctx := f.t.Context()
	keys := masterKeys(f.t)
	supplier, credential, group := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	user := f.text("SELECT id::text FROM accounts.users")
	for i, id := range []string{user, supplier.String(), credential.String()} {
		store := f.r.Stores[i]
		sealed := keys.Seal(secret.New("planted rotation plaintext"), secret.Location{
			Table: store.Schema + "." + store.Table, RowID: id, Purpose: store.Purpose,
		})
		f.before = append(f.before, rotationEnvelope{store, id, sealed})
	}
	if err := f.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO suppliers.suppliers
			(id, owner_scope, name, kind, origin, upstream_account, native_unit, settlement_currency, mgmt_egress_id)
			VALUES ($1, 'platform', 'rotation', 'newapi', 'https://rotation.example', 'account', 'usd', 'USD', 'direct')`, supplier); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO suppliers.supplier_groups
			(id, supplier_id, upstream_ref, name, ratio, selected, present, codename)
			VALUES ($1, $2, 'default', 'default', 1, true, true, 'rotation')`, group, supplier); err != nil {
			return err
		}
		queries := []string{
			`UPDATE accounts.users SET totp_ciphertext=$2, totp_nonce=$3, totp_wrapped_dek=$4, totp_key_version=$5 WHERE id=$1`,
			`INSERT INTO suppliers.supplier_logins (supplier_id, kind, ciphertext, nonce, wrapped_dek, key_version)
				VALUES ($1, 'token', $2, $3, $4, $5)`,
			`INSERT INTO suppliers.credentials
				(id, ciphertext, nonce, wrapped_dek, key_version, supplier_id, supplier_group_id, upstream_key_ref, status, max_concurrency)
				VALUES ($1, $2, $3, $4, $5, $6, $7, 'rotation', 'active', 1)`,
		}
		for i, row := range f.before {
			args := []any{row.id, row.sealed.Ciphertext, row.sealed.Nonce, row.sealed.WrappedDEK, int64(row.sealed.KeyVersion)}
			if i == 2 {
				args = append(args, supplier, group)
			}
			if _, err := tx.Exec(ctx, queries[i], args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f rotationFixture) run(args ...string) string {
	f.t.Helper()
	var out bytes.Buffer
	if err := keyringCommand(f.t.Context(), f.environ, args, &out); err != nil {
		f.t.Fatal(err)
	}
	return out.String()
}

func (f rotationFixture) read(row rotationEnvelope) secret.Sealed {
	f.t.Helper()
	var sealed secret.Sealed
	s := row.store
	// Identifiers are the static inventory from keyRotation, not test or user input.
	query := "SELECT " + s.Prefix + "ciphertext, " + s.Prefix + "nonce, " + s.Prefix + "wrapped_dek, " + s.Prefix + "key_version FROM " +
		s.Schema + "." + s.Table + " WHERE " + s.ID + "=$1"
	if err := f.db.Tx(f.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(f.t.Context(), query, row.id).Scan(&sealed.Ciphertext, &sealed.Nonce, &sealed.WrappedDEK, &sealed.KeyVersion)
	}); err != nil {
		f.t.Fatal(err)
	}
	return sealed
}

func TestKeyringRotationEndToEnd(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.addAndActivate("2")
	// A rotated read is only useful once the data written before the switch is readable under the new version.
	if out := f.run("rewrap", "--batch-size", "1", "--max-batches", "1"); !strings.Contains(out, `"paused"`) {
		t.Fatalf("bounded run did not pause: %s", out)
	}
	if out := f.run("rewrap", "--batch-size", "1"); !strings.Contains(out, `"done"`) {
		t.Fatalf("resume did not finish: %s", out)
	}
	if got := f.rewrapped(); got != "3" {
		t.Fatalf("rewrapped/audited %s rows, want 3", got)
	}
	// Adding a version that exists, and activating the active one, must change nothing.
	audits := f.text("SELECT count(*)::text FROM platform.audit_log WHERE action LIKE 'keyring.%'")
	f.run("add", "--version", "2")
	f.run("activate", "--version", "2")
	f.run("rewrap")
	if got := f.text("SELECT count(*)::text FROM platform.audit_log WHERE action LIKE 'keyring.%'"); got != audits {
		t.Fatal("idempotent retries added mutations or audit entries")
	}
	f.run("retire", "--version", "1")
	f.run("retire", "--version", "1")
	f.verifyRetired()
}

// verifyRetired checks the state a finished rotation leaves, and that the retired version stays refused.
func (f rotationFixture) verifyRetired() {
	f.t.Helper()
	ring, status, err := f.r.Check(f.t.Context())
	if err != nil || status.Active != 2 || len(status.Readable) != 1 || len(status.Retired) != 1 {
		f.t.Fatalf("retirement status: %+v, %v", status, err)
	}
	f.verifyRotated(ring)
	// A retired version is refused by its state, before any material is written for it: the tombstone in the database
	// would refuse the insert too, but only after the file had grown a key for a version it considers dead.
	before, err := os.ReadFile(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.r.Add(f.t.Context(), 1); err == nil || !strings.Contains(err.Error(), "is retired") {
		f.t.Fatalf("reused a retired version: %v", err)
	}
	if after, err := os.ReadFile(f.path); err != nil || !bytes.Equal(before, after) {
		f.t.Fatal("a refused addition rewrote the key file")
	}
}

// addAndActivate runs the two lifecycle commands of a rotation and checks what adding alone must not do: change the
// write version, or leave the new key unusable for later reads.
func (f rotationFixture) addAndActivate(version string) {
	f.t.Helper()
	f.run("add", "--version", version)
	ring, status, err := f.r.Check(f.t.Context())
	if err != nil || status.Active != 1 || len(status.Readable) != 2 {
		f.t.Fatalf("adding activated a write key or lost a readable key: %+v, %v", status, err)
	}
	if ring.Seal(secret.New("new data"), secret.Location{Table: "test", RowID: "1", Purpose: "test"}).KeyVersion != 1 {
		f.t.Fatal("a newly added readable key was used for writes")
	}
	f.run("activate", "--version", version)
	if err := f.r.Retire(f.t.Context(), 1); err == nil || !strings.Contains(err.Error(), "still referenced") {
		f.t.Fatalf("retired a referenced key: %v", err)
	}
	if err := f.r.Retire(f.t.Context(), 2); err == nil {
		f.t.Fatal("retired the active key")
	}
}

// rewrapped is the number of envelopes the rewrap audit entries report, which is also the number of committed batches.
func (f rotationFixture) rewrapped() string {
	f.t.Helper()
	return f.text("SELECT coalesce(sum((detail->>'count')::int), 0)::text FROM platform.audit_log WHERE action='keyring.rewrap'")
}

func (f rotationFixture) verifyRotated(ring *secret.Keyring) {
	f.t.Helper()
	for _, row := range f.before {
		got := f.read(row)
		if got.KeyVersion != 2 || !bytes.Equal(got.Ciphertext, row.sealed.Ciphertext) || !bytes.Equal(got.Nonce, row.sealed.Nonce) {
			f.t.Fatalf("data layer or version changed incorrectly in %s", row.store.Table)
		}
		opened, err := ring.Open(got, secret.Location{
			Table: row.store.Schema + "." + row.store.Table, RowID: row.id, Purpose: row.store.Purpose,
		})
		if err != nil || opened.Reveal() != "planted rotation plaintext" {
			f.t.Fatalf("could not open rotated %s: %v", row.store.Table, err)
		}
	}
	contents, err := os.ReadFile(f.path)
	if err != nil || bytes.Contains(contents, []byte("1:")) {
		f.t.Fatal("retirement did not prune the old file key")
	}
}

type cancelAfterBatch struct{ cancel context.CancelFunc }

func (w cancelAfterBatch) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

func TestKeyringInterruptedRewrapResumes(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("add", "--version", "2")
	f.run("activate", "--version", "2")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := keyringCommand(ctx, f.environ, []string{"rewrap", "--batch-size", "1"}, cancelAfterBatch{cancel})
	if err == nil {
		t.Fatal("cancellation did not interrupt rewrap")
	}
	if got := f.text("SELECT sum((detail->>'count')::int)::text FROM platform.audit_log WHERE action='keyring.rewrap'"); got != "1" {
		t.Fatalf("interruption committed %s envelopes, want one", got)
	}
	f.run("rewrap", "--batch-size", "1")
	if got := f.text("SELECT sum((detail->>'count')::int)::text FROM platform.audit_log WHERE action='keyring.rewrap'"); got != "3" {
		t.Fatalf("resume skipped or repeated envelopes: %s", got)
	}
}

func TestKeyringCheckMissingAndMismatchedVersions(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("add", "--version", "2")
	f.run("activate", "--version", "2")
	for _, tc := range []struct {
		name   string
		path   string
		reason string
	}{
		{"missing", keyFile(t, 'm'), "missing versions [2]"},
		{"mismatched", keyFile(t, 'x'), "mismatched versions [1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// This replacement file simulates a node that was missed during distribution.
			environ := []string{
				pg.DatabaseURLVar + "=" + f.database.App, masterKeyFileVar + "=" + tc.path,
				keyHashFileVar + "=" + keyFile(t, 'h'), identityKeyFileVar + "=" + keyFile(t, 'i'),
			}
			var out bytes.Buffer
			err := keyringCommand(t.Context(), environ, []string{"check"}, &out)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("deploy check: %v", err)
			}
			err = serve(t.Context(), environ, slog.New(slog.DiscardHandler))
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("startup check: %v", err)
			}
			if strings.Contains(err.Error(), tc.path) || out.Len() != 0 {
				t.Fatal("failed consistency check exposed a path or printed a success")
			}
		})
	}
}

func TestKeyringAuditFailureRollsBackBatch(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("add", "--version", "2")
	f.run("activate", "--version", "2")
	if err := pgtest.Run(t.Context(), f.database.Superuser,
		"ALTER TABLE platform.audit_log ADD CONSTRAINT reject_rewrap CHECK (action <> 'keyring.rewrap') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.r.RewrapBatch(t.Context(), secret.RewrapCursor{}, 100); err == nil {
		t.Fatal("rewrap succeeded without audit")
	}
	for _, row := range f.before {
		got := f.read(row)
		if got.KeyVersion != row.sealed.KeyVersion || !bytes.Equal(got.WrappedDEK, row.sealed.WrappedDEK) {
			t.Fatal("audit failure committed an envelope")
		}
	}
}

func TestKeyringArgs(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"add"},
		{"add", "--version", "0"},
		{"activate", "--version", "01"},
		{"retire", "--version", "4294967296"},
		{"rewrap", "--batch-size", "0"},
		{"rewrap", "--batch-size", "1001"},
		{"rewrap", "--max-batches", "-1"},
		{"check", "--version", "1"},
	} {
		if _, err := parseKeyringArgs(args); err == nil {
			t.Errorf("accepted invalid arguments %v", args)
		}
	}
}

// TestKeyringCheckFitsTheStartupTimeout: the startup check reads every envelope store, so a lock held on one of them
// ends the start within AXIOM_STARTUP_TIMEOUT instead of hanging before the listeners open.
func TestKeyringCheckFitsTheStartupTimeout(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	admin, err := pg.Open(t.Context(), f.database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	locked, unlock, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- admin.Tx(context.WithoutCancel(t.Context()), pg.Scope{}, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "LOCK TABLE accounts.users IN ACCESS EXCLUSIVE MODE"); err != nil {
				return err
			}
			close(locked)
			<-unlock
			return nil
		})
	}()
	<-locked
	environ := []string{
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0", "AXIOM_STARTUP_TIMEOUT=1s",
		pg.DatabaseURLVar + "=" + f.database.App, masterKeyFileVar + "=" + f.path,
		keyHashFileVar + "=" + keyFile(t, 'h'), identityKeyFileVar + "=" + keyFile(t, 'i'),
	}
	served := make(chan error, 1)
	go func() { served <- serve(t.Context(), environ, slog.New(slog.DiscardHandler)) }()
	select {
	case err := <-served:
		if !errors.Is(err, errNoAnswer) {
			t.Errorf("serve = %v, want errNoAnswer", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the keyring check kept the node from starting 10s past a startup timeout of 1s")
	}
	close(unlock)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestKeyringOutputFailureLeavesCommittedCheckpoint(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("add", "--version", "2")
	f.run("activate", "--version", "2")
	err := keyringCommand(t.Context(), f.environ, []string{"rewrap", "--batch-size", "1"}, failingKeyringOutput{})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output failure: %v", err)
	}
	f.run("rewrap")
	if got := f.text("SELECT sum((detail->>'count')::int)::text FROM platform.audit_log WHERE action='keyring.rewrap'"); got != "3" {
		t.Fatalf("output failure lost checkpoint: %s", got)
	}
}

type failingKeyringOutput struct{}

func (failingKeyringOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

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
	f.run("rewrap", "--batch-size", "2")

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
	if err := os.WriteFile(leftover, contents, 0o600); err != nil { //nolint:gosec // the path is the fixture's own temp file
		t.Fatal(err)
	}
	f.run("add", "--version", "2")
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leftover replacement after add: %v", err)
	}
}
