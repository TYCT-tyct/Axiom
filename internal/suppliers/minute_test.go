package suppliers

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestACredentialTakesOnlyItsMinute: a per-minute limit admits that many requests in a minute and no more, free slots or
// not; Done gives the slot back but not the request; the next minute admits another, and the count is not reset by a
// snapshot rebuilt under it.
func TestACredentialTakesOnlyItsMinute(t *testing.T) {
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
		t.Fatalf("a third request in a minute of two: %v", err)
	}
	first.Done() // the slot is free again, but the request was made and is counted
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("a minute that is used up took a request because a slot was free: %v", err)
	}
	if err := w.svc.Reload(t.Context()); err != nil { // rebuilt under the minute
		t.Fatal(err)
	}
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("a rebuild forgot the minute's requests: %v", err)
	}
	w.clock.advance(59 * time.Second)
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("a second less than a minute: %v", err)
	}
	w.clock.advance(time.Second)
	third, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("the next minute: %v", err)
	}
	second.Done()
	third.Done()
}

// TestNeverMoreRequestsThanTheMinuteWhoeverRaces: many goroutines asking at once take exactly the minute's requests, and
// one that loses the last of them gives its slot back, so the credential is not left looking full.
func TestNeverMoreRequestsThanTheMinuteWhoeverRaces(t *testing.T) {
	t.Parallel()
	const limit = 3
	w := newWorld(t, 8) // slots to spare: only the minute can refuse
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
		t.Errorf("%d requests in a minute of %d", got, limit)
	}
	w.clock.advance(time.Minute)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("the next minute on a credential whose refused requests kept their slots: %v", err)
	}
	a.Done()
}

// TestASpentMinuteIsLeftToTheOtherKeys: a key whose minute is used up is passed over while another key of the endpoint
// has room, so one key at its ceiling does not make the endpoint read as full.
func TestASpentMinuteIsLeftToTheOtherKeys(t *testing.T) {
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
	same(t, "the key that took the minute's request", first, spent.cred.id)
	spent.Done()
	other, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("a key whose minute is spent left the endpoint looking full: %v", err)
	}
	same(t, "the key that serves instead", second, other.cred.id)
	other.Done()
}
