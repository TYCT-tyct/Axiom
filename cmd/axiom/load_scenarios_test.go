//go:build load

package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"
)

// TestLoad is the load test of docs/performance.md「怎么验证」: seven scenarios, each on a node of its own, run one after
// another against one database. `just load` runs it; its table is printed at the end and goes into the pull request.
func TestLoad(t *testing.T) {
	lw := newLoadWorld(t, loadPlanFromEnv(t))
	lw.scenario(t, "1_streams", lw.manyStreams)
	lw.scenario(t, "2_burst_and_cancel", lw.burstAndCancel)
	lw.scenario(t, "3_large_requests", lw.largeRequests)
	lw.scenario(t, "4_hot_account", lw.hotAccount)
	lw.scenario(t, "5_cold_key_flood", lw.coldKeyFlood)
	lw.scenario(t, "6a_database_out_30s", func() { lw.databaseOut(30 * time.Second) })
	lw.scenario(t, "6b_database_out_150s", func() { lw.databaseOut(150 * time.Second) })
	lw.note("7", "primary/standby failover: not run; M1 has no replica, so there is nothing to fail over to")
	lw.report()
}

// manyStreams: scenario 1, a node holding as many streams at once as the product promises (5000), each paced like a
// model's: a first token after 300 ms and an event every 250 ms. They arrive over forty seconds, so the ends, and with them the settlements, are spread as
// real ones are.
func (lw *loadWorld) manyStreams() {
	p := lw.plan
	n := p.n(5000)
	lw.upstream.setShape(streamShape{firstToken: 300 * time.Millisecond, every: 250 * time.Millisecond, length: p.d(100 * time.Second)})
	lw.upstream.peak.Store(0)
	node := lw.node(nil)
	name := fmt.Sprintf("1. %d concurrent streams", n)
	outs, peak := lw.phase(node, name, func() []outcome {
		return lw.driver.wave(lw.t.Context(), n, p.d(40*time.Second), lw.spec)
	})
	lw.record(name, outs, peak)
	lw.note(name, "the most streams open at the upstream at once: %d", lw.upstream.peak.Load())
	lw.finish(node)
}

// burstAndCancel: scenario 2, a burst of new requests in one second whose streams run on, and then every client going
// away at the same moment, at two sizes. What follows is how fast the node lets the upstream and the ledger go.
func (lw *loadWorld) burstAndCancel() {
	node := lw.node(nil)
	for _, size := range []int{1000, 4000} {
		lw.burst(node, lw.plan.n(size))
	}
	lw.finish(node)
}

func (lw *loadWorld) burst(node *loadNode, n int) {
	p := lw.plan
	ramp, hold, length := time.Second, p.d(15*time.Second), p.d(45*time.Second)
	lw.upstream.setShape(streamShape{firstToken: 200 * time.Millisecond, every: 500 * time.Millisecond, length: length})
	lw.upstream.cancelled.Store(0)
	ctx, cancel := context.WithCancel(lw.t.Context())
	defer cancel()
	name := fmt.Sprintf("2. burst of %d new requests in 1 s, then all cancel", n)
	go func() {
		sleepCtx(ctx, ramp+hold)
		cancel()
	}()
	var idle time.Duration
	var open int64
	outs, peak := lw.phase(node, name, func() []outcome {
		outs := lw.driver.wave(ctx, n, ramp, lw.spec)
		idle, open = lw.awaitUpstreamIdle(length + 90*time.Second)
		return outs
	})
	lw.record(name, outs, peak)
	lw.note(name, "%d cancels reached the upstream; %d streams were still open there %v after the clients went (a node reads on to the end of a stream it cannot cancel, to bill it)",
		lw.upstream.cancelled.Load(), open, idle.Round(time.Second))
}

