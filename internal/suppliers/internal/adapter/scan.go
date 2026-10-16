package adapter

import (
	"time"

	"axiom/internal/platform/jsonx"
)

// Scan is the part of a ScanState every adapter shares: the span of charge records one pass covers. The sync engine
// decides it (the watermark less an overlap, up to a moment safely in the past) and begins the pass with BeginScan;
// an adapter keeps its own position in further members of the same JSON object (docs: internal/suppliers/README.md
// 「扣费记录的扫描」), which the engine does not read.
type Scan struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// BeginScan is the state of a pass that has read nothing yet.
func BeginScan(start, end time.Time) ScanState {
	b, err := jsonx.MarshalCanonical(Scan{Start: start.UTC(), End: end.UTC()})
	if err != nil {
		panic(err) // two timestamps always encode
	}
	return b
}
