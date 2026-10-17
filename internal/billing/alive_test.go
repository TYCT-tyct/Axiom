package billing

import (
	"testing"
	"time"
)

// TestAliveKeepsAMarginOfTheTTL: the holder counts as alive from its last successful heartbeat until the TTL less a
// margin has passed, and never before one was made. The TTL is a day so that no pause of the test moves a case across
// the line.
func TestAliveKeepsAMarginOfTheTTL(t *testing.T) {
	t.Parallel()
	const ttl = 24 * time.Hour
	b := &Billing{config: Config{HolderTTL: ttl}}
	if b.Alive() {
		t.Error("a holder that never registered is alive")
	}
	for _, c := range []struct {
		name string
		ago  time.Duration
		want bool
	}{
		{"just beat", 0, true},
		{"most of the TTL ago", ttl - time.Minute, true},
		{"inside the margin", ttl - time.Second, false},
		{"after the TTL", ttl + time.Minute, false},
	} {
		b.beat.Store(time.Now().Add(-c.ago).UnixNano())
		if got := b.Alive(); got != c.want {
			t.Errorf("%s: alive %v", c.name, got)
		}
	}
}
