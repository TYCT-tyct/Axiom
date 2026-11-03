package quality

import (
	"testing"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// TestBenchmarkScoreWaitsForItsConfiguration: a score recorded for a configuration that another node has already
// published is kept aside until this node publishes the same configuration. Merely reading it for a different
// configuration must not mark it as handled.
func TestBenchmarkScoreWaitsForItsConfiguration(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Observe)
	official, candidate := benchmarkEndpoints()
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)

	second := &probeFixture{
		endpoints: []suppliers.Routable{official, candidate},
		calls:     make(chan suppliers.TestRequest, 1),
		release:   make(chan struct{}, 1),
	}
	t.Cleanup(func() { close(second.release) })
	other, otherLoop, err := Start(t.Context(), Config{
		DB: l.db, Scope: l.scope, Mode: Observe, Source: second, Pauses: second,
		Now: func() time.Time { return *now }, Rand: func() float64 { return .5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, otherLoop)

	set := fixedBenchmark()
	at := *now
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, at, "yes")) {
		t.Fatal("the official baseline was refused")
	}

	// The first node moves the candidate to a new upstream and records its score there.
	changed := candidate
	changed.Upstream = "relay-2"
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, changed}
	f.mu.Unlock()
	tick(t, l)
	want, ok := m.RecordBenchmark(t.Context(), changed, runOfSet(set, at.Add(time.Minute), "yes"))
	if !ok {
		t.Fatal("the candidate run on the new configuration was refused")
	}

	// The second node still publishes the old configuration; the new score arrives first and must stay pending.
	*now = now.Add(benchmarkEvery)
	tick(t, otherLoop)
	if got := other.MeasuredFor(candidate, want.At); got.ProductScore != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("the old configuration adopted a score for the new one: %+v", got)
	}

	// Once the second node publishes the configuration the score names, the row recorded earlier is adopted.
	second.mu.Lock()
	second.endpoints = []suppliers.Routable{official, changed}
	second.mu.Unlock()
	tick(t, otherLoop)
	if got := other.MeasuredFor(changed, want.At); got.ProductScore != want.Score || !got.ProductScoreAt.Equal(want.At) {
		t.Fatalf("the second node read %+v, want %v at %v after publishing the configuration", got, want.Score, want.At)
	}
}

// TestBenchmarkConfigurationRoundTripSurvivesRestart: a score voided by a configuration change stays voided when the
// configuration returns, including on another node and after a restart that reloads the shared generation.
//
//nolint:cyclop // one test walks the local node, the second node and the restart in order
func TestBenchmarkConfigurationRoundTripSurvivesRestart(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Observe)
	official, candidate := benchmarkEndpoints()
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)

	set := fixedBenchmark()
	at := *now
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, at, "yes")) {
		t.Fatal("the official baseline was refused")
	}
	if _, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, at.Add(time.Minute), "yes")); !ok {
		t.Fatal("the candidate run was refused")
	}

	// A second node adopts the old score before the configuration moves. It must drop it when the shared generation
	// advances, even though it never published the intermediate configuration itself.
	second := &probeFixture{
		endpoints: []suppliers.Routable{official, candidate},
		calls:     make(chan suppliers.TestRequest, 1),
		release:   make(chan struct{}, 1),
	}
	t.Cleanup(func() { close(second.release) })
	other, otherLoop, err := Start(t.Context(), Config{
		DB: l.db, Scope: l.scope, Mode: Observe, Source: second, Pauses: second,
		Now: func() time.Time { return *now }, Rand: func() float64 { return .5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, otherLoop)
	if got := other.MeasuredFor(candidate, at.Add(time.Minute)); got.ProductScore == 0 {
		t.Fatalf("the second node did not adopt the score before the change: %+v", got)
	}

	changed := candidate
	changed.Upstream = "relay-2"
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, changed}
	f.mu.Unlock()
	tick(t, l)

	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)
	if got := m.MeasuredFor(candidate, at.Add(2*time.Minute)); got.ProductScore != 0 {
		t.Fatalf("a voided score came back before the restart: %+v", got)
	}
	*now = now.Add(benchmarkEvery)
	tick(t, otherLoop)
	if got := other.MeasuredFor(candidate, at.Add(2*time.Minute)); got.ProductScore != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("the other node kept a score after the shared generation advanced: %+v", got)
	}

	restarted, restartedLoop, err := Start(t.Context(), Config{
		DB: l.db, Scope: l.scope, Mode: Observe, Source: f, Pauses: f,
		Now: func() time.Time { return *now }, Rand: func() float64 { return .5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, restartedLoop)
	if got := restarted.MeasuredFor(candidate, at.Add(2*time.Minute)); got.ProductScore != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("the restart resurrected a voided score: %+v", got)
	}

	fresh, ok := restarted.RecordBenchmark(t.Context(), candidate, runOfSet(set, at.Add(2*time.Minute), "yes"))
	if !ok {
		t.Fatal("a fresh run after the restart was refused")
	}
	if got := restarted.MeasuredFor(candidate, fresh.At); got.ProductScore != fresh.Score || !got.ProductScoreAt.Equal(fresh.At) {
		t.Fatalf("the fresh run read %+v, want %v at %v", got, fresh.Score, fresh.At)
	}
}

