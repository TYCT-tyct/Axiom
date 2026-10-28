package kv

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/valkey-io/valkey-go"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// pausedWrites answers prechecks immediately while SET replies remain under test control.
type pausedWrites struct{ *stuckValkey }

func (s *pausedWrites) DoMulti(ctx context.Context, cmds ...valkey.Completed) []valkey.ValkeyResult {
	if cmds[0].Commands()[0] == "SET" {
		return s.stuckValkey.DoMulti(ctx, cmds...)
	}
	return make([]valkey.ValkeyResult, len(cmds))
}

func priorityClient() (*Client, *stuckValkey) {
	c, server := stuckClient(maxOutstanding)
	c.vk = &pausedWrites{server}
	c.later = make(chan Cmd, laterCapacity)
	return c, server
}

func concurrentPrechecks(t *testing.T, c *Client) {
	t.Helper()
	start := time.Now()
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			_, err := c.Do(t.Context(), Precheck, NewScript("return 1").Cmd(nil), Get("ax:test"))
			if err != nil {
				t.Errorf("healthy precheck waited behind sticky writes: %v", err)
			}
		})
	}
	group.Wait()
	if time.Now() != start {
		t.Error("sticky writes delayed healthy prechecks to the deadline")
	}
}

func checkForegroundBreaker(t *testing.T, c *Client) {
	t.Helper()
	c.br.mu.Lock()
	defer c.br.mu.Unlock()
	if c.br.open || c.br.streak != 0 || c.br.probing {
		t.Error("sticky writes changed the foreground breaker")
	}
}

func TestStickyWriterDoesNotQueueAheadOfPrechecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, server := priorityClient()
		reader := sdkmetric.NewManualReader()
		c.m = newMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"))
		const occupied = maxOutstanding - 2
		if err := c.slots.Acquire(t.Context(), occupied); err != nil {
			t.Fatal(err)
		}
		for range 32 {
			c.later <- Set("ax:test", "value", time.Hour)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer func() {
			cancel()
			c.slots.Release(occupied)
			close(server.release)
			c.done.Wait()
			synctest.Wait()
		}()
		c.done.Go(func() { c.writeLater(ctx) })
		synctest.Wait()
		concurrentPrechecks(t, c)
		checkForegroundBreaker(t, c)
		if dropped(t, reader, dropSlots) != 32 {
			t.Error("busy write slots were not dropped and counted immediately")
		}
	})
}

func TestStickyWriterLeavesRoomForPrechecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, server := priorityClient()
		reader := sdkmetric.NewManualReader()
		c.m = newMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"))
		for range laterCapacity {
			c.later <- Set("ax:test", "value", time.Hour)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer func() { cancel(); close(server.release); c.done.Wait(); synctest.Wait() }()
		c.done.Go(func() { c.writeLater(ctx) })
		synctest.Wait()
		concurrentPrechecks(t, c)
		time.Sleep((streakAccesses + 1) * Timeout)
		// Timed-out commands still occupy the ring until their replies arrive. Later batches must not accumulate.
		if got := server.sent.Load(); got != 32 {
			t.Errorf("stalled sticky writes held %d slots, want at most one batch of 32", got)
		}
		concurrentPrechecks(t, c)
		checkForegroundBreaker(t, c)
		if dropped(t, reader, dropSlots) != laterCapacity-32 {
			t.Error("later batches bypassed the outstanding write budget or were not counted")
		}
	})
}

func TestStickyTimeoutsDoNotAffectBreaker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, server := priorityClient()
		defer func() { close(server.release); synctest.Wait() }()
		for range streakAccesses {
			if _, err := c.Do(t.Context(), StickyWrite, Set("ax:test", "value", time.Hour)); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stalled write did not reach its deadline: %v", err)
			}
		}
		checkForegroundBreaker(t, c)
		concurrentPrechecks(t, c)
	})
}

func TestStickyOnlyForegroundCanProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, server := priorityClient()
		defer func() { close(server.release); synctest.Wait() }()
		c.br.trip(time.Now())
		time.Sleep(openFor)
		if _, err := c.Do(t.Context(), StickyWrite, Set("ax:test", "value", time.Hour)); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
		if server.sent.Load() != 0 || c.br.probing {
			t.Error("sticky write claimed the foreground probe")
		}
		if err := countOnce(t.Context(), t, c); err != nil {
			t.Fatalf("foreground probe failed: %v", err)
		}
		concurrentPrechecks(t, c)
		checkForegroundBreaker(t, c)
	})
}
