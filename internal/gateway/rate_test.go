package gateway

import (
	"strconv"
	"testing"
	"testing/synctest"
	"time"
)

func TestRateCounterSlidingWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRateCounter(time.Minute)
		for i := 1; i <= 10; i++ {
			if got := r.Count("a"); got != i {
				t.Fatalf("count %d = %d", i, got)
			}
		}
		if got := r.Count("b"); got != 1 {
			t.Fatalf("another key = %d, want 1", got)
		}
		time.Sleep(90 * time.Second) // half of the previous window is still covered: 10/2 = 5, plus this call
		if got := r.Count("a"); got != 1+5 {
			t.Fatalf("after 90s = %d, want 6", got)
		}
		time.Sleep(2 * time.Minute)
		if got := r.Count("a"); got != 1 {
			t.Fatalf("after two idle windows = %d, want 1", got)
		}
	})
}

// Cold keys must not accumulate: after two windows without them nothing is held.
func TestRateCounterDropsIdleKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRateCounter(time.Minute)
		for i := range 100 {
			r.Count(strconv.Itoa(i))
		}
		time.Sleep(2 * time.Minute)
		r.Count("x")
		if held := len(r.cur) + len(r.prev); held != 1 {
			t.Fatalf("%d keys held, want 1", held)
		}
	})
}
