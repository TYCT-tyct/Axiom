package observability

import (
	"context"
	"sync"
	"testing"
	"time"
	"uuid"
)

// parkedRecorder starts a recorder whose writer is stopped in its first database write before the benchmark's timer
// starts, and returns the sink that releases it. Otherwise the writer reads and decodes the first records the loop
// appends — as many as arrive within its flush interval, so their number depends on scheduling — inside the measured
// window, and that background work divided by the iteration count made one commit read 43 to 86 allocs/op. Here the
// writer reads a fixed batch of one record before the timer; the loop then pays only what a call pays: admission,
// validation, encoding, the segment append and the group-commit fsync.
func parkedRecorder(b *testing.B, capacity int) (*Recorder, *sink) {
	b.Helper()
	s := &sink{gate: make(chan struct{})}
	begun := make(chan struct{})
	var once sync.Once
	r, err := newRecorder(context.Background(), Config{
		Dir: b.TempDir(), Capacity: capacity, BatchSize: 500, FlushInterval: time.Millisecond,
		// The parked write must outlast the loop however long the machine takes: a timeout would retry it, with a timer
		// and a log line, inside the measured window.
		RetryBase: time.Millisecond, RetryMax: time.Millisecond, WriteTimeout: time.Hour,
	}, func(ctx context.Context, batch []queued) error {
		once.Do(func() { close(begun) })
		return s.write(ctx, batch)
	}, func(error) bool { return false }, quiet)
	if err != nil {
		b.Fatal(err)
	}
	// One record, distinct from the one the loop commits: the writer keeps what it read, and the loop must not be
	// measured on a record whose rows the writer rebuilt.
	if err := r.Enqueue(record(uuid.NewV7(), now)); err != nil {
		b.Fatal(err)
	}
	<-begun
	return r, s
}

// BenchmarkEnqueue measures the call-path cost of reserving and appending one record: admission, encoding and the
// segment write. The writer drains in the background; the database is a sink, so this is the part a call pays for.
func BenchmarkEnqueue(b *testing.B) {
	// The capacity leaves room for the writer's one parked record beside the loop's.
	r, s := parkedRecorder(b, b.N+2)
	rec := record(uuid.NewV7(), now)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := r.Enqueue(rec); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	s.open()
	if err := r.Close(context.Background()); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkEnqueueParallel measures the same path under concurrency, where the group commit shares one fsync between
// the calls that appended before it. The writer drains into a sink; the capacity leaves room for the in-flight calls.
func BenchmarkEnqueueParallel(b *testing.B) {
	r, s := parkedRecorder(b, b.N+2)
	rec := record(uuid.NewV7(), now)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := r.Enqueue(rec); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.StopTimer()
	s.open()
	if err := r.Close(context.Background()); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkEncodeRecord is the CPU half of Commit: validation and canonical JSON, without the segment write.
func BenchmarkEncodeRecord(b *testing.B) {
	rec := record(uuid.NewV7(), now)
	call, attempts, err := newRows(rec)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := encodeRecord(call, attempts); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSpillAppend is the I/O half of Commit: one append and its group-commit fsync.
func BenchmarkSpillAppend(b *testing.B) {
	sp, err := openSpill(b.TempDir(), b.N+1, syncDir)
	if err != nil {
		b.Fatal(err)
	}
	defer sp.finish()
	rec := record(uuid.NewV7(), now)
	call, attempts, err := newRows(rec)
	if err != nil {
		b.Fatal(err)
	}
	body, err := encodeRecord(call, attempts)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := sp.appendSynced(body, false); err != nil {
			b.Fatal(err)
		}
	}
}
