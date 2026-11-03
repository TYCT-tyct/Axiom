// Package quality measures endpoints and maintains their health loop. Attempts update fixed-size local evidence;
// the background loop persists breaker transitions and leases recovery probes. See internal/quality/README.md.
package quality

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/catalog"
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
	// Headers are the accepted 2xx response's headers, the evidence source classes are recognized by. They are only
	// read when a class rule names them and are not retained whole (docs/domains/quality.md「来源类别与指纹」).
	Headers http.Header
	// UsageItems are the billing items the upstream itself reported with a non-zero count in the response Headers came
	// from, the other fingerprint evidence. An item the gateway estimated is not one the upstream reported.
	UsageItems catalog.ItemSet
	// Config is the endpoint configuration the attempt was sent to: its catalog model, upstream name and formats. A
	// response from a configuration Prepare has since replaced is not evidence for the new one.
	Config EndpointConfig
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
	// LatencyPercentiles and ThroughputPercentiles are p50, p75, p90 and p99 over the last five minutes, 0 when fewer
	// than minSamples samples are that recent. They are copied here so routing reads them without a lock.
	LatencyPercentiles    [4]time.Duration
	ThroughputPercentiles [4]float64
	Quality               float64 // 0 to 1: reliability and the measured time to first token and speed
	Confidence            float64 // 0 to 1: 0 when nothing was counted; decays after the last counted outcome
	// Verified is set when a fingerprint check passed and is still recent (the configured maximum age), with no contradiction
	// since. An endpoint never checked is not verified; neither is one concluded fake (docs/domains/quality.md
	//「真伪检测」). VerifiedAt is when the last check passed, kept after it ages out or is contradicted, and
	// VerifyConfidence how much the pass still counts: 1 then, 0 at the maximum age, and 0 whenever Verified is false.
	Verified         bool
	VerifiedAt       time.Time
	VerifyConfidence float64
	// Class is the source class's stable slug, decided by the endpoint's last successful response and the live rules;
	// the fallback when no rule matched or no response has been seen. Routing filters on it and the call record names it.
	Class string
	// Facts are what detection measured of the endpoint's structure, by call format: a measurement of Chat says
	// nothing about Responses (docs/domains/quality.md「实测的能力与上限」).
	Facts map[protocol.Format]Facts
}

// Facts are what detection found out about an endpoint's structure, as opposed to how well it performs: a capability
// or a request parameter that works or does not, and the context and output ceilings it enforces. A measured value
// replaces the catalog's: Supported adds a capability or parameter the catalog did not declare, Unsupported removes
// one it did, and a non-zero Context or MaxOutput replaces the catalog's ceiling. A zero field was not measured and
// the catalog's value stands. Supported and Unsupported are disjoint: a measurement of one clears the same bit from
// the other, so a later measurement can flip an earlier one.
type Facts struct {
	Supported, Unsupported                     protocol.Capabilities
	SupportedParameters, UnsupportedParameters protocol.ParameterSet
	// Context and MaxOutput are the ceilings measurement found, in tokens; 0 is unmeasured. A measured ceiling replaces
	// the catalog's, higher or lower: the catalog is a baseline, and detection is the evidence for this endpoint.
	Context, MaxOutput int64
}

// add merges measurement m over f. m's statements replace what they cover; what m leaves zero stands. A support
// statement wins over its own lack in the same measurement, so a detector that reports both is read as working.
func (f *Facts) add(m Facts) {
	m.Unsupported &^= m.Supported
	m.UnsupportedParameters &^= m.SupportedParameters
	f.Supported = f.Supported&^m.Unsupported | m.Supported
	f.Unsupported = f.Unsupported&^m.Supported | m.Unsupported
	f.SupportedParameters = f.SupportedParameters&^m.UnsupportedParameters | m.SupportedParameters
	f.UnsupportedParameters = f.UnsupportedParameters&^m.SupportedParameters | m.UnsupportedParameters
	if m.Context > 0 {
		f.Context = m.Context
	}
	if m.MaxOutput > 0 {
		f.MaxOutput = m.MaxOutput
	}
}

// measuredFacts are the facts recorded for one call format, with the configuration they were measured on: a
// measurement of one upstream name, model or format says nothing about another
// (docs/domains/quality.md「实测的能力与上限」).
type measuredFacts struct {
	claim claim
	facts Facts
}

// EndpointConfig identifies the endpoint configuration a measurement belongs to: its model, upstream name and the set
// of formats it serves. A measurement that arrives after any of them changed is stale and must not overwrite the current
// configuration's facts.
type EndpointConfig struct {
	Model    uuid.UUID
	Upstream string
	Formats  uint16
}

