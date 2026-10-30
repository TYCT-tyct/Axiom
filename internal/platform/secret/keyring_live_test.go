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

func wantLiveConcurrency(t *testing.T, ring *Keyring, snapshots ...*keySnapshot) {
	t.Helper()
	stop, installed := startInstaller(ring, snapshots)
	runLiveReaders(t, ring)
	close(stop)
	<-installed
}

// startInstaller keeps replacing the whole snapshot until stop is closed. It runs concurrently with the readers so a
// non-atomic replacement is a race the detector sees, not a value written before them.
func startInstaller(ring *Keyring, snapshots []*keySnapshot) (chan struct{}, <-chan struct{}) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				ring.install(snapshots[i%len(snapshots)])
			}
		}
	}()
	return stop, done
}

func runLiveReaders(t *testing.T, ring *Keyring) {
	t.Helper()
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for range 100 {
				if err := liveRoundTrip(ring); err != nil {
					t.Error(err)
					return
				}
				versions, active := ring.Report()
				if !liveReport(versions, active) {
					t.Errorf("a concurrent reload reported versions %v active %d, want [1 2] and 1 or 2", versions, active)
					return
				}
			}
		})
	}
	readers.Wait()
}

func liveRoundTrip(ring *Keyring) error {
	location := Location{Table: "t", RowID: "3", Purpose: "p"}
	sealed := ring.Seal(New("concurrent"), location)
	_, err := ring.Open(sealed, location)
	return err
}

func liveReport(versions []uint32, active uint32) bool {
	return len(versions) == 2 && versions[0] == 1 && versions[1] == 2 && (active == 1 || active == 2)
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
	fresh := snapshot.withConfirmed([]uint32{1, 2})
	ring.install(fresh.withActive(2))
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
	wantLiveConcurrency(t, ring, fresh.withActive(1), fresh.withActive(2))
}
