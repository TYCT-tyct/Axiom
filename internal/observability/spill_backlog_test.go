package observability

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"
)

// TestSpillBacklog: the backlog is what a node that stops now leaves for the database: the records after the
// checkpoint and the parked ones, none once a Close drained the spill, and not a torn tail. Counting changes nothing in
// the directory, and damage is an error rather than a number.
func TestSpillBacklog(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  int64
		err   string
	}{
		{name: "an unused directory", setup: func(*testing.T, string) {}},
		{name: "a drained spill", setup: drained},
		{name: "a writer that fell behind", want: 5, setup: func(t *testing.T, dir string) { behind(t, dir, 5) }},
		{name: "a torn tail", want: 4, setup: func(t *testing.T, dir string) {
			behind(t, dir, 5)
			truncateBy(t, onlySegment(t, dir), 3)
		}},
		{name: "a parked record", want: 1, setup: parked},
		{name: "a damaged record", err: "checksum mismatch", setup: damaged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			tc.setup(t, dir)
			before := tree(t, dir)
			n, err := SpillBacklog(dir)
			switch {
			case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
				t.Fatalf("SpillBacklog = %d, %v; want an error with %q", n, err, tc.err)
			case tc.err == "" && (n != tc.want || err != nil):
				t.Fatalf("SpillBacklog = %d, %v; want %d", n, err, tc.want)
			}
			if after := tree(t, dir); !maps.Equal(before, after) {
				t.Errorf("counting changed the directory: %v, was %v", after, before)
			}
		})
	}
}

// drained is a spill whose five records a Close wrote.
func drained(t *testing.T, dir string) {
	t.Helper()
	r := recorderOf(t, &sink{}, Config{Dir: dir, BatchSize: 2, FlushInterval: time.Hour})
	enqueueAll(t, r, calls(5))
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// parked is a drained spill that also holds one parked pending record.
func parked(t *testing.T, dir string) {
	t.Helper()
	r := recorderOf(t, &sink{}, Config{Dir: dir, BatchSize: 10, FlushInterval: time.Hour})
	enqueueAll(t, r, calls(2))
	commit(t, admit(t, r), pendingOf(record(uuid.NewV7(), now)))
	if err := r.Close(t.Context()); !errors.Is(err, ErrPending) {
		t.Fatalf("Close with a parked record = %v, want ErrPending", err)
	}
}

// damaged is a spill whose last complete record fails its checksum.
func damaged(t *testing.T, dir string) {
	t.Helper()
	behind(t, dir, 2)
	path := onlySegment(t, dir)
	data, err := os.ReadFile(path) //nolint:gosec // the test's own temporary spill
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // the test's own temporary spill
		t.Fatal(err)
	}
}

// behind leaves n records in dir's spill with none of them written, as a node does that stops while its database is
// down.
func behind(t *testing.T, dir string, n int) {
	t.Helper()
	r := recorderOf(t, &sink{failing: 1 << 30}, Config{
		Dir: dir, BatchSize: 10, FlushInterval: time.Hour, RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond,
	})
	enqueueAll(t, r, calls(n))
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := r.Close(ctx); !errors.Is(err, ErrPending) {
		t.Fatalf("Close with the database down = %v, want ErrPending", err)
	}
}

// truncateBy cuts n bytes off the end of path, as a crash in the middle of an append does.
func truncateBy(t *testing.T, path string, n int64) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-n); err != nil {
		t.Fatal(err)
	}
}

// tree is every file under dir with its size and modification time.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[path] = fmt.Sprintf("%d %s %s", info.Size(), info.Mode(), info.ModTime())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
