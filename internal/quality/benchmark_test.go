package quality

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// benchmarkV2Hash is the exact set the recorded scores belong to. Editing benchmark-v2.json without a new version and
// a new hash must fail here: an edited set silently makes every stored score incomparable.
const benchmarkV2Hash = "5ac6d2c34cbd4dee0faedf8cb8d3547039f037be81f1ede2626dea42d4c3175e"

func TestBenchmarkSet(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	if set.Version != "benchmark-v2" || set.Hash != benchmarkV2Hash {
		t.Fatalf("set %q hash %s, want benchmark-v2 %s", set.Version, set.Hash, benchmarkV2Hash)
	}
	if len(set.Questions) != 34 {
		t.Fatalf("%d questions, want 34", len(set.Questions))
	}
	ids := map[string]bool{}
	points := 0
	for _, q := range set.Questions {
		if ids[q.ID] {
			t.Fatalf("question %q is listed twice", q.ID)
		}
		ids[q.ID] = true
		if q.MaxOutput <= 0 || len(q.Checks) == 0 {
			t.Fatalf("question %q has no output limit or no checks", q.ID)
		}
		points += len(q.Checks)
	}
	if points != 37 {
		t.Fatalf("%d check points, want 37", points)
	}
}

//nolint:cyclop // each check compares the score, confidence and time of one read
func TestRecordBenchmark(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	m, ep, at := benchmarkMonitor(t, set)
	candidate := ep
	candidate.ID = suppliers.EndpointID{2}
	if err := m.Prepare(t.Context(), []suppliers.Routable{ep, candidate}); err != nil {
		t.Fatal(err)
	}
	if got := m.MeasuredFor(candidate, at); got.ProductScore != 0 || got.ProductScoreConfidence != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("a baseline alone scored another endpoint: %+v", got)
	}
	official := runOfSet(set, at, "yes")
	score, ok := m.RecordBenchmark(t.Context(), candidate, official)
	if !ok || score.Score != 100 || score.Confidence != 1 || score.Possible == 0 {
		t.Fatalf("the candidate scored %+v, want 100 with full confidence", score)
	}
	got := m.MeasuredFor(candidate, at)
	if got.ProductScore != 100 || got.ProductScoreConfidence != 1 || !got.ProductScoreAt.Equal(at) {
		t.Fatalf("read back %+v, want 100 with full confidence at %v", got, at)
	}
	if half := m.MeasuredFor(candidate, at.Add(benchmarkMaxAge/2)); half.ProductScore != 100 || math.Abs(half.ProductScoreConfidence-0.5) > 1e-9 {
		t.Fatalf("half the age: %+v, want 100 and confidence 0.5", half)
	}
	if expired := m.MeasuredFor(candidate, at.Add(benchmarkMaxAge)); expired.ProductScore != 0 ||
		expired.ProductScoreConfidence != 0 || !expired.ProductScoreAt.Equal(at) {
		t.Fatalf("aged out: %+v, want no score and the last run's time", expired)
	}
}

func TestOfficialBaselineOwnsItsAnswers(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	run := recordedBenchmarkRuns(t, set)["official"]
	at := t0.Add(time.Hour)
	run.At = at
	run.Format = protocol.Chat
	correct := make(map[string]string, len(run.Answers))
	for _, a := range run.Answers {
		correct[a.Question] = a.Reply
	}
	ep := suppliers.Routable{
		ID: suppliers.EndpointID{1}, Model: uuid.NewV7(), Upstream: "official",
		Formats: []protocol.Format{protocol.Chat},
	}
	m := New(nil)
	if err := m.Prepare(t.Context(), []suppliers.Routable{ep}); err != nil {
		t.Fatal(err)
	}
	if !m.RecordOfficialBenchmark(t.Context(), ep, run) {
		t.Fatal("the official baseline was refused")
	}
	for i := range 17 { // the caller reuses the slice after the call
		run.Answers[i].Reply = "no"
	}
	candidate := ep
	candidate.ID = suppliers.EndpointID{2}
	candidate.Upstream = "relay"
	if err := m.Prepare(t.Context(), []suppliers.Routable{ep, candidate}); err != nil {
		t.Fatal(err)
	}
	weaker := runOfSet(set, at, "no")
	for i := range 17 {
		weaker.Answers[i].Reply = correct[weaker.Answers[i].Question]
	}
	score, ok := m.RecordBenchmark(t.Context(), candidate, weaker)
	if !ok || math.Abs(score.Score-50) > 1e-9 {
		t.Fatalf("a candidate with 17 of 34 scored %+v, want 50 after the input was mutated", score)
	}
}

