package billing

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestRunKeepsTheHolderAlive: the loop's heartbeat, at the period Start gives it when the configuration names none, is
// what keeps this process able to write at all. Alive holds for the TTL less a margin of 5s after a beat, 10s here, and
// the default period is a third of the 15s TTL, so every beat has 5s to spare: a loop that beat less often, or stopped,
// is seen inside the 12s the test watches, which is two periods and more than the window.
func TestRunKeepsTheHolderAlive(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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

// TestHeartbeatBeatsWhileThePoolIsExhausted: a call holding every connection of the ordinary pool still lets a beat
// through within its period, because the heartbeat has a connection of its own. The same beat on the pool waits for a
// connection until its deadline, which is how a pool exhausted by calls delays a beat past the holder's TTL less the
// margin, turns Alive false and answers 503 on the whole node (#217).
func TestHeartbeatBeatsWhileThePoolIsExhausted(t *testing.T) {
	t.Parallel()

	database := pgtest.New(t, Migrations())
	// One ordinary connection and one for the completion class, so the one can be held without the pool opening more.
	app, err := pg.Open(t.Context(), database.App+"&pool_max_conns=2", pg.WithCompletionReserve(1))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	platform := pg.NewScope(true, nil, nil)
	config := Config{HolderTTL: 15 * time.Second, ReapGrace: time.Hour, Lifetime: time.Hour}
	b, err := Start(t.Context(), app, platform, config, newSupplies())
	if err != nil {
		t.Fatal(err)
	}
	defer b.conn.Close(t.Context())

	// The held transaction is released however the test ends: a Fatal would otherwise leave it waiting on release, and
	// the pool closes only once its connection comes back.
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- app.Tx(t.Context(), platform, func(pg.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	var ended bool
	defer func() {
		if ended {
			return
		}
		close(release)
		if err := <-done; err != nil {
			t.Errorf("the transaction that held the pool: %v", err)
		}
	}()
	select {
	case <-held:
	case err := <-done:
		ended = true
		t.Fatalf("the transaction that was to hold the pool ended first: %v", err)
	}

	beat, cancel := context.WithTimeout(t.Context(), b.config.Heartbeat)
	defer cancel()
	if err := b.Heartbeat(beat); err != nil {
		t.Fatalf("the heartbeat waited for a pooled connection: %v", err)
	}

	pooled, cancelPooled := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancelPooled()
	if err := app.Tx(pooled, platform, func(pg.Tx) error { return nil }); err == nil {
		t.Fatal("a pooled transaction ran: the pool was not exhausted, so the test proved nothing")
	}
}

// TestTheReapDoesNotTakeABeat: the heartbeat and the reap share their connection, so a reap that waits on the row of a
// holder another node's write transaction still fences gives up after half a beat period; the beat that was waiting for
// the connection lands inside its own period, and it is the beat, not the crossing of a deadline, that keeps Alive true.
func TestTheReapDoesNotTakeABeat(t *testing.T) {
	t.Parallel()

	b := newBank(t, 0, 0, Config{HolderTTL: 6 * time.Second, ReapGrace: time.Hour, Lifetime: time.Hour})
	// A holder of another node that is past its TTL and a reap's grace, and a transaction that fences it the way a write
	// in flight on that node does: FOR UPDATE, which the reap takes, waits for FOR KEY SHARE.
	other := uuid.NewV7()
	b.sql("INSERT INTO billing.holders (id, expires_at) VALUES ('" + other.String() + "', clock_timestamp() - interval '2 hours')")
	// The fencing transaction is released however the test ends: a Fatal would otherwise leave it waiting on release, and
	// the bank's pool closes only once its connection comes back.
	held, release := make(chan struct{}), make(chan struct{})
	fenced := make(chan error, 1)
	go func() {
		fenced <- b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "SELECT 1 FROM billing.holders WHERE id = $1 FOR KEY SHARE", other); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	var ended bool
	defer func() {
		if ended {
			return
		}
		close(release)
		if err := <-fenced; err != nil {
			t.Errorf("the transaction that fenced the row: %v", err)
		}
	}()
	select {
	case <-held:
	case err := <-fenced:
		ended = true
		t.Fatalf("the fencing transaction ended before the reap waited: %v", err)
	}
	reaped := make(chan error, 1)
	go func() {
		_, err := b.billing.Reap(t.Context())
		reaped <- err
	}()
	// The reap holds the connection only once it is waiting in the database, which is the state whose cost to the beat
	// this test is about.
	for deadline := time.Now().Add(5 * time.Second); b.count(`SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND state = 'active' AND wait_event_type = 'Lock'`) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the reap never waited on the fence, so the test proved nothing")
		}
	}

	beat, cancel := context.WithTimeout(t.Context(), b.billing.config.Heartbeat)
	defer cancel()
	if err := b.billing.Heartbeat(beat); err != nil {
		t.Fatalf("the heartbeat waited for the reap's lock: %v", err)
	}
	if err := <-reaped; err == nil {
		t.Error("the reap succeeded while the row it must lock was fenced, so the test proved nothing")
	}
	close(release)
	ended = true
	if err := <-fenced; err != nil {
		t.Fatalf("the fencing transaction: %v", err)
	}
}