// configOf is the configuration the endpoint presents now.
func configOf(ep suppliers.Routable) EndpointConfig {
	var formats uint16
	for _, f := range ep.Formats {
		formats |= 1 << f
	}
	return EndpointConfig{Model: ep.Model, Upstream: ep.Upstream, Formats: formats}
}

// Monitor keeps local endpoint evidence and live breaker state. Observe never accesses the database or allocates
// once the endpoint has been registered; Loop writes accepted transitions asynchronously.
type Monitor struct {
	deferred  atomic.Int64
	mu        sync.RWMutex
	endpoints map[suppliers.EndpointID]*State
	collected map[suppliers.EndpointID]struct{}

	unclassified   metric.Int64Counter // failures the adapters could not classify, which are not counted
	verifyAge      atomic.Int64        // how long a verification pass counts, in nanoseconds, from the probe settings
	mode           Mode
	changes        chan change
	loop           *Loop
	classes        atomic.Pointer[compiledClasses]
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
	probeable   bool // a platform endpoint with a generation format: the only kind the loop can re-check
	loaded      bool
	pending     int
	successes   int
	lastSeen    time.Time
	mu          sync.Mutex
	errorRate   float64 // the moving average of failures (1) and successes (0); 0 before any
	latency     average // seconds to the first token
	speed       average // output tokens per second
	// The rolling windows the percentile preferences read, fed by the same samples as latency and speed.
	latencyWindow sampleWindow
	speedWindow   sampleWindow
	samples       int // counted outcomes, capped at scoreSamples: the confidence's evidence
	sampled       time.Time
	verify        verification                      // the last fingerprint check and its schedule
	verifying     bool                              // a verification generation is in flight
	current       EndpointConfig                    // the configuration Prepare last published
	published     uint64                            // the supplier snapshot Prepare last accepted
	facts         map[protocol.Format]measuredFacts // what detection measured, per call format
	class         string                            // the source class's stable slug, from the last successful response
	evidence      classEvidence                     // the headers and usage items the live rules read
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
	m.verifyAge.Store(int64(defaultVerifyMaxAge))
	m.classes.Store(&compiledClasses{fallback: "relay"}) // Start replaces this with the table; before then nothing has a fingerprint
	m.register(meter)
	return m
}

