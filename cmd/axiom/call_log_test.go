package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCallLogCheck: the check passes on a spill with nothing for the database, fails naming the count on one with a
// record left behind (a parked one here), and refuses other arguments.
func TestCallLogCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var out bytes.Buffer
	if err := callLogCommand([]string{"check", dir}, &out); err != nil || !strings.Contains(out.String(), "holds no call record") {
		t.Fatalf("an empty spill: %v, %q", err, out.String())
	}
	if err := os.MkdirAll(filepath.Join(dir, "pending"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pending", "r.rec"), []byte("record"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := callLogCommand([]string{"check", dir}, &out); err == nil || !strings.Contains(err.Error(), "holds 1 call records") {
		t.Fatalf("a spill with a parked record: %v", err)
	}
	if err := callLogCommand([]string{"drain", dir}, &out); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("another verb: %v", err)
	}
}
