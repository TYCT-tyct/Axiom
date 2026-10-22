package kv

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func queuedClient() *Client {
	return &Client{
		now: time.Now, later: make(chan Cmd, laterCapacity),
		accounts: writeAccounts{fullAt: make(map[uuid.UUID]time.Time)}, m: newMetrics(nil),
	}
}

func dropped(t *testing.T, reader *sdkmetric.ManualReader, reason dropReason) int64 {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != droppedMetric {
				continue
			}
			for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
				if v, _ := p.Attributes.Value("reason"); v.AsString() == string(reason) {
					total += p.Value
				}
			}
		}
	}
	return total
}

// makeDrop brings the client into the state in which Later drops for one reason.
func makeDrop(c *Client, reason dropReason) {
	cmd := Set("ax:test", "endpoint", time.Hour)
	switch reason {
	case dropQueue:
		for range cap(c.later) {
			c.later <- cmd
		}
	case dropMemory:
		c.checkMemory(memory{used: 71, max: 100})
	case dropAccount:
		for range writeBurst {
			c.Later(uuid.UUID{1}, cmd)
		}
	}
}

func TestLaterDropsAndCounts(t *testing.T) {
	for _, reason := range dropReasons {
		t.Run(string(reason), func(t *testing.T) {
			c := queuedClient()
			reader := sdkmetric.NewManualReader()
			c.m = newMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"))
			now := time.Unix(1, 0)
			c.now = func() time.Time { return now }
			makeDrop(c, reason)
			before := len(c.later)
			c.Later(uuid.UUID{1}, Set("ax:test", "endpoint", time.Hour))
			if len(c.later) != before || dropped(t, reader, reason) != 1 {
				t.Fatalf("%s: write was queued or not counted", reason)
			}
		})
	}
}

func TestWriteAccountsBoundAndRefill(t *testing.T) {
	now := time.Unix(1, 0)
	a := writeAccounts{fullAt: make(map[uuid.UUID]time.Time)}
	var allowed atomic.Int64
	var group sync.WaitGroup
	for range 200 {
		group.Go(func() {
			if a.allow(uuid.UUID{1}, now) {
				allowed.Add(1)
			}
		})
	}
	group.Wait()
	if allowed.Load() != writeBurst || a.allow(uuid.UUID{1}, now.Add(writeInterval-1)) {
		t.Fatal("concurrent callers exceeded the burst or refilled early")
	}
	if !a.allow(uuid.UUID{1}, now.Add(writeInterval)) || a.allow(uuid.UUID{1}, now.Add(writeInterval)) {
		t.Fatal("one interval must refill exactly one write")
	}
	if !a.allow(uuid.UUID{2}, now) {
		t.Fatal("another account shared the first one's bucket")
	}
	a.prune(now.Add(time.Minute))
	if len(a.fullAt) != 0 {
		t.Fatal("idle accounts were retained")
	}
	for range accountCapacity {
		a.fullAt[uuid.New()] = now.Add(time.Minute)
	}
	if a.allow(uuid.UUID{}, now) || len(a.fullAt) != accountCapacity {
		t.Fatal("a full table grew or reset a bucket")
	}
}

func TestStickyMemoryHysteresis(t *testing.T) {
	c := queuedClient()
	for _, step := range []struct {
		used int64
		stop bool
	}{{70, false}, {71, true}, {65, true}, {60, true}, {59, false}} {
		c.checkMemory(memory{used: step.used, max: 100})
		if c.memoryStop.Load() != step.stop {
			t.Fatalf("%d%%: stopped = %v", step.used, c.memoryStop.Load())
		}
	}
	c.checkMemory(memory{used: 1<<63 - 1, max: 1<<63 - 1})
	if !c.memoryStop.Load() {
		t.Fatal("large memory readings bypassed the stop")
	}
}

func TestLaterBatchesAndRechecksMemory(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			c, server := stuckClient(maxOutstanding)
			c.later = make(chan Cmd, laterCapacity)
			reader := sdkmetric.NewManualReader()
			c.m = newMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"))
			for range 20 {
				c.later <- Set("ax:test", "value", time.Hour)
			}
			c.memoryStop.Store(stopped)
			ctx, cancel := context.WithCancel(t.Context())
			defer func() {
				cancel()
				close(server.release)
				c.done.Wait()
			}()
			c.done.Go(func() { c.writeLater(ctx) })
			synctest.Wait()
			if stopped {
				if server.sent.Load() != 0 || dropped(t, reader, dropMemory) != 20 {
					t.Fatal("queued writes escaped the memory stop")
				}
			} else if server.sent.Load() != 20 || server.batches.Load() != 1 {
				t.Fatalf("sent %d commands, want one batch of 20", server.sent.Load())
			}
		})
	}
}

func TestLaterStopsWithClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, server := stuckClient(maxOutstanding)
		c.later = make(chan Cmd, laterCapacity)
		ctx, cancel := context.WithCancel(t.Context())
		c.stop = cancel
		c.done.Go(func() { c.writeLater(ctx) })
		if err := c.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		c.Later(uuid.UUID{}, Set("ax:test", "value", time.Hour))
		if len(c.later) != 0 || server.sent.Load() != 0 {
			t.Fatal("a closed client retained or sent a write")
		}
	})
}

func TestLaterSplitsAtTheOutstandingLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, server := stuckClient(maxOutstanding)
		c.later = make(chan Cmd, laterCapacity)
		const writes = 2*maxOutstanding + 1
		for range writes {
			c.later <- Set("ax:test", "value", time.Hour)
		}
		close(server.release)
		ctx, cancel := context.WithCancel(t.Context())
		c.done.Go(func() { c.writeLater(ctx) })
		synctest.Wait()
		cancel()
		c.done.Wait()
		server.mu.Lock()
		most := server.mostOut
		server.mu.Unlock()
		if server.sent.Load() != writes || server.batches.Load() != 3 || most > maxOutstanding {
			t.Fatalf("sent %d in %d batches, most outstanding %d", server.sent.Load(), server.batches.Load(), most)
		}
	})
}

func TestLaterDoesNotWaitForStalledWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, server := stuckClient(maxOutstanding)
		c.later = make(chan Cmd, laterCapacity)
		c.accounts.fullAt = make(map[uuid.UUID]time.Time)
		reader := sdkmetric.NewManualReader()
		c.m = newMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"))
		cmd := Set("ax:test", "endpoint", time.Hour)
		for range maxOutstanding {
			c.later <- cmd
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer func() {
			cancel()
			close(server.release)
			c.done.Wait()
		}()
		c.done.Go(func() { c.writeLater(ctx) })
		synctest.Wait() // the first batch is waiting for replies the server has withheld
		if server.sent.Load() != maxOutstanding {
			t.Fatal("the writer did not reach the stalled server")
		}
		start := time.Now()
		for n := range laterCapacity + 1 {
			c.Later(uuid.UUID{byte(n), byte(n >> 8)}, cmd)
		}
		if time.Now() != start || len(c.later) != laterCapacity || dropped(t, reader, dropQueue) != 1 {
			t.Fatal("offers waited, grew the queue, or failed to count the overflow")
		}
		if server.sent.Load() != maxOutstanding {
			t.Fatal("a second batch escaped while all command slots were held")
		}
	})
}
