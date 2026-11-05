package observability

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"
)

// TestAPendingRecordKeepsItsSlotsRoom: a pending record is parked in its slot's room, not in the spill: the slot then
// takes one settled record of the call, which replaces the parked one, and Release moves a parked record whose
// settlement went to the cleanup into the spill, in the same room. One record of each call reaches the writer.
func TestAPendingRecordKeepsItsSlotsRoom(t *testing.T) {
	t.Parallel()
	s := &sink{gate: make(chan struct{})}
	r := recorderOf(t, s, Config{Capacity: 2, BatchSize: 10, FlushInterval: time.Hour})
	settled, left := record(uuid.NewV7(), now), record(uuid.NewV7(), now)
	first, second := admit(t, r), admit(t, r)
	commit(t, first, pendingOf(settled))
	commit(t, second, pendingOf(left))
	if _, err := r.Admit(); !errors.Is(err, ErrBacklog) {
		t.Fatalf("Admit with two calls parked = %v, want ErrBacklog", err)
	}
	if err := first.Commit(pendingOf(settled)); !errors.Is(err, ErrClosed) {
		t.Errorf("a second pending Commit = %v, want ErrClosed", err)
	}
	if n := r.parked.Load(); n != 2 {
		t.Fatalf("%d records parked, want 2", n)
	}
	commit(t, first, settled)
	first.Release() // after the settled Commit this must not free the writer's room
	second.Release()
	if n := r.parked.Load(); n != 0 {
		t.Errorf("%d records still parked, want none", n)
	}
	if _, err := r.Admit(); !errors.Is(err, ErrBacklog) {
		t.Fatalf("Admit with both records in the spill = %v, want ErrBacklog until the write", err)
	}
	s.open()
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.stored(); !slices.Equal(got, []string{settled.ID.String(), left.ID.String()}) {
		t.Errorf("stored %v, want the settled record, then the one left pending", got)
	}
}

// TestCloseReportsParkedRecords: a call whose record is parked has not reached the spill, let alone the database, so
// Close says records remain for the next start even when the spill itself is empty; the record is still parked.
func TestCloseReportsParkedRecords(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := recorderOf(t, &sink{}, Config{Dir: dir, BatchSize: 10, FlushInterval: time.Hour})
	slot := admit(t, r)
	rec := record(uuid.NewV7(), now)
	commit(t, slot, pendingOf(rec))
	if err := r.Close(t.Context()); !errors.Is(err, ErrPending) {
		t.Errorf("Close with a parked record = %v, want ErrPending", err)
	}
	slot.Release() // after Close the spill takes nothing: the record stays parked for the next start
	if b := readParked(t, dir, rec.ID); len(b) == 0 {
		t.Error("the parked record is gone")
	}
}

// TestPendingRecordsKeepOldBinariesOut: a pending record that enters the spill, moved there by Release or by the next
// start, goes into a segment whose header a binary that predates pending records refuses, so a rollback cannot store it
// as a settled call; once the spill drains, no such segment is left to hold a rollback back. oldBinaryStarts is that
// binary's header check, written out: it accepts "AXIOM-WAL-1\n" and nothing else.
func TestPendingRecordsKeepOldBinariesOut(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		move func(t *testing.T, dir string, r *Recorder, slot *Slot)
	}{
		{"moved by Release", func(_ *testing.T, _ string, _ *Recorder, slot *Slot) { slot.Release() }},
		{"moved by the next start", func(t *testing.T, dir string, r *Recorder, _ *Slot) {
			crash(r)
			crash(recorderOf(t, &sink{gate: make(chan struct{})}, Config{Dir: dir, BatchSize: 10, FlushInterval: time.Hour}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r := recorderOf(t, &sink{gate: make(chan struct{})}, Config{Dir: dir, BatchSize: 10, FlushInterval: time.Hour})
			commit(t, admit(t, r), record(uuid.NewV7(), now)) // a settled record first, in a segment old binaries read
			slot := admit(t, r)
			commit(t, slot, pendingOf(record(uuid.NewV7(), now)))
			tc.move(t, dir, r, slot)
			crash(r) // a no-op when the case already stopped it
			if oldBinaryStarts(t, dir) {
				t.Fatal("a binary that predates pending records would store the moved pending record")
			}
			s := &sink{}
			if err := recorderOf(t, s, Config{Dir: dir, BatchSize: 10, FlushInterval: time.Hour}).Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got, _ := s.stored(); len(got) != 2 {
				t.Errorf("the drain stored %d records, want both", len(got))
			}
			if !oldBinaryStarts(t, dir) {
				t.Error("the drained spill still holds a segment a binary that predates pending records refuses")
			}
		})
	}
}

// oldBinaryStarts reports whether every segment in dir has the one header a binary from before pending records accepts.
func oldBinaryStarts(t *testing.T, dir string) bool {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, ok := spillSeq(e.Name()); !ok {
			continue
		}
		b, err := root.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(b), "AXIOM-WAL-1\n") {
			return false
		}
	}
	return true
}

func admit(t *testing.T, r *Recorder) *Slot {
	t.Helper()
	slot, err := r.Admit()
	if err != nil {
		t.Fatal(err)
	}
	return slot
}
