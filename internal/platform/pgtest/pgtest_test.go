package pgtest

import "testing"

// TestDockerHostFor: on Windows without DOCKER_HOST the pipe is named, so testcontainers does not have to find it by
// checking that the path exists, which fails while the pipe is busy; whatever the developer set is kept, and other
// systems are left to testcontainers.
func TestDockerHostFor(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ goos, current, want string }{
		{"windows", "", windowsDockerHost},
		{"windows", "tcp://remote:2375", "tcp://remote:2375"},
		{"linux", "", ""},
		{"darwin", "unix:///x.sock", "unix:///x.sock"},
	} {
		if got := dockerHostFor(c.goos, c.current); got != c.want {
			t.Errorf("%s with %q: %q, want %q", c.goos, c.current, got, c.want)
		}
	}
}
