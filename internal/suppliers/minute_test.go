package suppliers

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestACredentialTakesOnlyItsWindow: a per-minute limit admits that many requests in any rolling minute and no more,
// free slots or not; Done gives the slot back but not the request; the request leaves the window on the 61st second,
// and a snapshot rebuilt under it keeps the count.
func TestACredentialTakesOnlyItsWindow(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	w.perMinute(w.credential, 2)
	first, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("a third request in a window of two: %v", err)
	}
	first.Done() // the slot is free again, but the request was made and is counted
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("a window that is used up took a request because a slot was free: %v", err)
	}
	if err := w.svc.Reload(t.Context()); err != nil { // rebuilt under the window
		t.Fatal(err)
	}
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("a rebuild forgot the window's requests: %v", err)
	}
	w.clock.advance(59 * time.Second)
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("59 seconds later: %v", err)
	}
	w.clock.advance(time.Second) // exactly 60 seconds: the first second is still in the window
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("at the 60th second: %v", err)
	}
	w.clock.advance(time.Second) // 61 seconds: the first second has left it
	third, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("at the 61st second: %v", err)
	}
	second.Done()
	third.Done()
}

// TestNeverMoreRequestsThanTheWindowWhoeverRaces: many goroutines asking at once take exactly the window's requests,
// and one that loses the last of them gives its slot back, so the credential is not left looking full.
func TestNeverMoreRequestsThanTheWindowWhoeverRaces(t *testing.T) {
	t.Parallel()
	const limit = 3
	w := newWorld(t, 8) // slots to spare: only the window can refuse
	w.perMinute(w.credential, limit)
	var taken atomic.Int32
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			a, err := w.svc.Acquire(w.endpoint)
			if err != nil {
				return
			}
			taken.Add(1)
			a.Done()
		})
	}
	wg.Wait()
	if got := taken.Load(); got != limit {
		t.Errorf("%d requests in a window of %d", got, limit)
	}
	w.clock.advance(61 * time.Second)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("the window after the requests kept their slots: %v", err)
	}
	a.Done()
}

// TestASpentWindowIsLeftToTheOtherKeys: a key whose account window is used up is passed over while another key of the
// endpoint has room, so one key at its ceiling does not make the endpoint read as full.
func TestASpentWindowIsLeftToTheOtherKeys(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	w.anotherKey(4)
	creds := w.svc.snap.Load().endpoints[w.endpoint].creds
	first, second := creds[0].id, creds[1].id // the order leastBusy prefers them in while the loads are equal
	w.perMinute(first, 1)
	spent, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	same(t, "the key that took the window's request", first, spent.cred.id)
	spent.Done()
	other, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("a key whose window is spent left the endpoint looking full: %v", err)
	}
	same(t, "the key that serves instead", second, other.cred.id)
	other.Done()
}

// TestTheWindowEndsAtTheSixtiethSecond: a request still counts at exactly 60 seconds, and the window admits again at
// the 61st, down to the sub-second part of a second in which it was made.
func TestTheWindowEndsAtTheSixtiethSecond(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		at   time.Duration
		fits bool
	}{
		{"59 seconds later", 59 * time.Second, false},
		{"at the 60th second", 60 * time.Second, false},
		{"just before the 61st", 60*time.Second + 999*time.Millisecond, false},
		{"at the 61st second", 61 * time.Second, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rt := &minuteRuntime{}
			if !rt.take(base, 1) {
				t.Fatal("the first request was refused")
			}
			if got := rt.take(base.Add(c.at), 1); got != c.fits {
				t.Fatalf("the next request fits: %v, want %v", got, c.fits)
			}
		})
	}
}

// TestABurstAtTheWindowEdgeIsBounded: a burst at the edge of a used-up window admits nothing until the first second
// has left it, and then admits the ceiling and no more.
func TestABurstAtTheWindowEdgeIsBounded(t *testing.T) {
	t.Parallel()
	const limit = 5
	base := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	rt := &minuteRuntime{}
	for range limit {
		if !rt.take(base, limit) {
			t.Fatal("the first burst was refused within its ceiling")
		}
	}
	for _, at := range []time.Duration{59 * time.Second, 60 * time.Second, 60*time.Second + 999*time.Millisecond} {
		for range 20 {
			if rt.take(base.Add(at), limit) {
				t.Fatalf("a burst %s later was admitted into the same window", at)
			}
		}
	}
	var admitted int
	for range 20 {
		if rt.take(base.Add(61*time.Second), limit) {
			admitted++
		}
	}
	if admitted != limit {
		t.Fatalf("a burst at the next window admitted %d, want %d", admitted, limit)
	}
}

// TestOneSecondSaturatesAtTheCeiling: requests from unlimited credentials still count, and one second's count stops at
// the largest ceiling instead of overflowing; the saturated second leaves the window at the 61st second.
func TestOneSecondSaturatesAtTheCeiling(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	rt := &minuteRuntime{}
	for range MaxRequestsPerMinute + 1 {
		if !rt.take(base, 0) {
			t.Fatal("an unlimited credential was refused")
		}
	}
	if got := rt.count(base); got != MaxRequestsPerMinute {
		t.Fatalf("a saturated second counted %d, want %d", got, MaxRequestsPerMinute)
	}
	if rt.take(base, MaxRequestsPerMinute) {
		t.Fatal("a saturated second admitted a request at the largest ceiling")
	}
	if !rt.take(base.Add(61*time.Second), MaxRequestsPerMinute) {
		t.Fatal("the saturated second did not leave the window")
	}
}

// BenchmarkWindowTake is the admission step of Acquire: it must allocate nothing, since every call of the node's call
// path takes it.
func BenchmarkWindowTake(b *testing.B) {
	rt := &minuteRuntime{}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	for b.Loop() {
		rt.take(now, MaxRequestsPerMinute)
	}
}
