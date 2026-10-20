package kv

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/valkey-io/valkey-go"
	"golang.org/x/sync/semaphore"
)

// stuckValkey is a server that answers when the test lets it, and a connection that behaves as valkey-go v1.0.78 does when
// its ring is full: a command is accepted whatever the caller's context says, and its reply comes when it comes. Embedding
// the interface leaves everything the code does not use to panic.
type stuckValkey struct {
	valkey.Client
	release chan struct{} // closed to let every command through
	mu      sync.Mutex
	out     int // commands sent and not yet answered
	mostOut int
	sent    atomic.Int64
}

func (*stuckValkey) B() valkey.Builder { return valkey.Builder{} }

func (s *stuckValkey) DoMulti(_ context.Context, cmds ...valkey.Completed) []valkey.ValkeyResult {
	s.sent.Add(int64(len(cmds)))
	s.mu.Lock()
	s.out += len(cmds)
	s.mostOut = max(s.mostOut, s.out)
	s.mu.Unlock()
	<-s.release
	s.mu.Lock()
	s.out -= len(cmds)
	s.mu.Unlock()
	return make([]valkey.ValkeyResult, len(cmds))
}

func (*stuckValkey) Close() {}

// stuckClient is a client of that server whose places are as many as a test wants.
func stuckClient(places int64) (*Client, *stuckValkey) {
	server := &stuckValkey{release: make(chan struct{})}
	return &Client{
		vk: server, now: time.Now, logger: slog.New(slog.DiscardHandler), m: newMetrics(nil), br: newBreaker(),
		slots: semaphore.NewWeighted(places),
	}, server
}

func countOnce(ctx context.Context, t *testing.T, client *Client) error {
	t.Helper()
	_, err := client.Do(ctx, Precheck, NewScript("return 1").Cmd(nil))
	return err
}

// leavesAtTheDeadline makes a call and checks that it returned unavailable exactly when its deadline came.
func leavesAtTheDeadline(t *testing.T, client *Client) {
	t.Helper()
	start := time.Now()
	if err := countOnce(t.Context(), t, client); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a call that could not be answered: %v", err)
	}
	if took := time.Since(start); took != Timeout {
		t.Fatalf("the call took %v, want the deadline %v", took, Timeout)
	}
}

// TestAFullQueueNeverHoldsACallPastItsDeadline: with every place taken by a command the server has not answered, a call
// leaves at kv.Timeout without sending, the commands that hold the places count as still out after their callers left, and
// a place is free again only when the reply comes (README「客户端与连接」).
func TestAFullQueueNeverHoldsACallPastItsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := stuckClient(2)
		// Two calls take the two places and leave at the deadline, as callers do when the server does not answer.
		leavesAtTheDeadline(t, client)
		leavesAtTheDeadline(t, client)
		if got := server.sent.Load(); got != 2 {
			t.Fatalf("%d commands sent, want 2", got)
		}
		// The third finds no place: it leaves at its deadline too, and sends nothing.
		leavesAtTheDeadline(t, client)
		if got := server.sent.Load(); got != 2 {
			t.Fatalf("a call with no place sent a command: %d sent", got)
		}
		// The replies come: the places are free and the next call is answered.
		close(server.release)
		synctest.Wait()
		if err := countOnce(t.Context(), t, client); err != nil {
			t.Fatalf("a call after the replies came: %v", err)
		}
	})
}

// TestAPlaceGoesToTheCallThatWaitsForIt: a call waiting for a place takes it when the reply of the command that held it
// comes, and sends then; a call that is cancelled while it waits leaves at once and sends nothing, and is no failure of
// the server's.
func TestAPlaceGoesToTheCallThatWaitsForIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := stuckClient(1)
		var group sync.WaitGroup
		results := make([]error, 3)
		cancelled, cancel := context.WithCancel(t.Context())
		for i, ctx := range []context.Context{t.Context(), t.Context(), cancelled} {
			group.Go(func() { results[i] = countOnce(ctx, t, client) })
			synctest.Wait() // in the order of the places: the first call holds it, the others wait
		}
		time.Sleep(Timeout / 4)
		cancel()
		synctest.Wait()
		if got := server.sent.Load(); got != 1 {
			t.Fatalf("%d commands sent while one place was held, want 1", got)
		}
		time.Sleep(Timeout / 4)
		close(server.release) // the first reply: the place goes to the second call, in time
		group.Wait()
		if results[0] != nil || results[1] != nil {
			t.Fatalf("the calls that had or got the place: %v, %v", results[0], results[1])
		}
		if !errors.Is(results[2], ErrUnavailable) || !errors.Is(results[2], context.Canceled) {
			t.Fatalf("the call cancelled while it waited: %v", results[2])
		}
		if got := server.sent.Load(); got != 2 {
			t.Fatalf("%d commands sent, want the first and the second", got)
		}
		if _, allowed := client.br.allow(time.Now()); !allowed || client.br.open {
			t.Fatal("a caller that left opened the breaker")
		}
	})
}

// TestNoMoreCommandsAreOutThanPlaces: however many calls arrive at once, the commands the server holds unanswered stay
// within the places, which is what keeps a connection's ring from filling.
func TestNoMoreCommandsAreOutThanPlaces(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const places = 3
		client, server := stuckClient(places)
		var group sync.WaitGroup
		for range 50 {
			group.Go(func() {
				if err := countOnce(t.Context(), t, client); err != nil && !errors.Is(err, ErrUnavailable) {
					t.Error(err)
				}
			})
		}
		synctest.Wait()
		close(server.release)
		group.Wait()
		synctest.Wait()
		if server.mostOut > places {
			t.Fatalf("%d commands out at once, want at most %d", server.mostOut, places)
		}
		if server.mostOut == 0 {
			t.Fatal("no command was sent")
		}
	})
}