func TestRecordBenchmarkRejectsRunBeforeCurrentBaseline(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	model := uuid.NewV7()
	ep := suppliers.Routable{
		ID: suppliers.EndpointID{1}, Model: model, Upstream: "official",
		Formats: []protocol.Format{protocol.Chat},
	}
	candidate := ep
	candidate.ID = suppliers.EndpointID{2}
	candidate.Upstream = "relay"
	m := New(nil)
	if err := m.Prepare(t.Context(), []suppliers.Routable{ep, candidate}); err != nil {
		t.Fatal(err)
	}
	oldBase := t0.Add(time.Hour)
	if !m.RecordOfficialBenchmark(t.Context(), ep, runOfSet(set, oldBase, "yes")) {
		t.Fatal("the old baseline was refused")
	}
	if _, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, oldBase.Add(5*time.Minute), "yes")); !ok {
		t.Fatal("the old candidate run was refused")
	}
	newBase := oldBase.Add(time.Hour)
	if !m.RecordOfficialBenchmark(t.Context(), ep, runOfSet(set, newBase, "yes")) {
		t.Fatal("the new baseline was refused")
	}
	late := runOfSet(set, oldBase.Add(30*time.Minute), "yes")
	if _, ok := m.RecordBenchmark(t.Context(), candidate, late); ok {
		t.Fatal("a run before the new baseline was accepted")
	}
	if got := m.MeasuredFor(candidate, newBase); got.ProductScore != 0 {
		t.Fatalf("the old score survived the baseline replacement: %+v", got)
	}
}

func TestCommitBenchmarkRejectsAStaleBaseline(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	model := uuid.NewV7()
	official := suppliers.Routable{
		ID: suppliers.EndpointID{1}, Model: model, Upstream: "official",
		Formats: []protocol.Format{protocol.Chat},
	}
	candidate := official
	candidate.ID = suppliers.EndpointID{2}
	candidate.Upstream = "relay"
	m := New(nil)
	if err := m.Prepare(t.Context(), []suppliers.Routable{official, candidate}); err != nil {
		t.Fatal(err)
	}
	oldAt := t0
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, oldAt, "yes")) {
		t.Fatal("the old baseline was refused")
	}
	m.mu.RLock()
	oldBaseline := m.benchmarks[model]
	m.mu.RUnlock()
	run := runOfSet(set, oldAt.Add(10*time.Minute), "yes")
	score, ok := scoreAgainst(set, oldBaseline, run)
	if !ok {
		t.Fatal("the old baseline did not score a candidate run")
	}
	if !m.RecordOfficialBenchmark(t.Context(), official, runOfSet(set, oldAt.Add(time.Hour), "yes")) {
		t.Fatal("the new baseline was refused")
	}
	sentinel := benchmarkState{
		version: set.Version, baseline: oldAt.Add(5 * time.Minute),
		score: 42, confidence: 0.5, at: oldAt.Add(5 * time.Minute),
	}
	e := m.of(candidate.ID)
	e.mu.Lock()
	e.benchmark = sentinel
	e.mu.Unlock()
	if m.commitBenchmark(candidate, oldBaseline, benchmarkStateOf(score, oldBaseline.at, candidate)) {
		t.Fatal("a score computed against the replaced baseline was committed")
	}
	e.mu.Lock()
	got := e.benchmark
	e.mu.Unlock()
	if got != sentinel {
		t.Fatalf("the endpoint state was overwritten: %+v", got)
	}
}

func TestRecordBenchmarkBindsToConfiguration(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	m, ep, at := benchmarkMonitor(t, set)
	// One model's baseline scores another endpoint of the same model.
	other := ep
	other.ID = suppliers.EndpointID{2}
	other.Upstream = "relay"
	if err := m.Prepare(t.Context(), []suppliers.Routable{ep, other}); err != nil {
		t.Fatal(err)
	}
	weaker := runOfSet(set, at.Add(time.Hour), "no")
	score, ok := m.RecordBenchmark(t.Context(), other, weaker)
	if !ok || score.Score != 0 || score.Points != 0 {
		t.Fatalf("the weaker endpoint scored %+v, want 0", score)
	}
	mismatch := runOfSet(set, at.Add(2*time.Hour), "yes")
	mismatch.Format = protocol.Messages
	if _, ok := m.RecordBenchmark(t.Context(), other, mismatch); ok {
		t.Fatal("a run in another format was scored against the chat baseline")
	}
	older := runOfSet(set, at.Add(30*time.Minute), "yes")
	if _, ok := m.RecordBenchmark(t.Context(), other, older); ok {
		t.Fatal("an older run replaced a newer score")
	}
	changed := other
	changed.Upstream = "relay-2"
	if err := m.Prepare(t.Context(), []suppliers.Routable{ep, changed}); err != nil {
		t.Fatal(err)
	}
	if got := m.MeasuredFor(changed, at.Add(time.Hour)); got.ProductScore != 0 || got.ProductScoreConfidence != 0 {
		t.Fatalf("a changed configuration inherited the score: %+v", got)
	}
}

