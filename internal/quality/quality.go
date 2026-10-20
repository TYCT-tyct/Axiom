// Package quality judges endpoints by what they do. H1 measures real traffic: every attempt the gateway makes ends in
// Observe, which judges it by the one table of what counts as a failure and keeps, per endpoint, its health, time to
// first token and output speed; routing reads them through the snapshot. internal/quality/README.md has the design.
package quality

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// The measurement rules of docs/domains/quality.md「健康度」「质量分、吞吐与延迟」. They are the defaults the design
// gives; an administrator's setting of them comes with the console (H3).
const (
	alpha           = 0.2              // the weight of a new sample in every moving average, as sub2api's
	minSamples      = 5                // a latency or a speed rests on at least this many samples to count as measured
	maxAge          = 30 * time.Minute // and its last sample is no older than this
	latencyMaxInput = 4 << 10          // only an attempt whose input is estimated at no more tokens times its first token
	speedMinOutput  = 50               // only an attempt that output at least this many tokens times its speed
	slowMaxInput    = 32 << 10         // a timeout is a failure, an auxiliary one, only for an input up to this
)

// Cut is why the gateway cancelled an attempt, when it did.
type Cut uint8

// The cuts.
const (
	NotCut     Cut = iota
	HeaderWait     // the response headers did not come in time
	Idle           // the upstream sent nothing new in time
	Ours           // the call's own end: its deadline, the node's shutdown or memory, an extension refused, the holder
)

// Observation is how one attempt at an endpoint ended, as the gateway saw it: the facts, not a verdict, which is the
// table's (judge).
type Observation struct {
	Endpoint suppliers.EndpointID
	// Failure is the supplier's failure when no 2xx came; nil once one did.
	Failure *suppliers.Failure
	// Fault is why the read of the 2xx failed (protocol.FaultOf); FaultNone when it did not.
	Fault protocol.Fault
	Cut   Cut
	Gone  bool // the client had left
	Done  bool // the upstream ended normally
	Empty bool // it ended normally without content: a 200 with nothing in it
	// InputTokens is the request's input as estimated before it was sent (billing.InputEstimate).
	InputTokens  int64
	OutputTokens int64
	// Sent is when the attempt was sent; FirstToken when its first content came, zero when none did; Ended when it
	// ended, which dates the samples.
	Sent, FirstToken, Ended time.Time
}

func (o *Observation) committed() bool { return !o.FirstToken.IsZero() }

// Measures are what routing ranks an endpoint by: its health, 1 when nothing was counted, and its time to first token
// and output speed, 0 while they are not measured (fewer than minSamples samples, or none for maxAge).
type Measures struct {
	Health     float64
	Latency    time.Duration
	Throughput float64 // output tokens per second
}

// Monitor keeps the measures of every endpoint observed, in the process: nothing here is shared between nodes or
// written to a table (H2). Observe is on the call path and allocates nothing once the endpoint has been seen.
type Monitor struct {
	mu        sync.RWMutex
	endpoints map[suppliers.EndpointID]*endpoint

	unclassified   metric.Int64Counter // failures the adapters could not classify, which are not counted
	suspectedBlock metric.Int64Counter // HTML answers, taken for a firewall on the route and not counted
}

// endpoint is one endpoint's numbers, under its own lock.
type endpoint struct {
	mu        sync.Mutex
	errorRate float64 // the moving average of failures (1) and successes (0); 0 before any
	latency   average // seconds to the first token
	speed     average // output tokens per second
}

// average is a moving average with how many samples it rests on and when the last one came.
type average struct {
	value float64
	n     int64
	last  time.Time
}

func (a *average) add(x float64, at time.Time) {
	if a.n == 0 {
		a.value = x // the first sample is the average, as sub2api's time to first token
	} else {
		a.value += alpha * (x - a.value)
	}
	a.n++
	a.last = at
}

func (a *average) measured(now time.Time) (float64, bool) {
	return a.value, a.n >= minSamples && now.Sub(a.last) <= maxAge
}

// New makes a Monitor that reports its counters to meter, or to none when meter is nil.
func New(meter metric.Meter) *Monitor {
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("quality")
	}
	return &Monitor{
		endpoints: map[suppliers.EndpointID]*endpoint{},
		unclassified: counter(meter, "axiom.quality.unclassified",
			"Endpoint failures the adapter could not classify (an Unknown below 500), which do not count against health."),
		suspectedBlock: counter(meter, "axiom.quality.suspected_block",
			"HTML answers taken for a firewall on the route, which count against neither the endpoint nor the route."),
	}
}

func counter(m metric.Meter, name, description string) metric.Int64Counter {
	c, err := m.Int64Counter(name, metric.WithDescription(description))
	if err != nil {
		panic(err) // the instrument name is a constant
	}
	return c
}

// Observe takes one attempt's end: the table says whether it counts for the endpoint's health, and its time to first
// token and speed are sampled when the rules allow. The gateway calls it once per attempt, when the attempt closes; ctx is
// only for the counters, and may have ended.
func (m *Monitor) Observe(ctx context.Context, o Observation) {
	v := m.judge(ctx, &o)
	latency := o.committed() && o.InputTokens <= latencyMaxInput
	if v == uncounted && !latency {
		return
	}
	e := m.of(o.Endpoint)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.count(v)
	if latency {
		e.latency.add(o.FirstToken.Sub(o.Sent).Seconds(), o.Ended)
	}
	if v == success && o.Done && o.committed() && o.OutputTokens >= speedMinOutput && o.Ended.After(o.FirstToken) {
		e.speed.add(float64(o.OutputTokens)/o.Ended.Sub(o.FirstToken).Seconds(), o.Ended)
	}
}

// count moves the error rate by a counted outcome. e.mu is held.
func (e *endpoint) count(v verdict) {
	switch v {
	case success:
		e.errorRate -= alpha * e.errorRate
	case failure, auxiliary:
		e.errorRate += alpha * (1 - e.errorRate)
	case uncounted:
	}
}

// Measured is what the endpoint's numbers are now. An endpoint never observed is healthy and unmeasured.
func (m *Monitor) Measured(id suppliers.EndpointID, now time.Time) Measures {
	m.mu.RLock()
	e := m.endpoints[id]
	m.mu.RUnlock()
	if e == nil {
		return Measures{Health: 1}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := Measures{Health: 1 - e.errorRate}
	if v, ok := e.latency.measured(now); ok {
		out.Latency = time.Duration(v * float64(time.Second))
	}
	if v, ok := e.speed.measured(now); ok {
		out.Throughput = v
	}
	return out
}

func (m *Monitor) of(id suppliers.EndpointID) *endpoint {
	m.mu.RLock()
	e := m.endpoints[id]
	m.mu.RUnlock()
	if e != nil {
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e = m.endpoints[id]; e == nil {
		e = new(endpoint)
		m.endpoints[id] = e
	}
	return e
}
