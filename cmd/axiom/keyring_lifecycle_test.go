package main

import (
	"bytes"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"axiom/internal/platform/secret"
)

func mustLifecycleStep(t *testing.T, lifecycle *keyringLifecycle, phase string) {
	t.Helper()
	if err := lifecycle.step(t.Context()); err != nil {
		t.Fatalf("%s step: %v", phase, err)
	}
}

func mustRefreshStatus(t *testing.T, f rotationFixture, ring *secret.Keyring) secret.KeyStatus {
	t.Helper()
	status, err := f.r.Refresh(t.Context(), ring)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return status
}

func wantKeyStatus(t *testing.T, status secret.KeyStatus, active uint32, readable, retired int) {
	t.Helper()
	if status.Active != active || len(status.Readable) != readable || len(status.Retired) != retired {
		t.Fatalf("status %+v, want active %d with %d readable and %d retired", status, active, readable, retired)
	}
}

func wantFileVersions(t *testing.T, path string, want ...string) {
	t.Helper()
	contents, err := os.ReadFile(path) //nolint:gosec // The test's own key file.
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(contents)), "\n") {
		version, _, _ := strings.Cut(line, ":")
		got = append(got, version)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("file versions %v, want %v", got, want)
	}
}

func wantNoKeyMaterial(t *testing.T, logs *bytes.Buffer, material []byte) {
	t.Helper()
	for line := range strings.SplitSeq(strings.TrimSpace(string(material)), "\n") {
		_, key, _ := strings.Cut(line, ":")
		if strings.Contains(logs.String(), key) {
			t.Fatal("the lifecycle logged key material")
		}
	}
}

// TestKeyringLifecycleRotatesWhileNodesRun: a version written to the shared file is activated once every live node
// reports it, rewrapped in the background, and retired after its references and the quiet window are gone, all while
// another node keeps serving. The lifecycle only logs identifiers and counts; no key material reaches its output.
func TestKeyringLifecycleRotatesWhileNodesRun(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	ring, status, err := f.r.Check(t.Context())
	if err != nil {
		t.Fatalf("initial check: %v", err)
	}
	wantKeyStatus(t, status, 1, 1, 0)
	logs := &bytes.Buffer{}
	lifecycle := &keyringLifecycle{rotation: f.r, ring: ring, logger: slog.New(slog.NewTextHandler(logs, nil))}

	f.run("add", "--version", "2")
	// Read the file after the add: the scan below must cover the generated version too, not only the one that existed at
	// startup.
	material, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	// The other live node has reloaded the distribution file but is still writing version 1.
	f.execSQL(t, "INSERT INTO platform.nodes (id, seen_at, key_versions, key_active, key_active_since) VALUES ('other', now(), '{1,2}', 1, now())")
	mustLifecycleStep(t, lifecycle, "activation")
	wantKeyStatus(t, mustRefreshStatus(t, f, ring), 2, 2, 0)
	// Each step rewraps a bounded batch; the loop runs until a pass finds no old envelope.
	for range 3 {
		mustLifecycleStep(t, lifecycle, "rewrap")
	}
	if got := f.rewrapped(); got != "3" {
		t.Fatalf("rewrapped %s envelopes, want 3", got)
	}
	// The other node has now written version 2 for longer than the quiet window.
	f.execSQL(t, "UPDATE platform.nodes SET key_active=2, key_active_since=now() - make_interval(secs => 31)")
	mustLifecycleStep(t, lifecycle, "retirement")
	wantKeyStatus(t, mustRefreshStatus(t, f, ring), 2, 1, 1)
	wantFileVersions(t, f.path, "2")
	// With one readable version left, a step is one metadata read: the steady-state loop must not scan the envelope
	// tables or record another batch.
	batches := f.text("SELECT count(*)::text FROM platform.audit_log WHERE action='keyring.rewrap'")
	mustLifecycleStep(t, lifecycle, "idle")
	if got := f.text("SELECT count(*)::text FROM platform.audit_log WHERE action='keyring.rewrap'"); got != batches {
		t.Fatalf("an idle step rewrapped envelopes: %s batches, before %s", got, batches)
	}
	wantNoKeyMaterial(t, logs, material)
}