func TestRecordBenchmarkRejectsNonGenerationFormat(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	m, ep, _ := benchmarkMonitor(t, set)
	embedding := ep
	embedding.Formats = []protocol.Format{protocol.Embeddings}
	if err := m.Prepare(t.Context(), []suppliers.Routable{embedding}); err != nil {
		t.Fatal(err)
	}
	run := runOfSet(set, t0.Add(2*time.Hour), "yes")
	run.Format = protocol.Embeddings
	if _, ok := m.RecordBenchmark(t.Context(), embedding, run); ok {
		t.Fatal("an embeddings endpoint was scored on a text benchmark")
	}
}

// benchmarkMonitor prepares one platform endpoint and records a baseline that its run can pass.
func benchmarkMonitor(t *testing.T, set *BenchmarkSet) (*Monitor, suppliers.Routable, time.Time) {
	t.Helper()
	ep := suppliers.Routable{
		ID: suppliers.EndpointID{1}, Model: uuid.NewV7(), Upstream: "official",
		Formats: []protocol.Format{protocol.Chat, protocol.Messages},
	}
	m := New(nil)
	if err := m.Prepare(t.Context(), []suppliers.Routable{ep}); err != nil {
		t.Fatal(err)
	}
	at := t0.Add(time.Hour)
	if !m.RecordOfficialBenchmark(t.Context(), ep, runOfSet(set, at, "yes")) {
		t.Fatal("the official baseline was refused")
	}
	return m, ep, at
}

func TestProductScoreOnlyForPlatformEndpoints(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	m, ep, at := benchmarkMonitor(t, set)
	if got := m.MeasuredFor(ep, at); got.ProductScore != 100 {
		t.Fatalf("platform endpoint scored %+v, want 100", got)
	}
	tenant := ep
	tenant.Site = uuid.NewV7()
	if err := m.Prepare(t.Context(), []suppliers.Routable{tenant}); err != nil {
		t.Fatal(err)
	}
	if got := m.MeasuredFor(tenant, at); got.ProductScore != 0 || got.ProductScoreConfidence != 0 || !got.ProductScoreAt.IsZero() {
		t.Fatalf("tenant endpoint kept a product score: %+v", got)
	}
}

func TestRecordBenchmarkNeedsTheCurrentBaseline(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	model := uuid.NewV7()
	ep := suppliers.Routable{ID: suppliers.EndpointID{1}, Model: model, Upstream: "official", Formats: []protocol.Format{protocol.Chat}}
	m := New(nil)
	if err := m.Prepare(t.Context(), []suppliers.Routable{ep}); err != nil {
		t.Fatal(err)
	}
	at := t0.Add(time.Hour)
	if !m.RecordOfficialBenchmark(t.Context(), ep, runOfSet(set, at, "yes")) {
		t.Fatal("the first baseline was refused")
	}
	candidate := ep
	candidate.ID = suppliers.EndpointID{2}
	if err := m.Prepare(t.Context(), []suppliers.Routable{ep, candidate}); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, at, "yes")); !ok {
		t.Fatal("the endpoint was not scored against the first baseline")
	}
	newer := runOfSet(set, at.Add(time.Hour), "yes")
	newer.Answers[0].Reply = "no" // the official run itself moved: scores against the old anchor are incomparable
	if !m.RecordOfficialBenchmark(t.Context(), ep, newer) {
		t.Fatal("a newer baseline was refused")
	}
	if got := m.MeasuredFor(candidate, at.Add(time.Hour)); got.ProductScore != 0 || got.ProductScoreConfidence != 0 {
		t.Fatalf("a score against the old baseline survived its replacement: %+v", got)
	}
	if m.RecordOfficialBenchmark(t.Context(), ep, runOfSet(set, at.Add(-time.Hour), "yes")) {
		t.Fatal("an older baseline replaced the current one")
	}
	if _, ok := m.RecordBenchmark(t.Context(), candidate, runOfSet(set, newer.At.Add(benchmarkMaxAge), "yes")); ok {
		t.Fatal("a run against an aged-out anchor was scored")
	}
}

