package protocol

import (
	"strconv"
	"testing"

	"axiom/internal/platform/alloctest"
)

// The request writer already reserves the encoded identifier's room; neither JSON layer needs its own allocation.
func TestMetadataUserIDAllocs(t *testing.T) {
	t.Parallel()
	var funcs []alloctest.Function
	for _, value := range []string{"", "plain", "a\"b\\c\x00\n\t", "设备"} {
		w := Rewrites{Device: value, Identity: func(string) string { return value }}.withDefaults()
		out := make([]byte, len("prefix:"), len("prefix:")+quoted(w.IdentityLen))
		copy(out, "prefix:")
		funcs = append(funcs, alloctest.Function{Name: "metadata/" + strconv.Quote(value), F: func() {
			if _, err := appendMetadataUserID(out, w, value); err != nil {
				t.Fatal(err)
			}
		}})
	}
	for name, got := range alloctest.Measure(t, funcs...) {
		if got.Count != 0 {
			t.Errorf("%s: %g allocations for an identifier with reserved room, want zero", name, got.Count)
		}
	}
}