// verificationAge is how long a pass counts, from the live probe settings.
func (m *Monitor) verificationAge() time.Duration {
	return time.Duration(m.verifyAge.Load())
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
	if v == uncounted && !latency && o.Failure != nil {
		return
	}
	e := m.of(o.Endpoint)
	e.mu.Lock()
	defer e.mu.Unlock()
	m.observeClassEvidence(e, &o, v) // a successful response's headers and usage are the fingerprint evidence
	e.count(v, o.Ended)
	m.health(e, v, &o)
	if latency {
		seconds := o.FirstToken.Sub(o.Sent).Seconds()
		e.latency.add(seconds, o.Ended)
		e.latencyWindow.add(seconds, o.FirstToken)
	}
	if v == success && o.Done && o.content() && o.OutputTokens >= speedMinOutput && o.Ended.After(o.FirstToken) {
		speed := float64(o.OutputTokens) / o.Ended.Sub(o.FirstToken).Seconds()
		e.speed.add(speed, o.Ended)
		e.speedWindow.add(speed, o.Ended)
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
// when independent latency or speed samples are already measured—those stay in Measures but do not make the quality
// score non-neutral before reliability has any evidence. The confidence is full at scoreSamples outcomes and linear to
// 0 after scoreMaxAge without one. e.mu is held.
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

// measures is what the endpoint's numbers are now; the caller holds e.mu.
func (e *State) measures(now time.Time, verifyMaxAge time.Duration) Measures {
	out := Measures{Health: 1 - e.errorRate}
	out.Quality, out.Confidence = e.quality(now)
	if v, ok := e.latency.measured(now); ok {
		out.Latency = time.Duration(v * float64(time.Second))
	}
	if v, ok := e.speed.measured(now); ok {
		out.Throughput = v
	}
	if v, ok := e.latencyWindow.percentiles(now); ok {
		for i := range v {
			out.LatencyPercentiles[i] = time.Duration(v[i] * float64(time.Second))
		}
	}
	if v, ok := e.speedWindow.percentiles(now); ok {
		out.ThroughputPercentiles = v
	}
	out.Verified = e.verify.verified(now, verifyMaxAge)
	out.VerifyConfidence = e.verify.confidence(now, verifyMaxAge)
	if !e.verify.checked.IsZero() {
		out.VerifiedAt = e.verify.checked
	}
	return out
}

// Measure records what detection established about an endpoint's structure in one call format: the capabilities and
// request parameters it found working and lacking, and the context and output ceilings it measured. The facts replace
// what was measured of the same fields in that format and are bound to the configuration measured; a configuration
// change voids them until the new one is measured (docs/domains/quality.md「实测的能力与上限」). It returns false when
// the format is not one the endpoint serves or the endpoint's current configuration is no longer the one passed: a
// result from an old configuration must not clear facts the new configuration has already recorded. It is not on the
// call path; detection calls it, and the next snapshot rebuild carries the change to routing.
func (m *Monitor) Measure(ep suppliers.Routable, format protocol.Format, facts Facts) bool {
	if !slices.Contains(ep.Formats, format) {
		return false
	}
	e := m.of(ep.ID)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current != configOf(ep) {
		return false
	}
	c := factClaim(ep, format)
	if e.facts == nil {
		e.facts = map[protocol.Format]measuredFacts{}
	}
	f := e.facts[format]
	if f.claim != c {
		f = measuredFacts{claim: c}
	}
	f.facts.add(facts)
	e.facts[format] = f
	return true
}

// observeClassEvidence records what a successful response says about its source. The verdict, not the HTTP status,
// decides: an idle timeout, an empty body or a broken stream after 2xx is a failure and its headers say nothing about
// where the endpoint's real answers come from. The attempt must also belong to the configuration Prepare last
// published: a response from an upstream the endpoint no longer uses cannot classify the new one. e.mu is held.
func (m *Monitor) observeClassEvidence(e *State, o *Observation, v verdict) {
	if v != success || o.Failure != nil || o.Config != e.current {
		return
	}
	if c := m.classes.Load(); c != nil {
		e.observeClass(c, o.Headers, o.UsageItems, o.Ended)
	}
}

// fallbackClass is the class an endpoint belongs to before a rule decides, or when nothing matches: the live table's
// fallback. New installs the relay fallback before Start reads the table.
func (m *Monitor) fallbackClass() string { return m.classes.Load().fallback }

// MeasuredFor is what the endpoint's numbers are now for a routable endpoint: a pass counts only for the configuration
// it checked, so an endpoint whose model, upstream name or format changed since the check is not verified even before
// the loop clears the state (docs/domains/quality.md「真伪检测」), and a measurement of its structure likewise says
// nothing about another configuration. The evidence and its claim are read in one critical section: a claim switched
// between two reads must not be paired with the previous claim's evidence.
//
//nolint:gocognit // one guarded read of the endpoint's state and its claims
func (m *Monitor) MeasuredFor(ep suppliers.Routable, now time.Time) Measures {
	m.mu.RLock()
	e := m.endpoints[ep.ID]
	m.mu.RUnlock()
	if e == nil {
		return Measures{Health: 1, Quality: 1, Class: m.fallbackClass()}
	}
	has := len(ep.Formats) > 0
	want := claim{}
	if has {
		want = claimOf(ep)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.measures(now, m.verificationAge())
	// The class was recognized from responses of the configuration Prepare last published; a caller holding another
	// (a routing build that read an older supplier generation) gets the fallback, as its facts are withheld below.
	out.Class = e.class
	if out.Class == "" || e.current != configOf(ep) {
		out.Class = m.fallbackClass()
	}
	if out.Verified && (!has || e.verify.claim != want) {
		out.Verified, out.VerifyConfidence, out.VerifiedAt = false, 0, time.Time{}
	}
	out.Facts = nil
	if has && e.current == configOf(ep) {
		for f, mf := range e.facts {
			if mf.claim != factClaim(ep, f) {
				continue
			}
			if out.Facts == nil {
				out.Facts = make(map[protocol.Format]Facts, len(e.facts))
			}
			out.Facts[f] = mf.facts
		}
	}
	return out
}

// probeFormat picks the call format a health probe uses: the first generation format the endpoint serves. Embeddings,
// rerank and other direct formats cannot answer a generation probe, so an endpoint that serves only those is not probed.
func probeFormat(formats []protocol.Format) (protocol.Format, bool) {
	for _, f := range formats {
		switch f {
		case protocol.Messages, protocol.Responses, protocol.Chat, protocol.Completions:
			return f, true
		case protocol.Embeddings, protocol.Rerank: // direct formats cannot answer a generation
		}
	}
	return 0, false
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
		e = &State{id: id, mode: m.mode, probeable: true, loaded: !collected}
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
