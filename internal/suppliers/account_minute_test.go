package suppliers

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"
)

func TestAccountSharesMinuteAcrossCredentials(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8)
	second := w.anotherKey(8)
	w.perMinute(w.credential, 3)
	w.perMinute(second, 3)
	var taken atomic.Int32
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			a, err := w.svc.Acquire(w.endpoint)
			if err != nil {
				if !errors.Is(err, ErrNoSlot) {
					t.Error(err)
				}
				return
			}
			taken.Add(1)
			a.Done()
		})
	}
	wg.Wait()
	if n := taken.Load(); n != 3 {
		t.Fatalf("%d requests in a shared upstream account minute of 3", n)
	}
	w.clock.advance(time.Minute)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("the next account minute: %v", err)
	}
	a.Done()
}

func TestSharedMinuteHonorsCredentialLimits(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	w.anotherKey(4)
	creds := w.svc.snap.Load().endpoints[w.endpoint].creds
	first, second := creds[0].id, creds[1].id
	w.perMinute(first, 1)
	w.perMinute(second, 2)
	for _, want := range []uuid.UUID{first, second} {
		a, err := w.svc.Acquire(w.endpoint)
		if err != nil {
			t.Fatal(err)
		}
		same(t, "the credential whose account ceiling admits the request", want, a.cred.id)
		a.Done()
	}
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("the account already used the highest configured ceiling: %v", err)
	}
}

func TestUnlimitedCredentialsConsumeSharedMinute(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	second := w.anotherKey(4)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	a.Done()
	w.perMinute(w.credential, 1)
	w.perMinute(second, 1)
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("the account forgot a request made by an unlimited credential: %v", err)
	}
}

func TestSupplierMinutesAreIndependent(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	_, credential := w.addSupplier()
	w.perMinute(w.credential, 1)
	w.perMinute(credential, 1)
	if n := len(w.svc.snap.Load().endpoints); n != 2 {
		t.Fatalf("loaded %d independent suppliers' endpoints, want 2", n)
	}
	for id := range w.svc.snap.Load().endpoints {
		a, err := w.svc.Acquire(id)
		if err != nil {
			t.Fatalf("an independent supplier's minute: %v", err)
		}
		a.Done()
	}
}

func TestMinuteCounterDoesNotWrap(t *testing.T) {
	t.Parallel()
	now := time.Time{}
	rt := minuteRuntime{start: now, used: maxLimit - 1}
	for range 2 {
		if !rt.take(now, 0) {
			t.Fatal("an unlimited credential was refused")
		}
	}
	if rt.take(now, maxLimit) || rt.room(now, maxLimit) {
		t.Fatal("unlimited requests wrapped the shared minute's count")
	}
	if !rt.take(now.Add(time.Minute), maxLimit) {
		t.Fatal("the saturated minute did not expire")
	}
}
