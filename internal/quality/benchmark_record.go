package quality

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/suppliers"
)

// benchmarkState is one endpoint's score and the official baseline it was computed against. A later baseline makes the
// score incomparable rather than merely old, so it is withheld until the endpoint is run again. config binds the score
// to the model, upstream name and format set it was run on; epoch is the configuration generation it was measured
// under. A stored score is adopted only when both still match the endpoint.
type benchmarkState struct {
	version    string
	hash       string
	baseline   time.Time
	score      float64
	confidence float64
	at         time.Time
	config     EndpointConfig
	epoch      int64
}

// benchmarkConfig is one endpoint's current configuration and its shared generation. A generation advances when the
// configuration changes, and every score carries the generation it was run under.
type benchmarkConfig struct {
	config EndpointConfig
	epoch  int64
}

// newerScore reports whether a is later evidence than b. A run against a later baseline replaces a run against an
// earlier one whatever their run times: the earlier one is incomparable and withheld, so it cannot block the new
// baseline's score. Against the same baseline, a later run replaces an earlier one.
func newerScore(a, b benchmarkState) bool {
	if b.at.IsZero() {
		return true
	}
	if a.baseline.After(b.baseline) {
		return true
	}
	return a.baseline.Equal(b.baseline) && a.at.After(b.at)
}

// measures is the score routing and the public list read: zero and no confidence without a run on the current
// configuration, against the current baseline, in this question set, and younger than benchmarkMaxAge. The confidence
// decays linearly to zero at the age limit; the score and its time remain for "last measured" until it is replaced.
func (b benchmarkState) measures(now time.Time, version, hash string, baseline time.Time, current bool, epoch int64) (score, confidence float64, at time.Time) {
	if !current || b.at.IsZero() || b.version != version || b.hash != hash || !b.baseline.Equal(baseline) || b.epoch != epoch {
		return 0, 0, time.Time{}
	}
	age := max(0, now.Sub(b.at)) // a run timestamped by a clock slightly ahead of this one is not older than this one
	if age >= benchmarkMaxAge {
		return 0, 0, b.at
	}
	return b.score, b.confidence * (1 - float64(age)/float64(benchmarkMaxAge)), b.at
}

// productScore is what MeasuredFor returns for the product score: no baseline, no run, another set, an older baseline or
// a changed configuration all mean zero and no confidence, and an aged-out anchor or score means zero but keeps the
// last run's time so "never measured" and "measured, then stale" stay distinguishable.
func (b benchmarkState) productScore(now time.Time, baseline benchmarkAnchor, hasBaseline, current bool, epoch int64) (score, confidence float64, at time.Time) {
	if !hasBaseline {
		return 0, 0, time.Time{}
	}
	score, confidence, at = b.measures(now, baseline.version, baseline.hash, baseline.at, current, epoch)
	if now.Sub(baseline.at) >= benchmarkMaxAge {
		return 0, 0, at
	}
	return score, confidence, at
}

// benchmarkStateOf is the record of a scored run: what routing reads and what the store writes.
func benchmarkStateOf(score BenchmarkResult, baseline time.Time, ep suppliers.Routable) benchmarkState {
	return benchmarkState{
		version: score.Version, hash: score.Hash, baseline: baseline,
		score: score.Score, confidence: score.Confidence, at: score.At, config: configOf(ep),
	}
}

// RecordOfficialBenchmark records the official direct endpoint's run for a catalog model: the anchor its other endpoints
// are scored against. Only a platform endpoint can be the official run; a tenant's own key measures that tenant's
// endpoint, not the official service. The caller must pass the catalog author's own API, not a cloud host or a relay:
// this package can see only the platform flag. A baseline older than the one in force is rejected, so a late result
// cannot replace a newer anchor. The baseline and the official endpoint's own 100 are stored in one transaction before
// either is published, so a recorded run survives a restart; a store failure returns false and leaves the previous
// anchor in force. The endpoint's in-memory 100 waits for its snapshot to be current, but the stored row is ready for
// the next Prepare.
func (m *Monitor) RecordOfficialBenchmark(ctx context.Context, ep suppliers.Routable, run BenchmarkRun) bool {
	run = cloneBenchmarkRun(run)
	set := fixedBenchmark()
	if ep.Model == (uuid.UUID{}) || !benchmarkRunAllowed(ep, run, set) {
		return false
	}
	anchor, ok := anchorOf(set, run)
	if !ok {
		return false
	}
	anchor.endpoint = ep.ID
	// A baseline must itself meet the coverage and points floor a candidate is held to; a partial official run would
	// silently lower the anchor every other endpoint is measured against.
	official, ok := scoreAgainst(set, anchor, run)
	if !ok {
		return false
	}
	epoch, err := m.ensureConfig(ctx, ep)
	if err != nil {
		return false
	}
	state := benchmarkStateOf(official, anchor.at, ep)
	state.epoch = epoch
	won, storedEpoch := m.storeBaseline(ctx, ep, anchor, state)
	if !won {
		return false
	}
	state.epoch = storedEpoch
	m.mu.Lock()
	if old, ok := m.benchmarks[ep.Model]; ok && !anchor.at.After(old.at) {
		m.mu.Unlock()
		return false
	}
	m.benchmarks[ep.Model] = anchor
	if old, ok := m.scores[ep.ID]; !ok || newerScore(state, old) {
		m.scores[ep.ID] = state
	}
	m.mu.Unlock()
	m.commitBenchmark(ep, anchor, state) // best effort: a snapshot that is not current adopts the stored row later
	return true
}

