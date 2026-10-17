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
