package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers"
)

// rotationFixture helpers keep one lifecycle test's sequence readable: each statement or assertion is one call, and the
// failure message says which state was expected.
func (f rotationFixture) execSQL(t *testing.T, statement string) {
	t.Helper()
	if err := pgtest.Run(t.Context(), f.database.Superuser, statement); err != nil {
		t.Fatal(err)
	}
}

func (f rotationFixture) mustActivate(t *testing.T, version uint32) {
	t.Helper()
	if err := f.r.Activate(t.Context(), version); err != nil {
		t.Fatalf("activate %d: %v", version, err)
	}
}

func (f rotationFixture) mustRetire(t *testing.T, version uint32) {
	t.Helper()
	if err := f.r.Retire(t.Context(), version); err != nil {
		t.Fatalf("retire %d: %v", version, err)
	}
}

func wantLifecycleError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want an error containing %q", err, want)
	}
}

// TestKeyringWaitsForEveryLiveNode: activation waits until every live heartbeat reports the new version, and retirement
// waits until every live node has written the active version for a whole heartbeat window, so no delayed seal can commit
// under the version whose material is about to be destroyed. A stale row no longer counts as a running node.
func TestKeyringWaitsForEveryLiveNode(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("add", "--version", "2")
	f.run("add", "--version", "3")
	f.execSQL(t, "INSERT INTO platform.nodes (id, seen_at, key_versions, key_active) VALUES ('rotation-test', now(), '{1}', 1)")
	wantLifecycleError(t, f.r.Activate(t.Context(), 2), "do not report version 2")
	wantLifecycleError(t, f.r.Retire(t.Context(), 3), "not on version 1")
	// The node reloaded the distribution file but has written version 1 until just now: the quiet window still holds.
	f.execSQL(t, "UPDATE platform.nodes SET key_versions='{1,2,3}', key_active=1, key_active_since=now() - make_interval(secs => 31)")
	f.mustActivate(t, 2)
	f.execSQL(t, "UPDATE platform.nodes SET key_active=2, key_active_since=now()")
	wantLifecycleError(t, f.r.Retire(t.Context(), 3), "not on version 2")
	f.execSQL(t, "UPDATE platform.nodes SET key_active_since=now() - make_interval(secs => 31)")
	f.mustRetire(t, 3)
	// The window is kv's: a row older than the three beats no longer counts as a running node.
	f.execSQL(t, "UPDATE platform.nodes SET seen_at = now() - make_interval(secs => 31)")
	wantLifecycleError(t, f.r.Retire(t.Context(), 2), "unknown or active")
	// Version 1 is still readable and referenced by the fixture's envelopes, so it stays until rewrapped; with the row
	// stale, retirement is judged on references alone.
	wantLifecycleError(t, f.r.Retire(t.Context(), 1), "still referenced")
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
	credential := ""
	for _, row := range f.before {
		if row.store.Table == "credentials" {
			credential = row.id
		}
	}
	if err := f.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		var supplier, group uuid.UUID
		if err := tx.QueryRow(ctx, "SELECT supplier_id, supplier_group_id FROM suppliers.credentials WHERE id=$1",
			credential).Scan(&supplier, &group); err != nil {
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

// putFileKey replaces one version in the test's key file, without going through add: a failed add is what leaves a
// version in the file that the database never registered.
func putFileKey(t *testing.T, path string, version uint32, key []byte) {
	t.Helper()
	contents, err := os.ReadFile(path) //nolint:gosec // path is the test's own temporary key file
	if err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("%d:%s", version, base64.StdEncoding.EncodeToString(key))
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	replaced := false
	for i, existing := range lines {
		if v, _, _ := strings.Cut(existing, ":"); v == strconv.FormatUint(uint64(version), 10) {
			lines[i], replaced = line, true
		}
	}
	if !replaced {
		lines = append(lines, line)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil { //nolint:gosec // same test-owned path
		t.Fatal(err)
	}
}

func versionArray(versions []uint32) string {
	parts := make([]string, len(versions))
	for i, version := range versions {
		parts[i] = strconv.FormatUint(uint64(version), 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func wantReport(t *testing.T, ring *secret.Keyring, active uint32, versions ...uint32) {
	t.Helper()
	got, gotActive := ring.Report()
	if gotActive != active || !slices.Equal(got, versions) {
		t.Fatalf("report versions %v active %d, want %v active %d", got, gotActive, versions, active)
	}
}

func registerReadable(t *testing.T, f rotationFixture, version uint32, key []byte) {
	t.Helper()
	fingerprint := sha256.Sum256(key)
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(),
			"INSERT INTO platform.key_versions (version, fingerprint, state) VALUES ($1, $2, 'readable')",
			int64(version), fingerprint[:])
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestKeyringReportsOnlyFingerprintConfirmedVersions: a failed add can leave a version in one node's file, and another
// node can register that number with different material. The first node must not report the orphan as readable, or
// activation would let the cluster write envelopes it cannot open. Its old snapshot stays reportable after the failed
// refresh; replacing the orphan with the registered material recovers it.
func TestKeyringReportsOnlyFingerprintConfirmedVersions(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	ring, _, err := f.r.Check(t.Context())
	if err != nil {
		t.Fatalf("initial check: %v", err)
	}
	orphan := bytes.Repeat([]byte{'b'}, 32)
	owner := bytes.Repeat([]byte{'a'}, 32)
	putFileKey(t, f.path, 2, orphan)
	if _, err := f.r.Refresh(t.Context(), ring); err != nil {
		t.Fatalf("refresh with an unregistered file version: %v", err)
	}
	wantReport(t, ring, 1, 1)

	// Node A registers version 2 with different material. B's refresh fails on the mismatch, and B must keep reporting
	// only what its installed snapshot had confirmed.
	registerReadable(t, f, 2, owner)
	if _, err := f.r.Refresh(t.Context(), ring); err == nil || !strings.Contains(err.Error(), "mismatched") {
		t.Fatalf("refresh accepted a mismatched fingerprint: %v", err)
	}
	wantReport(t, ring, 1, 1)

	// Activation on A reads A's own (correct) file, but waits for B's heartbeat. If B reported the orphan, the row would
	// list 2 and activation would succeed.
	pathA := keyFile(t, 'm')
	putFileKey(t, pathA, 2, owner)
	rotationA := keyRotation(f.db, pathA)
	rotationRing, _, err := rotationA.Check(t.Context())
	if err != nil {
		t.Fatalf("node A check: %v", err)
	}
	versions, active := ring.Report()
	f.execSQL(t, fmt.Sprintf(
		"INSERT INTO platform.nodes (id, seen_at, key_versions, key_active, key_active_since) VALUES ('b', now(), '%s', %d, now())",
		versionArray(versions), active))
	if err := rotationA.Activate(t.Context(), 2); err == nil || !strings.Contains(err.Error(), "do not report version 2") {
		t.Fatalf("activation accepted a node that cannot read version 2: %v", err)
	}

	// Replacing the orphan with the registered material lets B confirm and report it, and activation proceeds.
	putFileKey(t, f.path, 2, owner)
	if _, err := f.r.Refresh(t.Context(), ring); err != nil {
		t.Fatalf("refresh after repairing the file: %v", err)
	}
	wantReport(t, ring, 1, 1, 2)
	versions, active = ring.Report()
	f.execSQL(t, fmt.Sprintf(
		"UPDATE platform.nodes SET key_versions='%s', key_active=%d, key_active_since=now() WHERE id='b'",
		versionArray(versions), active))
	if err := rotationA.Activate(t.Context(), 2); err != nil {
		t.Fatalf("activation after the node confirmed version 2: %v", err)
	}
	if status, err := rotationA.Refresh(t.Context(), rotationRing); err != nil || status.Active != 2 {
		t.Fatalf("node A did not install the activation: %+v, %v", status, err)
	}
}

// TestKeyringRetirementRefusesStaleWrites: a writer that sealed under version 1 before the retirement and stores it
// after must fail, not overwrite a rewrapped row with ciphertext whose key is gone.
func TestKeyringRetirementRefusesStaleWrites(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	stale := f.before[1].sealed
	f.addAndActivate()
	f.run("rewrap")
	f.run("retire", "--version", "1")
	err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE suppliers.supplier_logins
			SET ciphertext=$1, nonce=$2, wrapped_dek=$3, key_version=$4 WHERE supplier_id=$5`,
			stale.Ciphertext, stale.Nonce, stale.WrappedDEK, int64(stale.KeyVersion), f.before[1].id)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "is retired") {
		t.Fatalf("a stale version 1 write after retirement: %v", err)
	}
	if got := f.read(f.before[1]); got.KeyVersion != 2 {
		t.Fatalf("the refused write changed the stored version to %d", got.KeyVersion)
	}
	// The same fence must see the marker under a tenant scope, not only the platform scope a maintenance command uses.
	// The scope carries the billing account, not the user id, so read the pair the policy compares.
	account := f.before[0]
	owner := f.text("SELECT site_id::text || '|' || account_id::text FROM accounts.users WHERE id = '" + account.id + "'")
	site, billing, _ := strings.Cut(owner, "|")
	err = f.db.Tx(t.Context(), pg.AccountScopeIn(uuid.MustParse(site), uuid.MustParse(billing)), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE accounts.users
			SET totp_ciphertext=$1, totp_nonce=$2, totp_wrapped_dek=$3, totp_key_version=$4 WHERE id=$5`,
			account.sealed.Ciphertext, account.sealed.Nonce, account.sealed.WrappedDEK,
			int64(account.sealed.KeyVersion), account.id)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "is retired") {
		t.Fatalf("a stale version 1 write under an account scope after retirement: %v", err)
	}
}

// TestKeyringRetirementWaitsForAnOpenEnvelopeWrite: a writer that already holds the fence shared keeps the retirement
// out until it commits; the retirement then sees its envelope and refuses instead of deleting the key it needs.
func TestKeyringRetirementWaitsForAnOpenEnvelopeWrite(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	stale := f.before[1].sealed
	f.addAndActivate()
	f.run("rewrap")

	setup := make(chan error, 1)
	written := make(chan error, 1)
	release := make(chan struct{})
	go func() {
		written <- f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE suppliers.supplier_logins
				SET ciphertext=$1, nonce=$2, wrapped_dek=$3, key_version=$4 WHERE supplier_id=$5`,
				stale.Ciphertext, stale.Nonce, stale.WrappedDEK, int64(stale.KeyVersion), f.before[1].id); err != nil {
				setup <- err
				return err
			}
			setup <- nil
			<-release
			return nil
		})
	}()
	if err := <-setup; err != nil {
		close(release)
		t.Fatalf("the writer before retirement: %v", err)
	}

	// A retirement that does not block on the writer's shared lock would finish here, long before the deadline. Timeout
	// is the assertion that it is actually waiting, not a sleep that hopes it started.
	blocked, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	err := f.r.Retire(blocked, 1)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("retirement returned %v while a writer held the fence", err)
	}
	close(release)
	if err := <-written; err != nil {
		t.Fatalf("the writer after retirement was fenced: %v", err)
	}
	if err := f.r.Retire(t.Context(), 1); !errors.Is(err, secret.ErrReferenced) {
		t.Fatalf("retirement after the writer committed: %v, want a reference", err)
	}
	if got := f.read(f.before[1]); got.KeyVersion != 1 {
		t.Fatalf("the committed writer's version is %d, want 1", got.KeyVersion)
	}
}

// TestKeyringWillNotPruneAnUnfencedRetirement: only the tombstone writes a retirement marker, and the marker is what
// proves no writer can still use the version. A row edited to retired by hand has no such proof, so the key stays in
// the file even though no envelope references it.
func TestKeyringWillNotPruneAnUnfencedRetirement(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.addAndActivate()
	f.run("rewrap")
	f.execSQL(t, "UPDATE platform.key_versions SET state='retired' WHERE version=1")
	if _, _, err := f.r.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "no retirement fence") {
		t.Fatalf("check with a hand-retired version: %v", err)
	}
	contents, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "1:") {
		t.Fatal("the key file lost version 1 without a retirement fence")
	}
}

// TestKeyringMissingEnvelopeFenceFailsClosed: a registered store without its trigger would let a stale write bypass the
// retirement marker, so the startup check refuses the database instead of trusting that every migration was applied.
func TestKeyringMissingEnvelopeFenceFailsClosed(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	if err := pgtest.Run(t.Context(), f.database.Superuser,
		"DROP TRIGGER fence_envelope_key_version ON suppliers.supplier_logins"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.r.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "no retirement fence") {
		t.Fatalf("check without an envelope fence: %v", err)
	}
}
