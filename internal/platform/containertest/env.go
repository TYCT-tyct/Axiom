package containertest

import (
	"os"
	"slices"
	"strings"
)

// Untracked reads an infrastructure identity that is not a test input. Unlike Getenv and LookupEnv, Environ does not
// add it to Go's test cache key, so a new container or job identity does not invalidate otherwise reusable results.
// The last value wins because withcluster appends its values after the inherited environment.
func Untracked(name string) string {
	return untrackedIn(os.Environ(), name)
}

func untrackedIn(env []string, name string) string {
	prefix := name + "="
	for _, entry := range slices.Backward(env) {
		if value, ok := strings.CutPrefix(entry, prefix); ok {
			return value
		}
	}
	return ""
}