func TestRecordOfficialBenchmarkRejectsIncompleteRun(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	ep := suppliers.Routable{
		ID: suppliers.EndpointID{1}, Model: uuid.NewV7(), Upstream: "official",
		Formats: []protocol.Format{protocol.Chat},
	}
	m := New(nil)
	if err := m.Prepare(t.Context(), []suppliers.Routable{ep}); err != nil {
		t.Fatal(err)
	}
	partial := runOfSet(set, t0, "yes")
	partial.Answers = partial.Answers[:len(partial.Answers)/2]
	if m.RecordOfficialBenchmark(t.Context(), ep, partial) {
		t.Fatal("a partial official run became the anchor")
	}
	if m.RecordOfficialBenchmark(t.Context(), ep, runOfSet(set, t0, "no")) {
		t.Fatal("an official run that passed nothing became the anchor")
	}
}

func TestRecordOfficialBenchmarkRejectsTenantEndpoints(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	ep := suppliers.Routable{ID: suppliers.EndpointID{1}, Model: uuid.NewV7(), Upstream: "own", Formats: []protocol.Format{protocol.Chat}}
	ep.Site = uuid.NewV7()
	m := New(nil)
	if m.RecordOfficialBenchmark(t.Context(), ep, runOfSet(set, t0, "yes")) {
		t.Fatal("a tenant endpoint anchored the official score")
	}
}

func TestBenchmarkKeepsRunningMetricsSeparate(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	ep := suppliers.Routable{ID: suppliers.EndpointID{1}, Model: uuid.NewV7(), Upstream: "official", Formats: []protocol.Format{protocol.Chat}}
	m := New(nil)
	if err := m.Prepare(t.Context(), []suppliers.Routable{ep}); err != nil {
		t.Fatal(err)
	}
	at := t0.Add(time.Hour)
	if !m.RecordOfficialBenchmark(t.Context(), ep, runOfSet(set, at, "yes")) {
		t.Fatal("baseline refused")
	}
	for range scoreSamples {
		m.Observe(t.Context(), Observation{Endpoint: ep.ID, Failure: failed500, Ended: at})
	}
	got := m.MeasuredFor(ep, at)
	if got.Health >= 1 || got.Quality >= 1 {
		t.Fatalf("a failing endpoint kept its running metrics: %+v", got)
	}
	if got.ProductScore != 100 || got.ProductScoreConfidence != 1 {
		t.Fatalf("running failures changed the answer score: %+v", got)
	}
}

func TestBenchmarkConcurrently(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	model := uuid.NewV7()
	eps := make([]suppliers.Routable, 4)
	for i := range eps {
		eps[i] = suppliers.Routable{
			ID: suppliers.EndpointID{byte(i + 1)}, Model: model, Upstream: fmt.Sprintf("up-%d", i),
			Formats: []protocol.Format{protocol.Chat},
		}
	}
	m := New(nil)
	if err := m.Prepare(t.Context(), eps); err != nil {
		t.Fatal(err)
	}
	at := t0.Add(time.Hour)
	if !m.RecordOfficialBenchmark(t.Context(), eps[0], runOfSet(set, at, "yes")) {
		t.Fatal("baseline refused")
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for n := range 50 {
				ep := eps[i%len(eps)]
				_, _ = m.RecordBenchmark(t.Context(), ep, runOfSet(set, at.Add(time.Duration(n)*time.Second), "yes"))
				_ = m.MeasuredFor(ep, at)
				m.Observe(t.Context(), Observation{Endpoint: ep.ID, Done: true, Sent: at, FirstToken: at.Add(time.Millisecond), Ended: at.Add(time.Second)})
			}
		})
	}
	wg.Wait()
	if got := m.MeasuredFor(eps[0], at); got.ProductScore != 100 {
		t.Fatalf("the official endpoint lost its score under concurrency: %+v", got)
	}
}

// runOfSet answers every question with reply, which passes only the checks that accept it.
func runOfSet(set *BenchmarkSet, at time.Time, reply string) BenchmarkRun {
	run := BenchmarkRun{Version: set.Version, Format: protocol.Chat, At: at}
	for _, q := range set.Questions {
		run.Answers = append(run.Answers, BenchmarkAnswer{Question: q.ID, Reply: reply, Complete: true})
	}
	return run
}