// largeRequests: scenario 3, requests of megabytes, which the node reads twice (docs/performance.md「延迟预算」), first a
// few at a time of each size and then a hundred of 4 MiB at once, for the node's memory.
func (lw *loadWorld) largeRequests() {
	p := lw.plan
	sizes := []struct {
		name string
		pad  int
	}{{"1 MiB", 1 << 20}, {"4 MiB", 4 << 20}, {"8 MiB", 8 << 20}}
	lw.upstream.setShape(streamShape{firstToken: 100 * time.Millisecond, every: 100 * time.Millisecond, length: time.Second})
	node := lw.node(nil)
	outs, peak := lw.phase(node, "3. large requests", func() []outcome {
		return lw.driver.closed(lw.t.Context(), p.n(16), p.d(30*time.Second), func(worker int) callSpec {
			s := sizes[worker%len(sizes)]
			c := lw.spec(worker)
			c.pad, c.group = s.pad, s.name
			return c
		})
	})
	for i, s := range sizes {
		var mine []outcome
		for _, o := range outs {
			if o.group == s.name {
				mine = append(mine, o)
			}
		}
		rowPeak := int64(0)
		if i == 0 {
			rowPeak = peak // the node's peak is one for all three sizes, shown once
		}
		lw.record(fmt.Sprintf("3. %s requests, %d workers over three sizes", s.name, p.n(16)), mine, rowPeak)
	}
	n := p.n(100)
	name := fmt.Sprintf("3. %d requests of 4 MiB at once", n)
	outs, peak = lw.phase(node, name, func() []outcome {
		return lw.driver.wave(lw.t.Context(), n, 0, func(i int) callSpec {
			c := lw.spec(i)
			c.pad = 4 << 20
			return c
		})
	})
	lw.record(name, outs, peak)
	lw.finish(node)
}

// hotAccount: scenario 4, one account and its one key under many calls, which serialise on its balance. The scenario also
// asks for 200 calls hitting a mid-stream extension at once; M1 reserves a call's whole output at admission and never
// extends it (#229), so there is no extension to wait on. The nearest thing it has is 200 streams that end in the same
// instant, whose settlements queue on the same lock, and that is run instead.
func (lw *loadWorld) hotAccount() {
	p := lw.plan
	lw.upstream.setShape(streamShape{firstToken: 100 * time.Millisecond, every: 100 * time.Millisecond, length: time.Second})
	node := lw.node(nil)
	hot := lw.hot
	for _, workers := range []int{100, 400} {
		name := fmt.Sprintf("4. one account, %d workers making 1 s calls", p.n(workers))
		outs, peak := lw.phase(node, name, func() []outcome {
			return lw.driver.closed(lw.t.Context(), p.n(workers), p.d(20*time.Second), hot)
		})
		lw.record(name, outs, peak)
	}
	n := p.n(200)
	lw.upstream.setShape(streamShape{
		firstToken: 100 * time.Millisecond, every: 100 * time.Millisecond, length: 3 * time.Second, endAt: time.Now().Add(7 * time.Second),
	})
	name := fmt.Sprintf("4. one account, %d streams ending in the same instant", n)
	outs, peak := lw.phase(node, name, func() []outcome { return lw.driver.wave(lw.t.Context(), n, time.Second, hot) })
	lw.record(name, outs, peak)
	lw.note("4", "a call extending its reservation mid-stream (200 of them waiting on the lock): not applicable in M1, which reserves in full at admission (#229)")
	lw.finish(node)
}

