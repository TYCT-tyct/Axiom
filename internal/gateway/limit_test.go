package gateway

import (
	"errors"
	"strconv"
	"testing"
)

func newInFlight(t *testing.T, node int) *InFlight {
	t.Helper()
	cfg := DefaultConfig()
	cfg.NodeInFlight = node
	bp, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return bp.InFlight
}

func acquireSlot(t *testing.T, f *InFlight, account string, accountMax int) func() {
	t.Helper()
	release, err := f.Acquire(account, accountMax)
	if err != nil {
		t.Fatalf("Acquire(%q): %v", account, err)
	}
	return release
}

// The account limit is 429 and the node limit is 503; the default account limit is a tenth of the node's slots and
// an account's own setting replaces it.
func TestInFlightRefusals(t *testing.T) {
	t.Parallel()
	f := newInFlight(t, 20) // an account may hold 2 by default
	first := acquireSlot(t, f, "a", 0)
	acquireSlot(t, f, "a", 0)
	if _, err := f.Acquire("a", 0); !errors.Is(err, errAccountInFlight) {
		t.Fatalf("third slot of an account: %v, want errAccountInFlight", err)
	}
	acquireSlot(t, f, "a", 3) // this account is set to 3
	acquireSlot(t, f, "b", 0)
	first()
	first() // a second release must not free a second slot
	if _, err := f.Acquire("a", 0); !errors.Is(err, errAccountInFlight) {
		t.Fatalf("a has 2 left and 2 is its limit: %v, want errAccountInFlight", err)
	}
	if _, err := f.Acquire("a", 3); err != nil {
		t.Fatalf("a has 2 left and 3 is its setting: %v", err)
	}
	if _, err := f.Acquire("a", 3); !errors.Is(err, errAccountInFlight) {
		t.Fatalf("a has 3 and 3 is its setting: %v, want errAccountInFlight", err)
	}

	for i := range 20 - 4 { // 4 slots are in use
		acquireSlot(t, f, "filler"+strconv.Itoa(i), 1)
	}
	if _, err := f.Acquire("late", 0); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("node full: %v, want ErrOverloaded", err)
	}
	if ErrOverloaded.Status != 503 || ErrOverloaded.RetryAfter != 1 || errAccountInFlight.Status != 429 {
		t.Fatalf("refusals %+v %+v", ErrOverloaded, errAccountInFlight)
	}
}

// A request refused by the node must not keep its account's slot.
func TestInFlightNodeRefusalReleasesTheAccount(t *testing.T) {
	t.Parallel()
	f := newInFlight(t, 10) // an account may hold 1 by default
	var held []func()
	for i := range 10 {
		held = append(held, acquireSlot(t, f, "a"+strconv.Itoa(i), 0))
	}
	if _, err := f.Acquire("x", 0); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("got %v, want ErrOverloaded", err)
	}
	held[0]()
	acquireSlot(t, f, "x", 0) // would be errAccountInFlight had the refused attempt kept x's slot
}

// A flood of distinct keys leaves nothing behind once they are released.
func TestLimiterForgetsReleasedKeys(t *testing.T) {
	t.Parallel()
	l := NewLimiter(errIPInFlight)
	for i := range 100 {
		release, err := l.Acquire(strconv.Itoa(i), 1)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if len(l.held) != 0 {
		t.Fatalf("%d keys retained", len(l.held))
	}
}

// Release may run more than once (a request has several exit paths): the second call must not free the slot of
// the next holder.
func TestLimiterReleaseIsIdempotent(t *testing.T) {
	t.Parallel()
	l := NewLimiter(errIPInFlight)
	release, err := l.Acquire("k", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire("k", 2); err != nil {
		t.Fatal(err)
	}
	release()
	release()
	if _, err := l.Acquire("k", 2); err != nil {
		t.Fatalf("one slot was freed: %v", err)
	}
	if _, err := l.Acquire("k", 2); !errors.Is(err, errIPInFlight) {
		t.Fatalf("a second release freed a second slot: %v", err)
	}
}
