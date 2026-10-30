// Package quality measures endpoints and maintains their health loop. Attempts update fixed-size local evidence;
// the background loop persists breaker transitions and leases recovery probes. See internal/quality/README.md.
package quality

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

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

// The quality score's defaults (docs/domains/quality.md「质量分、吞吐与延迟」). They are what a good interactive answer is
// taken to mean, not what a model can do at best: the score separates endpoints of one model, so absolute references keep
// it comparable across models and across nodes. An administrator's setting of them comes with the console (H3).
const (
	scoreLatency = time.Second   // at or under it a time to first token scores 1
	scoreSpeed   = 50.0          // at or above it an output speed in tokens a second scores 1
	scoreSamples = 10            // the counted outcomes that make the confidence 1
	scoreMaxAge  = 4 * time.Hour // and the age at which it reaches 0: old evidence no longer ranks an endpoint
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
	Endpoint           suppliers.EndpointID
	Source, Credential uuid.UUID
	Request            uuid.UUID
	// Failure is the supplier's failure when no 2xx came; nil once one did.
	Failure *suppliers.Failure
	// Fault is why the read of the 2xx failed (protocol.FaultOf); FaultNone when it did not.
	Fault protocol.Fault
	Cut   Cut
	Gone  bool // the client had left
	// BreakerFallback says routing deliberately admitted this attempt as a breaker last resort.
	BreakerFallback bool
	// Delivered is set when the commit point came while the client was still there: the first content was the
	// client's. A client that left before it may still have its upstream read on, for the usage, without it.
	Delivered bool
	Done      bool // the upstream ended normally
	Empty     bool // it ended normally without content: a 200 with nothing in it
	// InputTokens is the request's input as estimated before it was sent (billing.InputEstimate).
	InputTokens  int64
	OutputTokens int64
	// Sent is when the attempt was sent; FirstToken when its first content came, zero when none did, whoever it went
	// to; Ended when the upstream's end or failure was read, before any wait of the gateway's own, which dates the
	// samples.
	Sent, FirstToken, Ended time.Time
}

func (o *Observation) content() bool { return !o.FirstToken.IsZero() }

// Measures are what routing ranks an endpoint by: its health and quality score, the confidence in that score, and its
// time to first token and output speed, 0 while the latter are not measured (fewer than minSamples samples, or none for
// maxAge). Quality combines all of them into the score routing orders by, and Confidence says how much and how recent
// the evidence is: an unknown endpoint has the neutral quality 1 and the confidence 0, never the low quality of a
// measured bad one.
type Measures struct {
	Health     float64
	Latency    time.Duration
	Throughput float64 // output tokens per second
	Quality    float64 // 0 to 1: reliability and the measured time to first token and speed
	Confidence float64 // 0 to 1: 0 when nothing was counted; decays after the last counted outcome
}

// Monitor keeps local endpoint evidence and live breaker state. Observe never accesses the database or allocates
// once the endpoint has been registered; Loop writes accepted transitions asynchronously.
type Monitor struct {
	deferred  atomic.Int64
	mu        sync.RWMutex
	endpoints map[suppliers.EndpointID]*State
	collected map[suppliers.EndpointID]struct{}

	unclassified   metric.Int64Counter // failures the adapters could not classify, which are not counted
	mode           Mode
	changes        chan change
	loop           *Loop
	suspectedBlock metric.Int64Counter // HTML answers, taken for a firewall on the route and not counted
}

// State holds one endpoint's measurements and live breaker under its own lock.
type State struct {
	generation  uint64
	id          suppliers.EndpointID
	mode        Mode
	credentials int
	window      window
	record      healthRecord
	owned       bool
	loaded      bool
	pending     int
	successes   int
	lastSeen    time.Time
	mu          sync.Mutex
	errorRate   float64 // the moving average of failures (1) and successes (0); 0 before any
	latency     average // seconds to the first token
	speed       average // output tokens per second
	samples     int     // counted outcomes, capped at scoreSamples: the confidence's evidence
	sampled     time.Time
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
	m := &Monitor{
		endpoints: map[suppliers.EndpointID]*State{},
		collected: map[suppliers.EndpointID]struct{}{},
		changes:   make(chan change, 1024),
		mode:      Observe,
		unclassified: counter(meter, "axiom.quality.unclassified",
			"Endpoint failures the adapter could not classify (an Unknown below 500), which do not count against health."),
		suspectedBlock: counter(meter, "axiom.quality.suspected_block",
			"HTML answers taken for a firewall on the route, which count against neither the endpoint nor the route."),
	}
	m.register(meter)
	return m
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
	latency := o.content() && o.InputTokens <= latencyMaxInput
	if v == uncounted && !latency {
		return
	}
	e := m.of(o.Endpoint)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.count(v, o.Ended)
	m.health(e, v, &o)
	if latency {
		e.latency.add(o.FirstToken.Sub(o.Sent).Seconds(), o.Ended)
	}
	if v == success && o.Done && o.content() && o.OutputTokens >= speedMinOutput && o.Ended.After(o.FirstToken) {
		e.speed.add(float64(o.OutputTokens)/o.Ended.Sub(o.FirstToken).Seconds(), o.Ended)
	}
}

