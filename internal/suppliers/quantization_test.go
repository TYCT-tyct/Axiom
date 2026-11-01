package suppliers

import "testing"

// TestQuantizationLevels: every level OpenRouter documents is read, and a name outside the list, including the empty
// unrecorded value, is refused.
func TestQuantizationLevels(t *testing.T) {
	t.Parallel()
	for _, level := range Quantizations {
		got, err := ParseQuantization(string(level))
		if err != nil || got != level {
			t.Errorf("%q: %q, %v", level, got, err)
		}
	}
	for _, name := range []string{"", "fp64", "FP8", "int3"} {
		if _, err := ParseQuantization(name); err == nil {
			t.Errorf("%q was accepted", name)
		}
	}
}
