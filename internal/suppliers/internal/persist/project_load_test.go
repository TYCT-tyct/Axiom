//go:build load

package persist_test

import (
	"context"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/store"
)

func BenchmarkProjection10k(b *testing.B) {
	db, supplier := projectionDB(b)
	w := newProjection(supplier, 10000)
	for name, apply := range map[string]func(context.Context, store.DBTX, store.ProjectionWrites) error{
		"row":   rowProjection,
		"batch": func(ctx context.Context, db store.DBTX, w store.ProjectionWrites) error { return w.Apply(ctx, db) },
	} {
		b.Run(name, func(b *testing.B) {
			var total time.Duration
			for b.Loop() {
				elapsed, statements, err := measureProjection(b.Context(), db, w, apply)
				if err != nil {
					b.Fatal(err)
				}
				total += elapsed
				b.ReportMetric(float64(statements), "statements/op")
			}
			b.ReportMetric(float64(total.Nanoseconds())/float64(b.N), "write-ns/op")
		})
	}
}