// count moves the error rate by a counted outcome, and records it as quality evidence. A probe's result comes through
// here too; it is not an independent source, so it never enters the breaker window (health). e.mu is held.
func (e *State) count(v verdict, at time.Time) {
	switch v {
	case success:
		e.errorRate -= alpha * e.errorRate
	case failure, auxiliary:
		e.errorRate += alpha * (1 - e.errorRate)
	case uncounted:
		return
	}
	if e.samples < scoreSamples {
		e.samples++
	}
	e.sampled = at
}

// quality composes the score from the moving averages: reliability (1 minus the error rate) times the mean of the
// performance factors that are measured. An unmeasured factor is left out, never counted as a bad one; with neither
// measured the performance is neutral 1. Nothing counted is the unknown: the neutral score 1 and confidence 0, even
// when independent latency or speed samples are already measured—those stay in Measures but cannot rank the endpoint
// before its reliability has any evidence. The confidence is full at scoreSamples outcomes and linear to 0 after
// scoreMaxAge without one. e.mu is held.
func (e *State) quality(now time.Time) (score, confidence float64) {
	if e.samples == 0 {
		return 1, 0
	}
	perf, measured := 0.0, 0
	if v, ok := e.latency.measured(now); ok {
		perf += min(1, max(0, scoreLatency.Seconds()/v)) // a zero or negative sample scores 1
		measured++
	}
	if v, ok := e.speed.measured(now); ok {
		perf += min(1, max(0, v/scoreSpeed))
		measured++
	}
	if measured == 0 {
		perf = 1
	} else {
		perf /= float64(measured)
	}
	score = max(0, 1-e.errorRate) * perf
	age := max(0, now.Sub(e.sampled))
	return score, min(1, float64(e.samples)/scoreSamples) * max(0, 1-float64(age)/float64(scoreMaxAge))
}

// Measured is what the endpoint's numbers are now. An endpoint never observed is healthy and unmeasured, with the
// neutral quality and no confidence.
func (m *Monitor) Measured(id suppliers.EndpointID, now time.Time) Measures {
	m.mu.RLock()
	e := m.endpoints[id]
	m.mu.RUnlock()
	if e == nil {
		return Measures{Health: 1, Quality: 1}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := Measures{Health: 1 - e.errorRate}
	out.Quality, out.Confidence = e.quality(now)
	if v, ok := e.latency.measured(now); ok {
		out.Latency = time.Duration(v * float64(time.Second))
	}
	if v, ok := e.speed.measured(now); ok {
		out.Throughput = v
	}
	return out
}

func (m *Monitor) of(id suppliers.EndpointID) *State {
	m.mu.RLock()
	e := m.endpoints[id]
	m.mu.RUnlock()
	if e != nil {
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e = m.endpoints[id]; e == nil {
		_, collected := m.collected[id]
		delete(m.collected, id)
		e = &State{id: id, mode: m.mode, loaded: !collected}
		m.endpoints[id] = e
	}
	return e
}

func (m *Monitor) register(meter metric.Meter) {
	_, err := meter.Int64ObservableCounter("axiom.quality.health.deferred", metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
		o.Observe(m.deferred.Load())
		return nil
	}))
	if err != nil {
		panic(err)
	}
	_, err = meter.Int64ObservableGauge("axiom.quality.health.open", metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
		var n int64
		m.mu.RLock()
		defer m.mu.RUnlock()
		for _, e := range m.endpoints {
			e.mu.Lock()
			if e.record.Opened != 0 {
				n++
			}
			e.mu.Unlock()
		}
		o.Observe(n)
		return nil
	}))
	if err != nil {
		panic(err)
	}
}
