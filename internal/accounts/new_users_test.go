package accounts

import (
	"testing"
	"time"
	"uuid"
)

// TestV7FloorBoundsTheMillisecond: a UUIDv7 made at a millisecond sorts at or above that millisecond's floor and below
// the next one's, so a window of floors is the users who signed up in it.
func TestV7FloorBoundsTheMillisecond(t *testing.T) {
	t.Parallel()
	for range 100 {
		before := time.Now()
		id := uuid.NewV7()
		after := time.Now()
		if id.Compare(v7Floor(before.Truncate(time.Millisecond))) < 0 {
			t.Fatalf("%s sorts below the floor of %s", id, before)
		}
		if id.Compare(v7Floor(after.Truncate(time.Millisecond).Add(time.Millisecond))) >= 0 {
			t.Fatalf("%s sorts at or above the floor after %s", id, after)
		}
	}
}
