//go:build load

package main

import (
	"strconv"
	"strings"
	"time"
)

// What every scenario checks when its clients are done, mechanically: the node's side of the books is whole, so that a
// leak, a reservation that was never given back or a call charged wrongly shows as a failure and not as a number in a
// table that nobody checks.

// phase runs the calls of one measuring window against the node and checks the books after them.
func (lw *loadWorld) phase(n *loadNode, name string, run func() []outcome) (outs []outcome, peakMiB int64) {
	lw.t.Helper()
	before := lw.charged()
	n.meter.begin()
	outs = run()
	peakMiB = n.meter.end()
	lw.note(name, "%s", n.meter.context())
	lw.verify(name, outs, before)
	return outs, peakMiB
}

// charged is what the ledger has charged the load's accounts, in billionths.
func (lw *loadWorld) charged() int64 {
	lw.t.Helper()
	v, err := strconv.ParseInt(lw.query("SELECT coalesce(sum(charged), 0)::text FROM billing.reservations"), 10, 64)
	if err != nil {
		lw.t.Fatal(err)
	}
	return v
}

func (lw *loadWorld) pending() int {
	lw.t.Helper()
	n, err := strconv.Atoi(lw.query("SELECT count(*)::text FROM billing.reservations WHERE state = 'pending'"))
	if err != nil {
		lw.t.Fatal(err)
	}
	return n
}

// awaitSettled waits for every reservation to leave pending, up to limit, and says how long it took and how many were left.
func (lw *loadWorld) awaitSettled(limit time.Duration) (took time.Duration, left int) {
	lw.t.Helper()
	start := time.Now()
	for left = lw.pending(); left > 0 && time.Since(start) < limit; left = lw.pending() {
		time.Sleep(250 * time.Millisecond)
	}
	return time.Since(start), left
}

// verify checks what the clients' calls left in the ledger. No reservation may stay pending once the calls have ended
// (the node returns the money it held); the books balance; and what was charged is what the calls were worth: exactly,
// where every call that reached the node ended, and between what the finished ones cost and what the others could have
// reserved otherwise.
func (lw *loadWorld) verify(name string, outs []outcome, before int64) {
	lw.t.Helper()
	took, left := lw.awaitSettled(90 * time.Second)
	if left > 0 {
		lw.t.Errorf("%s: %d reservations are still pending %v after the calls ended: money the node did not give back", name, left, took.Round(time.Second))
	}
	lw.checkBalanced()
	low, high := chargeBounds(outs)
	charged := lw.charged() - before
	if charged < low || charged > high {
		lw.t.Errorf("%s: the ledger charged %d, want between %d (the calls that finished) and %d (and what the others may have reserved)", name, charged, low, high)
	}
	lw.checkEachCharge(name, outs)
	lw.note(name, "reservations back in %s; charged %d (finished calls %d, at most %d)", took.Round(100*time.Millisecond), charged, low, high)
}

// awaitUpstreamIdle waits until the upstream has no stream open, up to limit, and says how long it took.
func (lw *loadWorld) awaitUpstreamIdle(limit time.Duration) (time.Duration, int64) {
	start := time.Now()
	for lw.upstream.active.Load() > 0 && time.Since(start) < limit && lw.t.Context().Err() == nil {
		time.Sleep(100 * time.Millisecond)
	}
	return time.Since(start), lw.upstream.active.Load()
}

// finish checks that the node came out of the scenario whole, then stops it and says what it logged. A call after the
// scenario is admitted, which shows that its slots were given back; one with a 4 MiB body is too, which shows the
// memory its bodies held was.
func (lw *loadWorld) finish(n *loadNode) {
	lw.t.Helper()
	if n.alive() {
		lw.probe()
	} else {
		lw.t.Errorf("the node exited during the scenario: %v", n.result)
	}
	if err := n.stop(); err != nil {
		lw.t.Errorf("the node's shutdown: %v", err)
	}
	for _, line := range n.log.problems() {
		lw.note("node log", "%s", line)
	}
	lw.driver.close()
}

func (lw *loadWorld) probe() {
	lw.t.Helper()
	lw.upstream.setShape(streamShape{firstToken: 50 * time.Millisecond, every: 50 * time.Millisecond, length: 200 * time.Millisecond})
	for _, pad := range []int{0, 4 << 20} {
		o := lw.driver.run(lw.t.Context(), callSpec{key: lw.keys[len(lw.keys)-1], pad: pad})
		if !o.done {
			lw.t.Errorf("a call with %d bytes of padding after the scenario got status %d, %s: the node did not give back a slot or its memory", pad, o.status, o.err)
		}
	}
	lw.awaitSettled(30 * time.Second)
	lw.checkBalanced()
}

// checkEachCharge reads what the ledger charged each call that completed and compares it call by call.
func (lw *loadWorld) checkEachCharge(name string, outs []outcome) {
	lw.t.Helper()
	var ids []string
	for _, o := range outs {
		if o.done && o.seen && o.request != "" {
			ids = append(ids, o.request)
		}
	}
	rows := lw.query(`SELECT coalesce(string_agg(request_id::text || '=' || charged, ','), '') FROM billing.reservations
		WHERE request_id::text = ANY (string_to_array($1, ','))`, strings.Join(ids, ","))
	charged := map[string]int64{}
	for row := range strings.SplitSeq(rows, ",") {
		id, amount, ok := strings.Cut(row, "=")
		if n, err := strconv.ParseInt(amount, 10, 64); ok && err == nil {
			charged[id] = n
		}
	}
	wrong := chargeMismatches(outs, charged)
	for _, w := range wrong[:min(len(wrong), 5)] {
		lw.t.Errorf("%s: %s", name, w)
	}
	if len(wrong) > 5 {
		lw.t.Errorf("%s: and %d more calls charged wrongly", name, len(wrong)-5)
	}
}
