package billing

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRunKeepsTheHolderAlive: the loop's heartbeat, at the period Start gives it when the configuration names none, is
// what keeps this process able to write at all. Alive holds for the TTL less a margin of 5s after a beat, 10s here, and
// the default period is a third of the 15s TTL, so every beat has 5s to spare: a loop that beat less often, or stopped,
// is seen inside the 12s the test watches, which is two periods and more than the window.
func TestRunKeepsTheHolderAlive(t *testing.T) {
	b := newBank(t, 0, 0, Config{HolderTTL: 15 * time.Second, ReapGrace: time.Hour, Lifetime: time.Hour})
	beats := b.billing.beat.Load()
	stop := runInBackground(t, b)
	for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !b.billing.Alive() {
			t.Fatal("the holder went stale while its loop was beating")
		}
	}
	if b.billing.beat.Load() <= beats+int64(9*time.Second) {
		t.Error("the holder's expiry was not pushed out twice, once in each default heartbeat period")
	}
	stop()
}

// TestRunStopsWhenTheHolderIsReaped: once another node has reaped this one's holder, nothing this process writes is
// fenced in any more, so the loop ends with ErrReaped and the node starts over with a new holder rather than running on
// and failing every reservation.
func TestRunStopsWhenTheHolderIsReaped(t *testing.T) {
	b := newBank(t, 0, 0, Config{HolderTTL: 300 * time.Millisecond, ReapGrace: time.Hour, Lifetime: time.Hour})
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { done <- b.billing.Run(ctx) }()
	b.sql("UPDATE billing.holders SET reaped_at = clock_timestamp() WHERE id = '" + b.billing.holder.String() + "'")
	select {
	case err := <-done:
		if !errors.Is(err, ErrReaped) {
			t.Errorf("the loop ended with %v, want ErrReaped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the loop outlived the reaping of its holder")
	}
}

// TestRunCleansUpAtOnce: the first sweep is not a period away, so what a node that died left behind is taken over as soon
// as its successor starts instead of up to 30 seconds later, all of which the account's balance stays reserved for.
func TestRunCleansUpAtOnce(t *testing.T) {
	b := newBank(t, 1_000_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond})
	h := b.hold(1000)
	b.pastDeadline()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- b.billing.Run(ctx) }()
	for deadline := time.Now().Add(5 * time.Second); b.state() != "cleanup_released"; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the reservation is still %s", b.state())
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("the loop ended with %v", err)
	}
	if !b.supplies.lostNow(h.id) {
		t.Error("the request was not recorded as lost")
	}
}

// runInBackground starts the billing's loop and returns the function that stops it and waits for it.
func runInBackground(t *testing.T, b *bank) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- b.billing.Run(ctx) }()
	var once bool
	stop = func() {
		if once {
			return
		}
		once = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("the loop ended with %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the loop did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

// TestRunBeatsWhileTheCleanupIsStuck: a sweep that waits on a lock (here, the supplier side of a cleanup that does not
// return) does not stop the heartbeat, which is what keeps the node answering: the holder stays alive for longer than
// its TTL less the margin, which a loop that beat between sweeps could not do.
func TestRunBeatsWhileTheCleanupIsStuck(t *testing.T) {
	b := newBank(t, 1_000_000, 0, Config{HolderTTL: 15 * time.Second, ReapGrace: time.Hour, Lifetime: time.Millisecond})
	b.hold(1000)
	b.pastDeadline()
	b.supplies.stuck = make(chan struct{})
	stop := runInBackground(t, b)
	for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !b.billing.Alive() {
			t.Fatal("the holder went stale while a cleanup was stuck")
		}
	}
	if b.state() != "pending" {
		t.Errorf("the reservation is %s: the cleanup was not stuck, so the test proved nothing", b.state())
	}
	stop()
}
