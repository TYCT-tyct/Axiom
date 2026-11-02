package observability

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform"
)

// TestEnqueueRejectsText: text that cannot be stored is refused at the boundary and marks the recorder unrecordable;
// no other call may be served after it.
func TestEnqueueRejectsText(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(*CallRecord){
		"bad UTF-8 in the client app":        func(c *CallRecord) { c.ClientApp = "\xff" },
		"a NUL in the model":                 func(c *CallRecord) { c.Model = "a\x00b" },
		"bad UTF-8 in an attempt's egress":   func(c *CallRecord) { c.Attempts[0].Egress = "\xff" },
		"a NUL in an attempt's error kind":   func(c *CallRecord) { c.Attempts[1].ErrorKind = "a\x00b" },
		"an attempt with an unknown outcome": func(c *CallRecord) { c.Attempts[0].Outcome = "maybe" },
	} {
		r := recorderOf(t, &sink{}, Config{Capacity: 1, BatchSize: 1, FlushInterval: time.Millisecond})
		rec := record(uuid.NewV7(), now)
		edit(&rec)
		if err := r.Enqueue(rec); err == nil {
			t.Errorf("%s was accepted", name)
		}
		if _, err := r.Admit(); !errors.Is(err, ErrBroken) {
			t.Errorf("%s left admission open (%v)", name, err)
		}
	}
}

// TestEnqueueRejectsAnIncompleteAttempt: the log cannot say what happened without an endpoint, a supplier, times or a
// sequence that runs from one; such a record is refused before it reaches the spill.
func TestEnqueueRejectsAnIncompleteAttempt(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(*CallRecord){
		"a nil endpoint":      func(c *CallRecord) { c.Attempts[0].Endpoint = uuid.Nil() },
		"a nil supplier":      func(c *CallRecord) { c.Attempts[1].Supplier = uuid.Nil() },
		"no start time":       func(c *CallRecord) { c.Attempts[0].StartedAt = time.Time{} },
		"finish before start": func(c *CallRecord) { c.Attempts[1].FinishedAt = c.Attempts[1].StartedAt.Add(-time.Second) },
		"a sequence gap":      func(c *CallRecord) { c.Attempts[1].Seq = 3 },
		"too many attempts":   func(c *CallRecord) { *c = withAttempts(*c, platform.MaxAttemptSeq+1) },
	} {
		r := recorderOf(t, &sink{}, Config{Capacity: 1, BatchSize: 1, FlushInterval: time.Millisecond})
		rec := record(uuid.NewV7(), now)
		edit(&rec)
		if err := r.Enqueue(rec); err == nil {
			t.Errorf("%s was accepted", name)
		}
		if _, err := r.Admit(); !errors.Is(err, ErrBroken) {
			t.Errorf("%s left admission open (%v)", name, err)
		}
	}
}

// TestHungWriteTimesOutAndRecovers: a write that never answers is cut off after WriteTimeout and retried, so the writer
// is not held for good by one dead connection.
func TestHungWriteTimesOutAndRecovers(t *testing.T) {
	t.Parallel()
	hung := make(chan struct{})
	var once sync.Once
	s := &sink{}
	write := func(ctx context.Context, batch []queued) error {
		blocked := false
		once.Do(func() { blocked = true })
		if blocked {
			close(hung)
			<-ctx.Done()
			return ctx.Err()
		}
		return s.write(ctx, batch)
	}
	r, err := newRecorder(context.Background(), Config{
		Dir: t.TempDir(), Capacity: 4, BatchSize: 2, FlushInterval: time.Millisecond, RetryBase: time.Millisecond,
		RetryMax: 2 * time.Millisecond, WriteTimeout: 30 * time.Millisecond,
	}, write, func(error) bool { return false }, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { crash(r) })
	records := calls(2)
	enqueueAll(t, r, records)
	<-hung
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.stored(); !slices.Equal(got, ids(records)) {
		t.Errorf("stored %v after the hung write, want %v", got, ids(records))
	}
	if n := s.attemptsStored(); n != 4 {
		t.Errorf("%d attempts stored after the hung write, want 4", n)
	}
}

// TestStopRefusesFurtherAppends guards the shutdown deadline: once the writer is told to stop, a commit that was
// admitted earlier must get ErrClosed instead of touching the files the writer closed.
func TestStopRefusesFurtherAppends(t *testing.T) {
	s, err := openSpill(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.finish)
	s.stopNow()
	if err := s.append([]byte("{}")); !errors.Is(err, ErrClosed) {
		t.Errorf("append after stop = %v, want ErrClosed", err)
	}
}
