package secret

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeLiveFile(t *testing.T, path string, versions ...uint32) {
	t.Helper()
	var contents strings.Builder
	for _, version := range versions {
		key := make([]byte, 32)
		binary.BigEndian.PutUint32(key[28:], version) // distinct material per version, without a narrowing conversion
		fmt.Fprintf(&contents, "%d:%s\n", version, base64.StdEncoding.EncodeToString(key))
	}
	if err := os.WriteFile(path, []byte(contents.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func wantLiveRing(t *testing.T, ring *Keyring, active uint32) {
	t.Helper()
	versions, reported := ring.Report()
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("versions %v, want [1 2]", versions)
	}
	if reported != active {
		t.Fatalf("reported write version %d, want %d", reported, active)
	}
}

func wantLiveConcurrency(t *testing.T, ring *Keyring) {
	t.Helper()
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 100 {
				sealed := ring.Seal(New("concurrent"), Location{Table: "t", RowID: "3", Purpose: "p"})
				if _, err := ring.Open(sealed, Location{Table: "t", RowID: "3", Purpose: "p"}); err != nil {
					t.Error(err)
					return
				}
				_, _ = ring.Report()
			}
		})
	}
	group.Wait()
}

// TestLiveRingInstallsOneWholeSnapshot: a reload replaces the versions and write version together, calls already using
// the old snapshot are unaffected, and new seals use the new write version. The race detector is the concurrency
// assertion: sealing, opening and reporting run while the snapshot is replaced.
func TestLiveRingInstallsOneWholeSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master")
	writeLiveFile(t, path, 1)
	ring, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	old := ring.Seal(New("before"), Location{Table: "t", RowID: "1", Purpose: "p"})
	writeLiveFile(t, path, 1, 2)
	snapshot, err := loadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	ring.install(snapshot.withActive(2))
	wantLiveRing(t, ring, 2)
	if opened, err := ring.Open(old, Location{Table: "t", RowID: "1", Purpose: "p"}); err != nil || opened.Reveal() != "before" {
		t.Fatalf("an old envelope stopped opening: %v", err)
	}
	if sealed := ring.Seal(New("after"), Location{Table: "t", RowID: "2", Purpose: "p"}); sealed.KeyVersion != 2 {
		t.Fatalf("a new seal used version %d, want 2", sealed.KeyVersion)
	}
	if active := snapshot.withActive(1); active.latest != 1 || snapshot.latest != 2 {
		t.Fatal("withActive changed the source snapshot instead of copying it")
	}
	wantLiveConcurrency(t, ring)
}