// benchmarkConfigCurrent reports whether the endpoint still publishes the configuration the run measured, and records
// a configuration row when the endpoint has never had one. The score write repeats the configuration check atomically,
// so a change between this check and the write cannot leave an old-configuration row.
func (m *Monitor) benchmarkConfigCurrent(ctx context.Context, ep suppliers.Routable) bool {
	e := m.of(ep.ID)
	e.mu.Lock()
	current := e.current == configOf(ep)
	e.mu.Unlock()
	return current && m.configForScore(ctx, ep)
}

// RecordBenchmark scores an endpoint's run against its model's official baseline and records it. It returns false for
// a tenant endpoint (the platform does not spend a tenant's key on a benchmark), a run of another question set, without
// a baseline, against an anchor that has already aged out, from another configuration than the one the snapshot
// published, older than the score already recorded, that does not cover enough questions, or that could not be stored.
// The score is evidence for the configuration it was run on; a later Prepare that changes the model, upstream name or
// formats clears it.
func (m *Monitor) RecordBenchmark(ctx context.Context, ep suppliers.Routable, run BenchmarkRun) (BenchmarkResult, bool) {
	set := fixedBenchmark()
	if !benchmarkRunAllowed(ep, run, set) {
		return BenchmarkResult{}, false
	}
	m.mu.RLock()
	baseline, ok := m.benchmarks[ep.Model]
	m.mu.RUnlock()
	if !ok || run.Format != baseline.format || run.At.Before(baseline.at) || run.At.Sub(baseline.at) >= benchmarkMaxAge {
		return BenchmarkResult{}, false
	}
	score, ok := scoreAgainst(set, baseline, run)
	if !ok {
		return BenchmarkResult{}, false
	}
	// Reject a stale configuration before the write, not after it: commitBenchmark would refuse it too, but the row
	// would remain and a later refresh could adopt it if the configuration came back. A brand-new endpoint also gets
	// its configuration row here, not in Prepare: a routing build may look at an endpoint that never ran. The
	// insert-if-absent never replaces a configuration another node already stored; if it finds one, the score write
	// below is the atomic arbiter.
	if !m.benchmarkConfigCurrent(ctx, ep) {
		return BenchmarkResult{}, false
	}
	state := benchmarkStateOf(score, baseline.at, ep)
	won, epoch := m.storeScore(ctx, ep, state)
	if !won {
		return BenchmarkResult{}, false
	}
	state.epoch = epoch
	m.mu.Lock()
	if old, ok := m.scores[ep.ID]; !ok || newerScore(state, old) {
		m.scores[ep.ID] = state
	}
	m.mu.Unlock()
	if !m.commitBenchmark(ep, baseline, state) {
		return BenchmarkResult{}, false
	}
	return score, true
}

// commitBenchmark stores a score only if the baseline and configuration generation it was scored against are still the
// current ones. The monitor lock stays held across the endpoint commit, so a replacement cannot slip between the check
// and the store.
func (m *Monitor) commitBenchmark(ep suppliers.Routable, baseline benchmarkAnchor, state benchmarkState) bool {
	e := m.of(ep.ID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	current, ok := m.benchmarks[ep.Model]
	if !ok || !current.at.Equal(baseline.at) || current.format != baseline.format || current.hash != baseline.hash {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current == state.config && e.benchmarkEpoch == 0 && state.epoch > 0 {
		e.benchmarkEpoch = state.epoch // the first score on a fresh endpoint establishes the generation
	}
	if e.current != state.config || state.epoch != e.benchmarkEpoch || !newerScore(state, e.benchmark) {
		return false
	}
	e.benchmark = state
	return true
}
