package protocol_test

import (
	"fmt"
	"testing"
	"time"

	"axiom/internal/protocol"
)

// Report the size budget alongside the combined operation. Absolute timings are
// evidence, not a CI failure on a shared host (docs/performance.md「怎么验证」).
func BenchmarkRequestRead(b *testing.B) {
	for f := range protocol.Format(len(turns)) {
		for _, size := range []int{1 << 10, 256 << 10, 1 << 20, 4 << 20} {
			data := body(f, size, "")
			budget := 2 * time.Millisecond
			if size > 256<<10 {
				budget = time.Duration((size+(1<<20)-1)/(1<<20)) * 8 * time.Millisecond
			}
			b.Run(fmt.Sprintf("format=%d/size=%d", f, size), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				benchmarkRequestRead(b, f, data)
				b.ReportMetric(float64(budget.Nanoseconds()), "budget-ns/op")
				b.ReportMetric(100*float64(b.Elapsed())/float64(b.N)/float64(budget), "%-budget")
			})
		}
	}
}

func benchmarkRequestRead(b *testing.B, f protocol.Format, data []byte) {
	b.Helper()
	for b.Loop() {
		r, err := protocol.Inspect(f, data)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := protocol.Prepare(&r, data, protocol.Target{Format: f, Model: "up"}, protocol.Rewrites{}); err != nil {
			b.Fatal(err)
		}
	}
}
