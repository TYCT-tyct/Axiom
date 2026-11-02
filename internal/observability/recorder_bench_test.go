package observability

import (
	"context"
	"testing"
	"time"
	"uuid"
)

// BenchmarkEnqueue measures the call-path cost of reserving and appending one record: admission, encoding and the
// segment write. The writer drains in the background; the database is a sink, so this is the part a call pays for.
func BenchmarkEnqueue(b *testing.B) {
	// The writer is held in its first database write so this measures what a call pays: admission, encoding and the
	// segment append. The background drain is measured by the end-to-end tests, not here.
	s := &sink{gate: make(chan struct{})}
	r, err := newRecorder(context.Background(), Config{
		Dir: b.TempDir(), Capacity: b.N + 1, BatchSize: 500, FlushInterval: time.Millisecond,
		RetryBase: time.Millisecond, RetryMax: time.Millisecond, WriteTimeout: time.Second,
	}, s.write, func(error) bool { return false }, quiet)
	if err != nil {
		b.Fatal(err)
	}
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
	s := &sink{gate: make(chan struct{})} // the writer waits, so this is only admission, encoding and the group commit
	r, err := newRecorder(context.Background(), Config{
		Dir: b.TempDir(), Capacity: b.N + 1, BatchSize: 500, FlushInterval: time.Millisecond,
		RetryBase: time.Millisecond, RetryMax: time.Millisecond, WriteTimeout: time.Second,
	}, s.write, func(error) bool { return false }, quiet)
	if err != nil {
		b.Fatal(err)
	}
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
	sp, err := openSpill(b.TempDir(), b.N+1)
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
		if err := sp.appendSynced(body); err != nil {
			b.Fatal(err)
		}
	}
}
