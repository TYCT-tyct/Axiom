package alloctest_test

import (
	"testing"

	"axiom/internal/platform/alloctest"
)

// sink keeps the allocation of a measured function alive, so the runtime counts it.
var sink []byte

// TestMeasure: a function that allocates once per call reports one allocation; one that allocates nothing reports none.
func TestMeasure(t *testing.T) {
	got := alloctest.Measure(t,
		alloctest.Function{Name: "nothing", F: func() {}},
		alloctest.Function{Name: "one", F: func() { sink = make([]byte, 1) }},
	)
	if a := got["nothing"]; a.Count != 0 || a.Bytes != 0 {
		t.Errorf("a function that allocates nothing: %+v", a)
	}
	if a := got["one"]; a.Count != 1 || a.Bytes == 0 {
		t.Errorf("a function that allocates once: %+v", a)
	}
}

// TestMeasureInSubtest: the child runs only the subtest that asked, so each measures its own function.
func TestMeasureInSubtest(t *testing.T) {
	t.Run("nothing", func(t *testing.T) {
		if a := alloctest.Measure(t, alloctest.Function{Name: "f", F: func() {}}); a["f"].Count != 0 {
			t.Errorf("a function that allocates nothing: %+v", a["f"])
		}
	})
	t.Run("one", func(t *testing.T) {
		if a := alloctest.Measure(t, alloctest.Function{Name: "f", F: func() { sink = make([]byte, 1) }}); a["f"].Count != 1 {
			t.Errorf("a function that allocates once: %+v", a["f"])
		}
	})
}

// TestMeasureRetained: a result the caller keeps counts the heap it holds; a temporary the function drops before it
// returns does not, which is what an allocation count cannot tell apart.
func TestMeasureRetained(t *testing.T) {
	const size = 1 << 20
	got := alloctest.MeasureRetained(t,
		alloctest.RetainedFunction{Name: "nothing", F: func() any { return nil }},
		alloctest.RetainedFunction{Name: "temporary", F: func() any {
			sink = make([]byte, size)
			sink = nil
			return nil
		}},
		alloctest.RetainedFunction{Name: "held", F: func() any { return make([]byte, size) }},
	)
	if held := got["nothing"]; held != 0 {
		t.Errorf("a function that retains nothing: %d bytes", held)
	}
	if held := got["temporary"]; held > 64<<10 {
		t.Errorf("a temporary the function dropped retains %d bytes, want at most %d", held, 64<<10)
	}
	if held := got["held"]; held < size || held > 2*size {
		t.Errorf("a held 1 MiB result measures %d bytes, want between %d and %d", held, size, 2*size)
	}
	for name, held := range got {
		t.Logf("%s: %d bytes", name, held)
	}
}

// runtimeNoise is how far under what a function holds a peak may read: the runtime frees a few of its own objects
// between the two readings that measure what a function keeps (200 runs on 2026-11-16 read at most 5,632 bytes under
// a held 1 MiB). Memory the first reading counted and freed while the function ran, #754's mechanism, read 23–32 KB
// under before measurePeak took what a function keeps apart, and must still fail.
const runtimeNoise = 8 << 10

// TestMeasurePeak: every function is measured apart, and what one keeps after it returns is still part of its peak.
func TestMeasurePeak(t *testing.T) {
	const size = 1 << 20
	got := alloctest.MeasurePeak(t,
		alloctest.PeakFunction{Name: "nothing", F: func() any { return nil }},
		alloctest.PeakFunction{Name: "held", F: func() any { return make([]byte, size) }},
	)
	if peak := got["nothing"]; peak > 64<<10 {
		t.Errorf("a function that holds nothing peaked at %d bytes, want at most %d", peak, 64<<10)
	}
	if peak := got["held"]; peak < size-runtimeNoise || peak > size+64<<10 {
		t.Errorf("a held 1 MiB result peaked at %d bytes, want between %d and %d", peak, size-runtimeNoise, size+64<<10)
	}
	for name, peak := range got {
		t.Logf("%s: %d bytes", name, peak)
	}
}

// TestMeasurePeakFreedBaseline: memory freed during the call must not be subtracted from its held result.
func TestMeasurePeakFreedBaseline(t *testing.T) {
	const size = 1 << 20
	sink = make([]byte, 32<<10)
	got := alloctest.MeasurePeak(t, alloctest.PeakFunction{Name: "held", F: func() any {
		sink = nil
		return make([]byte, size)
	}})
	if peak := got["held"]; peak < size-runtimeNoise || peak > size+64<<10 {
		t.Errorf("a held 1 MiB result after freeing old memory peaked at %d bytes, want between %d and %d", peak,
			size-runtimeNoise, size+64<<10)
	}
}
