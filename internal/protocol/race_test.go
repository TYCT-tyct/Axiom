package protocol

import (
	"runtime/debug"
	"slices"
)

// RaceDetector reports whether the test binary was built with the race detector, whose runtime allocates beside the
// code and drops sync.Pool entries at random, so allocation counts measured under it are not the code's. It is
// exported for the external tests.
func RaceDetector() bool {
	info, ok := debug.ReadBuildInfo()
	return ok && slices.ContainsFunc(info.Settings, func(s debug.BuildSetting) bool { return s.Key == "-race" && s.Value == "true" })
}