// coldKeyFlood: scenario 5, a flood of calls with keys the node has never seen, each of which costs a database lookup,
// on top of the hot account's calls; what the flood does to them is the difference between the two rows.
func (lw *loadWorld) coldKeyFlood() {
	p := lw.plan
	lw.upstream.setShape(streamShape{firstToken: 100 * time.Millisecond, every: 100 * time.Millisecond, length: time.Second})
	node := lw.node(nil)
	hot := lw.hot
	run := func() []outcome { return lw.driver.closed(lw.t.Context(), p.n(100), p.d(20*time.Second), hot) }
	base := "5. hot account, 100 workers, no flood"
	outs, peak := lw.phase(node, base, run)
	lw.record(base, outs, peak)

	var statuses map[int]int
	name := "5. hot account, 100 workers, with the flood"
	outs, peak = lw.phase(node, name, func() []outcome {
		var wg sync.WaitGroup
		wg.Go(func() {
			statuses = lw.driver.flood(lw.t.Context(), p.n(300), p.d(20*time.Second), func() string {
				return unknownKey(lw.hotKeys[0], func(n int) int { return rand.IntN(n) }) //nolint:gosec // unpredictability is not wanted of a flood's keys
			})
		})
		defer wg.Wait()
		return run()
	})
	lw.record(name, outs, peak)
	total := 0
	for _, n := range statuses {
		total += n
	}
	lw.note(name, "the flood sent %d calls with unknown keys in %v, answered by status: %v", total, p.d(20*time.Second), statuses)
	lw.finish(node)
}

// databaseOut: scenario 6, the database cut off for outage while 150 workers keep calling: before it, during it, and
// after it comes back. The node reaches the database through a proxy that closes its connections, so the shared cluster is
// never touched; the proxy's two extra hops are in every number of this scenario.
func (lw *loadWorld) databaseOut(outage time.Duration) {
	p := lw.plan
	proxy := newFaultProxy(lw.t, lw.databaseAddress())
	lw.upstream.setShape(streamShape{firstToken: 200 * time.Millisecond, every: 250 * time.Millisecond, length: p.d(20 * time.Second)})
	node := lw.node(proxy)
	pre, down, post := p.d(15*time.Second), p.d(outage), p.d(60*time.Second)
	t0 := time.Now()
	cutAt, backAt := t0.Add(pre), t0.Add(pre+down)
	var notReady, probes int
	done := make(chan struct{})
	go func() {
		defer close(done)
		sleepCtx(lw.t.Context(), time.Until(cutAt))
		proxy.setDown(true)
		for time.Now().Before(backAt) {
			probes++
			if !node.isReady() {
				notReady++
			}
			sleepCtx(lw.t.Context(), time.Second)
		}
		proxy.setDown(false)
	}()
	label := fmt.Sprintf("6. database out %s", outage)
	outs, peak := lw.phase(node, label, func() []outcome {
		return lw.driver.closed(lw.t.Context(), p.n(150), pre+down+post, lw.spec)
	})
	<-done
	lw.recordOutage(label, outs, peak, cutAt, backAt)
	lw.note(label, "/readyz failed in %d of %d probes during the outage", notReady, probes)
	lw.finish(node)
}

// recordOutage adds the rows of the calls that started before the cut, during it and after it, and says how long the
// first call after the database returned took to its first byte.
func (lw *loadWorld) recordOutage(label string, outs []outcome, peak int64, cutAt, backAt time.Time) {
	var before, during, after []outcome
	recovered := time.Duration(-1)
	for _, o := range outs {
		switch {
		case o.start.Before(cutAt):
			before = append(before, o)
		case o.start.Before(backAt):
			during = append(during, o)
		default:
			after = append(after, o)
			if first := o.start.Add(o.first).Sub(backAt); o.status == 200 && o.first > 0 && (recovered < 0 || first < recovered) {
				recovered = first
			}
		}
	}
	lw.record(label+": calls started before", before, peak)
	lw.record(label+": calls started during", during, 0)
	lw.record(label+": calls started after", after, 0)
	lw.note(label, "the first call started after the database came back reached its first byte %v later", recovered.Round(time.Millisecond))
}

// databaseAddress is the host and port the application's database URL names.
func (lw *loadWorld) databaseAddress() string {
	u, err := url.Parse(lw.database.App)
	if err != nil {
		lw.t.Fatal(err)
	}
	return net.JoinHostPort(u.Hostname(), u.Port())
}
