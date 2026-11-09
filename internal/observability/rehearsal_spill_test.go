package observability

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

var update = flag.Bool("update", false, "rewrite the spills in testdata/spill")

// The spills the backup rehearsal puts on a stopped node (scripts/ops/rehearse.sh): behind is a node whose writer fell
// behind, three settled records and one parked pending record that the database never got; after holds two records of
// calls served after the set the rehearsal restores. go test -update writes new ones from this version's recorder.
var rehearsalSpills = []struct {
	name             string
	settled, pending int
}{{"behind", 3, 1}, {"after", 2, 0}}

// TestRehearsalSpillsReplay: each spill in testdata/spill, written by an earlier version, still counts as its records
// and replays into the database on a node of this version, which is what the rehearsal and a rolling release rely on.
func TestRehearsalSpillsReplay(t *testing.T) {
	t.Parallel()
	if *update {
		for _, s := range rehearsalSpills {
			writeRehearsalSpill(t, s.name, s.settled, s.pending)
		}
	}
	db := openDB(t)
	var total int64
	for _, s := range rehearsalSpills {
		dir := t.TempDir()
		if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "spill", s.name))); err != nil {
			t.Fatal(err)
		}
		want := int64(s.settled + s.pending)
		if n, err := SpillBacklog(dir); n != want || err != nil {
			t.Fatalf("%s: backlog %d, %v; want %d", s.name, n, err, want)
		}
		if err := recorderOfDB(t, db, Config{Dir: dir, BatchSize: 10, FlushInterval: time.Hour}).Close(t.Context()); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		total += want
		var stored int64
		if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM observability.call_log`).Scan(&stored)
		}); err != nil || stored != total {
			t.Fatalf("%s: %d calls stored, %v; want %d", s.name, stored, err, total)
		}
	}
}

// writeRehearsalSpill replaces testdata/spill/name with the spill a node leaves that accepted settled and pending
// records and stopped before its database took any: the segments and the parked records, without the lock file.
func writeRehearsalSpill(t *testing.T, name string, settled, pending int) {
	t.Helper()
	dir := t.TempDir()
	r := recorderOf(t, &sink{failing: 1 << 30}, Config{
		Dir: dir, BatchSize: 10, FlushInterval: time.Hour, RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond,
	})
	enqueueAll(t, r, calls(settled))
	for range pending {
		commit(t, admit(t, r), pendingOf(record(uuid.NewV7(), now)))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := r.Close(ctx); !errors.Is(err, ErrPending) {
		t.Fatalf("Close with the database down = %v, want ErrPending", err)
	}
	if err := os.Remove(filepath.Join(dir, spillLockName)); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join("testdata", "spill", name)
	if err := os.RemoveAll(out); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(out, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
}