// TestBenchmarkScoreCannotOutliveItsConfigurationWrite: the pre-write configuration check can be passed and the write
// can still land after another Prepare changed the configuration. The store must reject it atomically; otherwise a
// later return to the old configuration could adopt the row.
func TestBenchmarkScoreCannotOutliveItsConfigurationWrite(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Observe)
	official, candidate := benchmarkEndpoints()
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)

	set := fixedBenchmark()
	at := *now
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, at, "yes")) {
		t.Fatal("the official baseline was refused")
	}
	m.mu.RLock()
	baseline := m.benchmarks[official.Model]
	m.mu.RUnlock()
	result, ok := scoreAgainst(set, baseline, runOfSet(set, at.Add(time.Minute), "yes"))
	if !ok {
		t.Fatal("the candidate run did not score against the baseline")
	}
	state := benchmarkStateOf(result, baseline.at, candidate)
	epoch, configOK := m.configForScore(t.Context(), candidate)
	if !configOK {
		t.Fatal("the candidate configuration was not recorded")
	}
	state.epoch = epoch

	changed := candidate
	changed.Upstream = "relay-2"
	// A configuration change commits before Prepare publishes it. The cached generation must already stop the old
	// configuration's score from becoming visible.
	if _, err := m.ensureConfig(t.Context(), changed); err != nil {
		t.Fatal(err)
	}
	if m.commitBenchmark(candidate, baseline, state) {
		t.Fatal("a score for the committed configuration change was published")
	}
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, changed}
	f.mu.Unlock()
	tick(t, l)

	// The run passed its configuration check before the switch and reaches the store after it.
	if won, _, err := l.saveScore(t.Context(), candidate, state); err != nil || won {
		t.Fatalf("a score for the replaced configuration was stored: won=%v err=%v", won, err)
	}

	// Returning to the old configuration must not find the rejected row.
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)
	restarted, restartedLoop, err := Start(t.Context(), Config{
		DB: l.db, Scope: l.scope, Mode: Observe, Source: f, Pauses: f,
		Now: func() time.Time { return *now }, Rand: func() float64 { return .5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, restartedLoop)
	if got := restarted.MeasuredFor(candidate, at.Add(time.Minute)); got.ProductScore != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("the rejected score came back after the restart: %+v", got)
	}
}

