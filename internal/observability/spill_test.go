package observability

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// TestSpillDirectoryIsLockedByOneProcess: two nodes pointed at the same spill directory would append from the same
// offset and overwrite each other, with both Commits returning success. The second process is refused instead.
func TestSpillDirectoryIsLockedByOneProcess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSpillLockHelper$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), spillLockHelperEnv+"="+dir)
	// The helper holds the spill until its stdin closes; the pipe stays open until the kill below.
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	t.Cleanup(func() {
		if !killed {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("the lock helper said %q (%v), want ready", ready, err)
	}
	if sp, err := openSpill(dir, 4); !errors.Is(err, errSpillLocked) {
		if sp != nil {
			sp.finish()
		}
		t.Fatalf("a second process opened a locked spill: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	killed = true
	_ = cmd.Wait()
	sp, err := openSpill(dir, 4)
	if err != nil {
		t.Fatalf("opening the spill after the owner exited: %v", err)
	}
	sp.finish()
}

const spillLockHelperEnv = "AXIOM_TEST_SPILL_LOCK_DIR"

// TestSpillLockHelper is the second process of TestSpillDirectoryIsLockedByOneProcess: it opens the spill and holds it
// until the parent kills it or closes its stdin. Outside that test it does nothing.
func TestSpillLockHelper(t *testing.T) {
	dir := os.Getenv(spillLockHelperEnv)
	if dir == "" {
		t.Skip("only runs as the helper process of TestSpillDirectoryIsLockedByOneProcess")
	}
	if _, err := openSpill(dir, 4); err != nil {
		fmt.Fprintf(os.Stderr, "the helper could not open the spill: %v\n", err)
		os.Exit(2)
	}
	fmt.Println("ready")
	if err := os.Stdout.Sync(); err != nil {
		os.Exit(2)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// TestInFlightCommitSurvivesFatalBatch: A's record makes the database refuse the batch after B has already reserved a
// slot. The writer must stop database writes without closing the append file under B: B's call finishes its upstream
// work later, and its record has room and a healthy disk.
func TestInFlightCommitSurvivesFatalBatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	records := calls(2)
	s := &sink{poison: records[0].ID.String()}
	first := recorderOf(t, s, Config{
		Dir: dir, Capacity: 4, BatchSize: 1, FlushInterval: time.Millisecond, RetryBase: time.Millisecond,
		RetryMax: time.Millisecond,
	})
	slotA, err := first.Admit()
	if err != nil {
		t.Fatal(err)
	}
	slotB, err := first.Admit()
	if err != nil {
		t.Fatal(err)
	}
	if err := slotA.Commit(records[0]); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the database to refuse A's record", func() bool { return first.broken.Load() })
	if err := slotB.Commit(records[1]); err != nil {
		t.Fatalf("B's Commit after the fatal batch = %v, want the record kept", err)
	}
	select {
	case <-first.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the writer did not close the spilling phase after the last in-flight call finished")
	}

	nextSink := &sink{}
	next := recorderOf(t, nextSink, Config{Dir: dir, Capacity: 4, BatchSize: 1, FlushInterval: time.Millisecond})
	if err := next.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := nextSink.stored(); !slices.Equal(got, ids(records)) {
		t.Errorf("replayed %v, want both records %v: the in-flight record was lost", got, ids(records))
	}
}

// TestCompleteTailCorruptionRefusesStartup: the last record is complete (its length and body are all there) but its
// checksum does not match. That is damage, not an unfinished append, so startup refuses instead of truncating a record
// that may already have been served.
func TestCompleteTailCorruptionRefusesStartup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sp, err := openSpill(dir, 4)
	if err != nil {
		t.Fatal(err)
	}
	call, attempts, err := newRows(record(uuid.NewV7(), now))
	if err != nil {
		t.Fatal(err)
	}
	body, err := encodeRecord(call, attempts)
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.appendSynced(body); err != nil {
		t.Fatal(err)
	}
	sp.finish()

	path := onlySegment(t, dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openSpill(dir, 4); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("opening a spill whose complete last record is corrupt = %v, want a checksum refusal", err)
	}
	if after, err := os.Stat(path); err != nil || after.Size() != info.Size() {
		t.Errorf("the corrupt record was truncated or removed: size %d, was %d (%v)", after.Size(), info.Size(), err)
	}
}

// TestCheckpointSkipsAcknowledgedRecordsOnRestart: the active segment holds records the database already has followed
// by records it does not. The checkpoint is inside that segment, so the next start replays only the unacknowledged
// tail; replaying the prefix after its month was archived would insert it into the default partition as a duplicate.
func TestCheckpointSkipsAcknowledgedRecordsOnRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	records := calls(4)
	first := recorderOf(t, &sink{poison: records[2].ID.String()}, Config{
		Dir: dir, Capacity: 10, BatchSize: 2, FlushInterval: time.Hour, RetryBase: time.Millisecond,
		RetryMax: time.Millisecond,
	})
	enqueueAll(t, first, records)
	eventually(t, "the second batch to be refused", func() bool { return first.broken.Load() })
	crash(first)

	nextSink := &sink{gate: make(chan struct{})} // held until the count is read, so no batch is acknowledged first
	next := recorderOf(t, nextSink, Config{Dir: dir, Capacity: 10, BatchSize: 2, FlushInterval: time.Millisecond})
	if got := next.spill.pendingCount(); got != 2 {
		t.Fatalf("%d records pending after restart, want the 2 unacknowledged ones", got)
	}
	nextSink.open()
	if err := next.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := nextSink.stored(); !slices.Equal(got, ids(records[2:])) {
		t.Errorf("replayed %v, want only %v", got, ids(records[2:]))
	}
}

// TestAcknowledgedRecordsAreReclaimed: a quiet node must not keep already-stored records in its active segment until it
// fills. When the last record is acknowledged, the segment is rotated away and deleted.
func TestAcknowledgedRecordsAreReclaimed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := recorderOf(t, &sink{}, Config{Dir: dir, Capacity: 4, BatchSize: 1, FlushInterval: time.Millisecond})
	enqueueAll(t, r, calls(2))
	eventually(t, "the acknowledged segment to be reclaimed", func() bool {
		info, err := os.Stat(onlySegmentNoFail(dir))
		return err == nil && info.Size() == int64(len(spillHeader))
	})
	crash(r)

	nextSink := &sink{}
	next := recorderOf(t, nextSink, Config{Dir: dir, Capacity: 4, BatchSize: 1, FlushInterval: time.Millisecond})
	if err := next.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := nextSink.stored(); len(got) != 0 {
		t.Errorf("replayed %v after every record was acknowledged", got)
	}
}

// onlySegmentNoFail is onlySegment for a poll inside eventually.
func onlySegmentNoFail(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if _, ok := spillSeq(entry.Name()); ok {
			return filepath.Join(dir, entry.Name())
		}
	}
	return ""
}

