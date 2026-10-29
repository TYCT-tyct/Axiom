package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

// The cluster's live connection must keep the command's reaper alive even when none of its test packages is running.
func TestCommandSharesTheClusterReaperSession(t *testing.T) {
	const sessionFileEnv = "AXIOM_WITHCLUSTER_SESSION_FILE"
	if file := os.Getenv(sessionFileEnv); file != "" {
		if err := os.WriteFile(file, []byte(testcontainers.SessionID()), 0o600); err != nil { // #nosec G703 -- the parent test passes its own temporary file
			t.Fatal(err)
		}
		return
	}
	if testcontainers.ReadConfig().Config.RyukDisabled {
		t.Skip("Ryuk is disabled; there is no reaper session to share")
	}
	// Even an inherited entry must give way to the session the cluster actually uses.
	t.Setenv("TESTCONTAINERS_SESSION_ID", testcontainers.SessionID()+"-child")
	file := filepath.Join(t.TempDir(), "session")
	t.Setenv(sessionFileEnv, file)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if status := run([]string{executable, "-test.run=^TestCommandSharesTheClusterReaperSession$"}); status != 0 {
		t.Fatalf("command exited with %d", status)
	}
	got, err := os.ReadFile(file) // #nosec G304 -- the test's own temporary file
	if err != nil {
		t.Fatal(err)
	}
	if want := testcontainers.SessionID(); string(got) != want {
		t.Fatalf("command's reaper session = %q, want cluster's %q", got, want)
	}
}
