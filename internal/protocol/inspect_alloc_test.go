package protocol_test

import (
	"fmt"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/alloctest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// allocSizes are the sizes TestAllocs reads: two tell a constant from growth with the body, and each costs seconds under
// the race detector.
var allocSizes = sizes[:2]

// TestAllocs holds Inspect's allocations beyond validation constant, and indexed Prepare's
// own allocations constant: it no longer visits wide objects in untouched content. The
// child process excludes other goroutines and collections (README.md「不变量」).
func TestAllocs(t *testing.T) {
	for _, c := range []struct {
		name    string
		formats protocol.Format // the formats below it
		read    func(protocol.Format, []byte) error
	}{
		{"Inspect", protocol.Format(len(turns)), func(f protocol.Format, b []byte) error {
			_, err := protocol.Inspect(f, b)
			return err
		}},
		{"Prepare", protocol.Format(len(turns)), prepareInspected()},
	} {
		for f := range c.formats {
			for _, lead := range []string{"", wideTurn} {
				t.Run(fmt.Sprintf("%s-format=%d-wide=%t", c.name, f, lead != ""), func(t *testing.T) {
					if extra := extraAllocs(t, f, lead, c.name == "Inspect", c.read); extra[0] != extra[len(extra)-1] {
						t.Errorf("%s, format %d, wide %t: measured allocations %v grow with the body sizes %v",
							c.name, f, lead != "", extra, allocSizes)
					}
				})
			}
		}
	}
}

// prepareInspected prepares a body with the summary Inspect read from it the first time, which the measurement's
// warm-up run is, so that only Prepare's allocations are counted.
func prepareInspected() func(protocol.Format, []byte) error {
	seen := map[*byte]protocol.Request{}
	return func(f protocol.Format, b []byte) error {
		r, ok := seen[&b[0]]
		if !ok {
			var err error
			if r, err = protocol.Inspect(f, b); err != nil {
				return err
			}
			seen[&b[0]] = r
		}
		_, err := protocol.Prepare(&r, b, protocol.Target{Format: f, Model: "up", EncryptedReasoning: catalog.ClaimYes}, protocol.Rewrites{Identity: derive})
		return err
	}
}

// extraAllocs measures read at each size, subtracting validation only for Inspect.
func extraAllocs(t *testing.T, f protocol.Format, lead string, subtractValidation bool, read func(protocol.Format, []byte) error) []float64 {
	t.Helper()
	var funcs []alloctest.Function
	for _, size := range allocSizes {
		b := body(f, size, lead)
		funcs = append(funcs,
			alloctest.Function{Name: fmt.Sprintf("read/%d", size), F: func() {
				if err := read(f, b); err != nil {
					t.Fatal(err)
				}
			}},
			alloctest.Function{Name: fmt.Sprintf("validate/%d", size), F: func() {
				if err := jsonx.Validate(b); err != nil {
					t.Fatal(err)
				}
			}},
		)
	}
	got := alloctest.Measure(t, funcs...)
	var extra []float64
	for _, size := range allocSizes {
		count := got[fmt.Sprintf("read/%d", size)].Count
		if subtractValidation {
			count -= got[fmt.Sprintf("validate/%d", size)].Count
		}
		extra = append(extra, count)
	}
	return extra
}
