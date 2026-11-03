package quality

import (
	"context"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// benchmarkEndpoints is one model's official direct endpoint and a candidate of the same model, in the only format the
// fixture gives them.
func benchmarkEndpoints() (official, candidate suppliers.Routable) {
	model := uuid.NewV7()
	official = suppliers.Routable{
		ID: uuid.NewV7(), Model: model, Upstream: "official",
		Formats: []protocol.Format{protocol.Chat},
	}
	candidate = official
	candidate.ID = uuid.NewV7()
	candidate.Upstream = "relay"
	return official, candidate
}

// TestBenchmarkPersistsAcrossRestartAndNodes: a recorded score is in the store before it is published, a node that
// started before the run adopts it on its next refresh without a restart, and a restarted node loads it at start.
//
//nolint:cyclop // one test walks the record, the second node and the restart in order
func TestBenchmarkPersistsAcrossRestartAndNodes(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Observe)
	official, candidate := benchmarkEndpoints()
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)

	// A second node is already running and has published the same endpoints before the run.
	otherNow := *now
	other, otherLoop, err := Start(t.Context(), Config{
		DB: l.db, Scope: l.scope, Mode: Observe, Source: f, Pauses: f,
		Now: func() time.Time { return otherNow }, Rand: func() float64 { return .5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, otherLoop)
	if got := other.MeasuredFor(candidate, otherNow); got.ProductScore != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("a node scored before any run: %+v", got)
	}

	set := fixedBenchmark()
	at := *now
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, at, "yes")) {
		t.Fatal("the official baseline was refused")
	}
	want, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, at.Add(time.Minute), "yes"))
	if !ok || want.Score == 0 {
		t.Fatalf("the candidate run scored %+v, %v", want, ok)
	}
	if got := m.MeasuredFor(candidate, want.At); got.ProductScore != want.Score ||
		got.ProductScoreConfidence != want.Confidence || !got.ProductScoreAt.Equal(want.At) {
		t.Fatalf("the recording node read %+v, want %v at %v", got, want.Score, want.At)
	}

	// The other node adopts the stored values on its next refresh, without a restart.
	otherNow = otherNow.Add(benchmarkEvery)
	tick(t, otherLoop)
	if got := other.MeasuredFor(candidate, want.At); got.ProductScore != want.Score ||
		got.ProductScoreConfidence != want.Confidence || !got.ProductScoreAt.Equal(want.At) {
		t.Fatalf("the second node read %+v after the refresh, want %v at %v", got, want.Score, want.At)
	}

	// A restart loads the same values before it serves.
	restarted, restartedLoop, err := Start(t.Context(), Config{
		DB: l.db, Scope: l.scope, Mode: Observe, Source: f, Pauses: f,
		Now: func() time.Time { return otherNow }, Rand: func() float64 { return .5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, restartedLoop)
	if got := restarted.MeasuredFor(candidate, want.At); got.ProductScore != want.Score ||
		got.ProductScoreConfidence != want.Confidence || !got.ProductScoreAt.Equal(want.At) {
		t.Fatalf("the restarted node read %+v, want %v at %v", got, want.Score, want.At)
	}
	if got := restarted.MeasuredFor(official, at); got.ProductScore != 100 || got.ProductScoreConfidence != 1 {
		t.Fatalf("the official endpoint read %+v, want its anchor score 100", got)
	}
}

// TestOfficialBenchmarkStoreFailureLeavesNoAnchor: the official anchor and its score are one transaction. A canceled
// write leaves neither in memory, and a later valid run is accepted.
func TestOfficialBenchmarkStoreFailureLeavesNoAnchor(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Observe)
	official, candidate := benchmarkEndpoints()
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)

	set := fixedBenchmark()
	at := *now
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if m.RecordOfficialBenchmark(canceled, official, runOfSet(set, at, "yes")) {
		t.Fatal("a baseline was published although its write was canceled")
	}
	if got := m.MeasuredFor(candidate, at); got.ProductScore != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("a failed baseline write left a score: %+v", got)
	}
	if _, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, at.Add(time.Minute), "yes")); ok {
		t.Fatal("a candidate run scored without a stored baseline")
	}
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, at, "yes")) {
		t.Fatal("the baseline did not succeed after the write could proceed")
	}
	if _, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, at.Add(time.Minute), "yes")); !ok {
		t.Fatal("the candidate run did not succeed after the baseline was stored")
	}
}

