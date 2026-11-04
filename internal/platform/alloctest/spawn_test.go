package alloctest

import (
	"strings"
	"testing"
)

// TestChildEnvironment: the child gets each key alloctest sets once, whatever this process already had. A peak child
// keeps the rest of GODEBUG and makes every collection stop the world; the other measurements leave it alone.
func TestChildEnvironment(t *testing.T) {
	t.Setenv(childEnv, "parent")
	t.Setenv(childModeEnv, "parent")
	t.Setenv("GODEBUG", "http2client=0")

	for _, c := range []struct {
		mode    string
		godebug string
	}{
		{allocMode, "http2client=0"},
		{peakMode, "http2client=0,gcstoptheworld=2"},
	} {
		checkChildEnvironment(t, c.mode, c.godebug)
	}
}

// checkChildEnvironment: the child's environment holds each key alloctest sets exactly once, with the value the mode
// needs, and keeps the rest of GODEBUG.
func checkChildEnvironment(t *testing.T, mode, godebug string) {
	t.Helper()
	want := map[string]string{
		childEnv:     "TestChildEnvironment",
		childModeEnv: mode,
		"GODEBUG":    godebug,
	}
	seen := map[string]int{}
	for _, entry := range childEnvironment("TestChildEnvironment", mode) {
		key, value, _ := strings.Cut(entry, "=")
		if expected, ok := want[key]; ok {
			if value != expected {
				t.Errorf("%s: %s=%q, want %q", mode, key, value, expected)
			}
			seen[key]++
		}
	}
	for key := range want {
		if seen[key] != 1 {
			t.Errorf("%s: %s appears %d times, want once", mode, key, seen[key])
		}
	}
}