// TestBoundedRouteFitsTheColumn: the bound Commit checks is the column's. A route whose jsonb text is exactly the limit
// is accepted by Commit and by PostgreSQL, and its stored text is what jsonbTextLen predicted; one byte more is refused
// before the spill (TestEnqueueRejectsText). routing's TestRecordIsBounded keeps the largest legal route under the
// same bound.
func TestBoundedRouteFitsTheColumn(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	// {"ab":[1,1,…]} with n members is 3n+8 bytes as jsonb text: ": " after the key, ", " between the members.
	n := (maxRouteText - 8) / 3
	route := []byte(`{"ab":[` + strings.Repeat(`1,`, n-1) + `1]}`)
	if got := jsonbTextLen(string(route)); got != maxRouteText {
		t.Fatalf("the test route is %d bytes as jsonb text, want exactly %d", got, maxRouteText)
	}
	rec := record(uuid.NewV7(), now)
	rec.Route = route
	keep(t, db, rec)

	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		got, err := GetTrace(t.Context(), tx, rec.ID)
		if err != nil {
			return err
		}
		if len(got.Route) != maxRouteText {
			t.Errorf("the stored route is %d bytes of jsonb text, want the predicted %d", len(got.Route), maxRouteText)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestWriterReadsOnlyDurableRecords: the writer must not store and checkpoint past a record whose fsync has not
// finished. After a power loss that record can be gone from the segment while the checkpoint still names a position
// beyond it, and the next start would have to refuse.
func TestWriterReadsOnlyDurableRecords(t *testing.T) {
	t.Parallel()
	sp, err := openSpill(t.TempDir(), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.finish()
	call, attempts, err := newRows(record(uuid.NewV7(), now))
	if err != nil {
		t.Fatal(err)
	}
	body, err := encodeRecord(call, attempts)
	if err != nil {
		t.Fatal(err)
	}
	pos, err := sp.append(body)
	if err != nil {
		t.Fatal(err)
	}
	readable := func() bool {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		return sp.availableLocked()
	}
	if readable() {
		t.Fatal("the writer can read a record before its fsync")
	}
	if err := sp.sync(pos); err != nil {
		t.Fatal(err)
	}
	if !readable() {
		t.Fatal("the writer cannot read a record after its fsync")
	}
}