// TestBenchmarkStoreFailureLeavesMemoryUnchanged: a score is published only after its row is committed. A canceled
// write returns false and the endpoint keeps no score; the same run succeeds once the write can proceed.
func TestBenchmarkStoreFailureLeavesMemoryUnchanged(t *testing.T) {
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
	run := runOfSet(set, at.Add(time.Minute), "yes")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, ok := m.RecordBenchmark(canceled, candidate, run); ok {
		t.Fatal("a score was published although its write was canceled")
	}
	if got := m.MeasuredFor(candidate, run.At); got.ProductScore != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("a failed write left a score: %+v", got)
	}
	if _, ok := m.RecordBenchmark(t.Context(), candidate, run); !ok {
		t.Fatal("the run did not succeed after the write could proceed")
	}
}

// TestNewerBenchmarkVersionSupersedesTheOlder: rows of another benchmark version are not read, even when their run is
// later. The older row is written through the same store functions, with a run after the current set's, so a reader that
// chose by time alone would prefer it.
func TestNewerBenchmarkVersionSupersedesTheOlder(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Observe)
	official, candidate := benchmarkEndpoints()
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)

	oldAt := now.Add(2 * time.Hour)
	oldHash := strings.Repeat("0", 64)
	oldAnchor := benchmarkAnchor{
		version: "benchmark-v1", hash: oldHash, format: protocol.Chat, endpoint: official.ID,
		at: oldAt, scores: map[string]float64{"arith-sum": 1},
	}
	oldScore := benchmarkState{
		version: "benchmark-v1", hash: oldHash, baseline: oldAt,
		score: 42, confidence: 0.4, at: oldAt, config: configOf(candidate),
	}
	if err := l.db.Tx(t.Context(), l.scope, func(tx pg.Tx) error {
		if _, err := writeBenchmarkBaseline(t.Context(), tx, official.Model, oldAnchor); err != nil {
			return err
		}
		_, err := writeBenchmarkScore(t.Context(), tx, candidate, oldScore)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// A node that reads only the current set ignores the older row.
	node, loop, err := Start(t.Context(), Config{
		DB: l.db, Scope: l.scope, Mode: Observe, Source: f, Pauses: f,
		Now: func() time.Time { return *now }, Rand: func() float64 { return .5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, loop)
	if got := node.MeasuredFor(candidate, oldAt); got.ProductScore != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("an older benchmark version scored the endpoint: %+v", got)
	}

	// The current set records its own baseline and score next to the older rows.
	set := fixedBenchmark()
	at := *now
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, at, "yes")) {
		t.Fatal("the current baseline was refused")
	}
	want, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, at.Add(time.Minute), "yes"))
	if !ok {
		t.Fatal("the current candidate run was refused")
	}
	restarted, restartedLoop, err := Start(t.Context(), Config{
		DB: l.db, Scope: l.scope, Mode: Observe, Source: f, Pauses: f,
		Now: func() time.Time { return *now }, Rand: func() float64 { return .5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, restartedLoop)
	if got := restarted.MeasuredFor(candidate, want.At); got.ProductScore != want.Score {
		t.Fatalf("the current version read %+v, want %v", got, want.Score)
	}
}

// TestNewerBaselineReplacesScoresAgainstTheOlder: a run against a later baseline replaces an older run even when the
// older run's clock time is later, because the older score is incomparable once its anchor is replaced. The official
// endpoint's own 100 must not be blocked by a candidate run recorded after it against the previous baseline.
func TestNewerBaselineReplacesScoresAgainstTheOlder(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Observe)
	official, candidate := benchmarkEndpoints()
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)

	set := fixedBenchmark()
	first := *now
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, first, "yes")) {
		t.Fatal("the first baseline was refused")
	}
	if _, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, first.Add(2*time.Hour), "no")); !ok {
		t.Fatal("the candidate run against the first baseline was refused")
	}
	second := first.Add(time.Hour)
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, second, "yes")) {
		t.Fatal("the second baseline was refused")
	}
	if got := m.MeasuredFor(official, second); got.ProductScore != 100 || got.ProductScoreConfidence != 1 {
		t.Fatalf("the official endpoint against the new baseline read %+v, want 100", got)
	}
	if got := m.MeasuredFor(candidate, second.Add(time.Minute)); got.ProductScore != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("the old candidate score read %+v, want withheld", got)
	}

	restarted, loop, err := Start(t.Context(), Config{
		DB: l.db, Scope: l.scope, Mode: Observe, Source: f, Pauses: f,
		Now: func() time.Time { return second }, Rand: func() float64 { return .5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, loop)
	if got := restarted.MeasuredFor(official, second); got.ProductScore != 100 || got.ProductScoreConfidence != 1 {
		t.Fatalf("the restarted official endpoint read %+v, want 100", got)
	}
	if got := restarted.MeasuredFor(candidate, second.Add(time.Minute)); got.ProductScore != 0 {
		t.Fatalf("the restarted node kept a score against the replaced baseline: %+v", got)
	}
}

// TestBenchmarkConfigurationChangeDoesNotResurrect: a score recorded for one configuration is voided when the endpoint
// publishes another, and a later refresh must not bring it back if the old configuration returns. A genuinely newer run
// for that configuration is still adopted.
func TestBenchmarkConfigurationChangeDoesNotResurrect(t *testing.T) {
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

	// The endpoint moves to another upstream: Prepare voids the score and remembers it as seen.
	changed := candidate
	changed.Upstream = "relay-2"
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, changed}
	f.mu.Unlock()
	tick(t, l)
	if got := m.MeasuredFor(changed, at.Add(2*time.Minute)); got.ProductScore != 0 {
		t.Fatalf("the new configuration read %+v, want no score", got)
	}

	// A refresh now sees the stored score for the old configuration; it must not adopt it for the new one.
	*now = now.Add(benchmarkEvery)
	tick(t, l)

	// The old configuration returns: the refresh must not resurrect the voided score.
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, candidate}
	f.mu.Unlock()
	tick(t, l)
	if got := m.MeasuredFor(candidate, at.Add(2*time.Minute)); got.ProductScore != 0 {
		t.Fatalf("a voided score came back with the old configuration: %+v", got)
	}

	// A new run after the change is fresh evidence and is accepted.
	fresh, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, at.Add(2*time.Minute), "yes"))
	if !ok {
		t.Fatal("a newer run after the configuration returned was refused")
	}
	if got := m.MeasuredFor(candidate, fresh.At); got.ProductScore != fresh.Score || !got.ProductScoreAt.Equal(fresh.At) {
		t.Fatalf("the fresh run read %+v, want %v at %v", got, fresh.Score, fresh.At)
	}
}

// TestStoredBenchmarkBindsToTheEndpointConfiguration: a stored score names the model, upstream and format set it was run
// on; a node whose endpoint publishes another configuration must not inherit it.
func TestStoredBenchmarkBindsToTheEndpointConfiguration(t *testing.T) {
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
	want, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, at.Add(time.Minute), "yes"))
	if !ok {
		t.Fatal("the candidate run was refused")
	}

	changed := candidate
	changed.Upstream = "relay-2"
	f.mu.Lock()
	f.endpoints = []suppliers.Routable{official, changed}
	f.mu.Unlock()
	node, loop, err := Start(t.Context(), Config{
		DB: l.db, Scope: l.scope, Mode: Observe, Source: f, Pauses: f,
		Now: func() time.Time { return *now }, Rand: func() float64 { return .5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, loop)
	if got := node.MeasuredFor(changed, want.At); got.ProductScore != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("a stored score crossed to another configuration: %+v", got)
	}
}