// TestBenchmarkScoreWaitsForAnUncommittedConfigurationChange: the score insert takes a shared lock on the configuration
// row, so it cannot read the old configuration while a configuration change is being committed. Without the lock the
// insert could see the old row, the change's delete could miss the not-yet-committed score, and both could commit.
//
//nolint:cyclop,gocognit // the test holds a transaction open while it checks that the write waits
func TestBenchmarkScoreWaitsForAnUncommittedConfigurationChange(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Observe)
	official, candidate := benchmarkEndpoints()
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)
	configEpoch, configOK := m.configForScore(t.Context(), candidate)
	if !configOK {
		t.Fatal("the candidate configuration was not recorded")
	}

	set := fixedBenchmark()
	at := *now
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, at, "yes")) {
		t.Fatal("the official baseline was refused")
	}
	m.mu.RLock()
	baseline := m.benchmarks[official.Model]
	m.mu.RUnlock()
	result, ok := scoreAgainst(set, baseline, runOfSet(set, at.Add(time.Minute), "yes"))
	if !ok {
		t.Fatal("the candidate run did not score against the baseline")
	}
	state := benchmarkStateOf(result, baseline.at, candidate)
	state.epoch = configEpoch

	changed := candidate
	changed.Upstream = "relay-2"
	ready := make(chan struct{})
	release := make(chan struct{})
	txDone := make(chan error, 1)
	go func() {
		txDone <- l.db.Tx(t.Context(), l.scope, func(tx pg.Tx) error {
			cfg := configOf(changed)
			_, err := tx.Exec(t.Context(), `UPDATE quality.benchmark_configs
 SET model_id=$2,upstream=$3,formats=$4,epoch=epoch+1,updated_at=now() WHERE endpoint_id=$1`,
				changed.ID, cfg.Model, cfg.Upstream, int32(cfg.Formats))
			if err == nil {
				_, err = tx.Exec(t.Context(), `DELETE FROM quality.benchmark_scores WHERE endpoint_id=$1`, changed.ID)
			}
			close(ready)
			if err != nil {
				return err
			}
			<-release
			return nil
		})
	}()
	select {
	case <-ready:
	case err := <-txDone:
		t.Fatalf("the configuration transaction failed before it held the row: %v", err)
	}

	scoreDone := make(chan struct{})
	var won bool
	var scoreErr error
	go func() {
		won, _, scoreErr = l.saveScore(t.Context(), candidate, state)
		close(scoreDone)
	}()
	blocked := false
	select {
	case <-scoreDone:
	case <-time.After(time.Second):
		blocked = true
	}
	close(release)
	if err := <-txDone; err != nil {
		t.Fatal(err)
	}
	<-scoreDone
	if !blocked {
		t.Fatal("the score write did not wait for the configuration transaction")
	}
	if scoreErr != nil || won {
		t.Fatalf("a score for the replaced configuration was stored: won=%v err=%v", won, scoreErr)
	}
	var count int
	if err := l.db.ReadTx(t.Context(), l.scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM quality.benchmark_scores
 WHERE endpoint_id=$1 AND version=$2`, candidate.ID, state.version).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("the store kept %d scores for the replaced configuration", count)
	}
}

// TestBenchmarkStaleRunCannotCrossConfigurationRoundTrip: a run checked against configuration A must not be accepted
// after A→B→A. The fields match again, but the shared generation advanced twice; the write has to compare the generation
// it checked, not only the fields it sees at write time.
func TestBenchmarkStaleRunCannotCrossConfigurationRoundTrip(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Observe)
	official, candidate := benchmarkEndpoints()
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)

	set := fixedBenchmark()
	at := *now
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, at, "yes")) {
		t.Fatal("the official baseline was refused")
	}
	if _, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, at.Add(time.Minute), "yes")); !ok {
		t.Fatal("the candidate run was refused")
	}
	m.mu.RLock()
	baseline := m.benchmarks[official.Model]
	m.mu.RUnlock()
	result, ok := scoreAgainst(set, baseline, runOfSet(set, at.Add(2*time.Minute), "yes"))
	if !ok {
		t.Fatal("the stale run did not score against the baseline")
	}
	stale := benchmarkStateOf(result, baseline.at, candidate)
	epoch, configOK := m.configForScore(t.Context(), candidate)
	if !configOK {
		t.Fatal("the candidate configuration was not recorded")
	}
	stale.epoch = epoch

	changed := candidate
	changed.Upstream = "relay-2"
	for _, endpoints := range [][]suppliers.Routable{
		{official, changed},
		{official, candidate},
	} {
		f.mu.Lock()
		f.endpoints = endpoints
		f.mu.Unlock()
		tick(t, l)
	}

	if won, _, err := l.saveScore(t.Context(), candidate, stale); err != nil || won {
		t.Fatalf("a run from before the configuration round trip was stored: won=%v err=%v", won, err)
	}
	if got := m.MeasuredFor(candidate, at.Add(2*time.Minute)); got.ProductScore != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("the round trip resurrected the stale run: %+v", got)
	}
}

// TestBenchmarkRestartSyncsChangedConfiguration: a process restart loads the stored configuration A and the supplier's
// new authoritative configuration B. No configuration has been published in this process yet, so Prepare must sync the
// shared row to B; otherwise every score for B is rejected forever.
func TestBenchmarkRestartSyncsChangedConfiguration(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Observe)
	official, candidate := benchmarkEndpoints()
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)

	set := fixedBenchmark()
	at := *now
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, at, "yes")) {
		t.Fatal("the official baseline was refused")
	}
	if _, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, at.Add(time.Minute), "yes")); !ok {
		t.Fatal("the candidate run before the restart was refused")
	}

	changed := candidate
	changed.Upstream = "relay-2"
	second := &probeFixture{
		endpoints: []suppliers.Routable{official, changed},
		calls:     make(chan suppliers.TestRequest, 1),
		release:   make(chan struct{}, 1),
	}
	t.Cleanup(func() { close(second.release) })
	restarted, restartedLoop, err := Start(t.Context(), Config{
		DB: l.db, Scope: l.scope, Mode: Observe, Source: second, Pauses: second,
		Now: func() time.Time { return *now }, Rand: func() float64 { return .5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, restartedLoop)
	if stored, ok := restarted.storedConfig(candidate.ID); !ok || stored.config != configOf(changed) {
		t.Fatalf("the restart kept the old configuration: %+v, %v", stored, ok)
	}
	result, ok := restarted.RecordBenchmark(t.Context(), changed, runOfSet(set, at.Add(2*time.Minute), "yes"))
	if !ok {
		t.Fatal("a new run on the restarted configuration was refused")
	}
	if got := restarted.MeasuredFor(changed, result.At); got.ProductScore != result.Score {
		t.Fatalf("the restarted configuration read %+v, want %v", got, result.Score)
	}
}

// TestBenchmarkOfficialStaleRunKeepsNewerConfiguration: a late official run of configuration A must not flip the shared
// configuration back to A (and delete B's valid 100) when its baseline loses to the newer B baseline.
func TestBenchmarkOfficialStaleRunKeepsNewerConfiguration(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Observe)
	official, _ := benchmarkEndpoints()
	changed := official
	changed.Upstream = "relay-2"
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official}
	f.mu.Unlock()
	tick(t, l)

	set := fixedBenchmark()
	at := *now
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, at, "yes")) {
		t.Fatal("the first official run was refused")
	}
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{changed}
	f.mu.Unlock()
	tick(t, l)
	if !m.RecordOfficialBenchmark(t.Context(), changed, runOfSet(set, at.Add(2*time.Minute), "yes")) {
		t.Fatal("the newer official run was refused")
	}

	if m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, at.Add(time.Minute), "yes")) {
		t.Fatal("the older official run was accepted")
	}
	if stored, ok := m.storedConfig(official.ID); !ok || stored.config != configOf(changed) {
		t.Fatalf("the rejected run changed the shared configuration: %+v, %v", stored, ok)
	}
	if got := m.MeasuredFor(changed, at.Add(2*time.Minute)); got.ProductScore != 100 {
		t.Fatalf("the rejected run removed the newer official score: %+v", got)
	}
}
