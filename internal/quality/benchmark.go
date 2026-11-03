package quality

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// The product score is the fixed benchmark set's result against the official direct endpoint, 100 there by definition
// (docs/domains/quality.md「产品成绩（固定测评题）」and docs/product.md「端点属性」). It is separate from the operational
// Quality in this package: that one measures how fast and reliable an endpoint is on real traffic, this one measures
// what it answers. A fixed set cannot be edited in place: every byte changes the set's hash and retires every score and
// official baseline recorded against the old one.
const (
	benchmarkMaxAge   = 120 * 24 * time.Hour // a score stops counting this long after its run
	benchmarkMinPairs = 20                   // paired questions needed for a standard error worth reporting
	benchmarkCoverage = 0.9                  // each side must answer this share of the set, or there is no score
	benchmarkCI       = 1.96                 // the 95% normal interval's multiplier
)

// The check kinds. Each is a fixed operation, never a regular expression or a script: the reply is untrusted, and a
// benchmark judge must not become an interpreter for it.
const (
	checkEquals     = "equals"      // the trimmed reply equals Value
	checkContains   = "contains"    // the reply contains Value, ignoring case
	checkOneOf      = "one_of"      // the trimmed reply equals one of Values, ignoring case
	checkJSONFields = "json_fields" // the reply is one JSON object holding Fields' values
	checkJSONEquals = "json_equals" // the reply is the JSON value Value
)

//go:embed benchmark-v1.json
var benchmarkBytes []byte

// BenchmarkQuestion is one fixed question and the checks its answer must pass. A question contributes the fraction of
// its checks that pass, so a multi-part instruction is not reduced to one bit.
type BenchmarkQuestion struct {
	ID        string           `json:"id"`
	Category  string           `json:"category"`
	Prompt    string           `json:"prompt"`
	MaxOutput int64            `json:"max_output"`
	Checks    []BenchmarkCheck `json:"checks"`
}

// BenchmarkCheck is one fixed judgement: equality, containment, membership, or a parsed JSON value.
type BenchmarkCheck struct {
	Kind   string            `json:"kind"`
	Value  string            `json:"value,omitempty"`
	Values []string          `json:"values,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
}

// BenchmarkSet is the versioned question set compiled into this binary. Hash identifies the exact bytes; a result
// recorded against another hash is not a result on this set.
type BenchmarkSet struct {
	Version   string
	Hash      string
	Questions []BenchmarkQuestion
}

// BenchmarkAnswer is one question's outcome in a run. Complete is set only when the endpoint answered with a 2xx stream
// read to its end; a request that failed before answering is evidence about availability, which Health measures, not
// about the answer, so it neither scores nor counts as an answered question.
type BenchmarkAnswer struct {
	Question string
	Reply    string
	Complete bool
}

// BenchmarkRun is one endpoint's run of the set: every question once, on one configuration, in one call format, at one
// time. Version is the set's version; a run of another set cannot be scored here. The official run and a candidate must
// use the same format: chat, messages and responses frame the same question differently, so mixing them would compare
// the APIs rather than the models.
type BenchmarkRun struct {
	Version string
	Format  protocol.Format
	At      time.Time
	Answers []BenchmarkAnswer
}

// BenchmarkResult is a scored run: Score is 0–100 with the official direct run at 100 and never above it, and
// Confidence is how much the score can be trusted: the 95% paired standard error, the share of questions the endpoint
// answered, and, later, the score's age. Points and Possible are question-equivalents: every question weighs one,
// and a question with several checks contributes the fraction of them the answer passed; Points counts a question the
// candidate did not answer as zero, Possible counts only the questions the official run answered. Answered counts the
// questions both runs answered, Questions the whole set.
type BenchmarkResult struct {
	Version    string
	Hash       string
	Format     protocol.Format
	Score      float64
	Confidence float64
	At         time.Time
	BaselineAt time.Time
	Points     float64
	Possible   float64
	Answered   int
	Questions  int
}

// fixedBenchmark is the question set compiled into the binary. The embedded bytes are part of the build, so a malformed
// set is a programming error that must stop the node at New rather than silently score nothing.
var fixedBenchmark = sync.OnceValue(func() *BenchmarkSet {
	set, err := parseBenchmarkSet(benchmarkBytes)
	if err != nil {
		panic("quality: the embedded benchmark set is invalid: " + err.Error())
	}
	return set
})

// parseBenchmarkSet validates a question set and identifies it by the SHA-256 of its exact bytes. Validation is at the
// boundary (embedded data here, an administrator's later): a duplicate id or an unknown check would make a score
// incomparable rather than merely wrong.
func parseBenchmarkSet(raw []byte) (*BenchmarkSet, error) {
	var file struct {
		Version   string              `json:"version"`
		Note      string              `json:"note"`
		Questions []BenchmarkQuestion `json:"questions"`
	}
	if err := jsonx.UnmarshalKnown(raw, &file); err != nil {
		return nil, err
	}
	if file.Version == "" {
		return nil, fmt.Errorf("no version")
	}
	if file.Note == "" {
		return nil, fmt.Errorf("no note")
	}
	if len(file.Questions) < benchmarkMinPairs {
		return nil, fmt.Errorf("%d questions, want at least %d", len(file.Questions), benchmarkMinPairs)
	}
	sum := sha256.Sum256(raw)
	set := &BenchmarkSet{Version: file.Version, Hash: hex.EncodeToString(sum[:]), Questions: file.Questions}
	ids := make(map[string]struct{}, len(set.Questions))
	for i, q := range set.Questions {
		if err := q.check(); err != nil {
			return nil, fmt.Errorf("question %d: %w", i, err)
		}
		if _, ok := ids[q.ID]; ok {
			return nil, fmt.Errorf("question %q is listed twice", q.ID)
		}
		ids[q.ID] = struct{}{}
	}
	return set, nil
}

// check validates one question before it can be scored.
func (q BenchmarkQuestion) check() error {
	if q.ID == "" || q.Category == "" || q.Prompt == "" {
		return fmt.Errorf("id, category and prompt are required")
	}
	if q.MaxOutput < 1 || q.MaxOutput > 4096 {
		return fmt.Errorf("question %q: max_output must be from 1 through 4096", q.ID)
	}
	if len(q.Checks) == 0 || len(q.Checks) > 8 {
		return fmt.Errorf("question %q must have from 1 through 8 checks", q.ID)
	}
	for j, c := range q.Checks {
		if err := c.check(); err != nil {
			return fmt.Errorf("question %q check %d: %w", q.ID, j, err)
		}
	}
	return nil
}

// check validates one fixed judgement before it can judge anything.
func (c BenchmarkCheck) check() error {
	switch c.Kind {
	case checkEquals, checkContains:
		if c.Value == "" {
			return fmt.Errorf("%s needs a value", c.Kind)
		}
	case checkOneOf:
		return validateOneOf(c.Values)
	case checkJSONFields:
		return validateJSONFields(c.Fields)
	case checkJSONEquals:
		if c.Value == "" {
			return fmt.Errorf("json_equals needs a value")
		}
		if _, ok := jsonAnswer(c.Value); !ok {
			return fmt.Errorf("json_equals value is not JSON")
		}
	default:
		return fmt.Errorf("unknown kind %q", c.Kind)
	}
	return nil
}

func validateOneOf(values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("one_of needs values")
	}
	if slices.Contains(values, "") {
		return fmt.Errorf("one_of has an empty value")
	}
	return nil
}

func validateJSONFields(fields map[string]string) error {
	if len(fields) == 0 {
		return fmt.Errorf("json_fields needs fields")
	}
	for field, value := range fields {
		if field == "" || value == "" {
			return fmt.Errorf("json_fields has an empty field or value")
		}
	}
	return nil
}

// matches applies one fixed judgement to an answer. Parsing an answer as JSON tolerates a Markdown fence, because a
// fenced object is still the object the question asked for; every other kind judges the reply as written.
func (c BenchmarkCheck) matches(reply string) bool {
	switch c.Kind {
	case checkEquals:
		return strings.TrimSpace(reply) == c.Value
	case checkContains:
		return strings.Contains(strings.ToLower(reply), strings.ToLower(c.Value))
	case checkOneOf:
		answer := strings.ToLower(strings.TrimSpace(reply))
		for _, v := range c.Values {
			if answer == strings.ToLower(v) {
				return true
			}
		}
	case checkJSONFields:
		return c.matchesFields(reply)
	case checkJSONEquals:
		return c.matchesJSON(reply)
	}
	return false
}

// matchesFields checks the named members of one JSON object.
func (c BenchmarkCheck) matchesFields(reply string) bool {
	value, ok := jsonAnswer(reply)
	if !ok {
		return false
	}
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for field, want := range c.Fields {
		got, ok := object[field]
		if !ok || want == "" || jsonScalar(got) != want {
			return false
		}
	}
	return true
}

// matchesJSON checks one parsed JSON value.
func (c BenchmarkCheck) matchesJSON(reply string) bool {
	got, ok := jsonAnswer(reply)
	if !ok {
		return false
	}
	want, ok := jsonAnswer(c.Value)
	return ok && equalJSON(got, want)
}

// jsonAnswer parses one JSON value, stripping one surrounding Markdown fence if the answer has it.
func jsonAnswer(reply string) (any, bool) {
	text := strings.TrimSpace(reply)
	if strings.HasPrefix(text, "```") {
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			text = text[newline+1:]
		}
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "```"))
	}
	var value any
	if err := jsonx.Unmarshal([]byte(text), &value); err != nil {
		return nil, false
	}
	return value, true
}

// jsonScalar is the string a JSON field's expected value is compared with, for the scalar types a structured-output
// question uses.
func jsonScalar(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

// equalJSON compares two parsed JSON values. Numbers decode as float64 on both sides, so equality is numeric.
//
//nolint:cyclop,gocognit // one branch per JSON value type; the types are a closed set
func equalJSON(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			other, ok := bv[k]
			if !ok || !equalJSON(v, other) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !equalJSON(av[i], bv[i]) {
				return false
			}
		}
		return true
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case nil:
		return b == nil
	}
	return false
}

// judgeBenchmarkAnswer is the fraction of a question's checks the answer passes.
func judgeBenchmarkAnswer(q BenchmarkQuestion, reply string) float64 {
	passed := 0
	for _, c := range q.Checks {
		if c.matches(reply) {
			passed++
		}
	}
	return float64(passed) / float64(len(q.Checks))
}

// scoreBenchmark scores a candidate run against the official run of the same set. The official run is the anchor: its
// points on the questions it answered are 100, and a candidate that passes more is capped at 100 because a difference of
// a few questions is noise, not a claim that it is better than official. A candidate that did not answer a question the
// official answered scores zero there; a run where the official answered fewer than benchmarkCoverage of the questions,
// or where fewer than benchmarkMinPairs pairs or benchmarkCoverage of the official answers remain, has no score (fail
// closed). The two runs must use the same call format.
func scoreBenchmark(set *BenchmarkSet, official, candidate BenchmarkRun) (BenchmarkResult, bool) {
	if set == nil || official.Version != set.Version || candidate.Version != set.Version || official.Format != candidate.Format {
		return BenchmarkResult{}, false
	}
	officialAnswers, ok := answersByQuestion(set, official.Answers)
	if !ok {
		return BenchmarkResult{}, false
	}
	candidateAnswers, ok := answersByQuestion(set, candidate.Answers)
	if !ok {
		return BenchmarkResult{}, false
	}
	at := candidate.At
	if at.IsZero() {
		at = official.At
	}
	if at.IsZero() {
		return BenchmarkResult{}, false
	}
	pairs := pairedScores(set, officialAnswers, candidateAnswers)
	if !scoreCoverage(len(set.Questions), pairs.officialAnswered, pairs.paired) || pairs.officialPoints == 0 {
		return BenchmarkResult{}, false
	}
	confidence, ok := benchmarkConfidence(pairs.officialScores, pairs.candidateScores)
	if !ok {
		return BenchmarkResult{}, false
	}
	return BenchmarkResult{
		Version: set.Version, Hash: set.Hash, Format: candidate.Format, At: at, BaselineAt: official.At,
		Score:  min(100, 100*pairs.candidatePoints/pairs.officialPoints),
		Points: pairs.candidatePoints, Possible: pairs.officialPoints,
		Answered: pairs.paired, Questions: len(set.Questions),
		// Coverage is part of confidence: a run that answered fewer questions has less evidence, even when the
		// questions it did answer agree with the official run.
		Confidence: confidence * float64(pairs.paired) / float64(pairs.officialAnswered),
	}, true
}

// benchmarkPairs is the paired evidence of one comparison: the question-equivalents on each side, and the per-question
// fractions the interval needs.
type benchmarkPairs struct {
	paired, officialAnswered        int
	officialPoints, candidatePoints float64
	officialScores, candidateScores []float64
}

// pairedScores sums the question-equivalents and collects the paired fractions used by the interval.
func pairedScores(set *BenchmarkSet, official, candidate map[string]BenchmarkAnswer) benchmarkPairs {
	pairs := benchmarkPairs{
		officialScores:  make([]float64, 0, len(set.Questions)),
		candidateScores: make([]float64, 0, len(set.Questions)),
	}
	for _, q := range set.Questions {
		o, ok := official[q.ID]
		if !ok || !o.Complete {
			continue
		}
		pairs.officialAnswered++
		officialScore := judgeBenchmarkAnswer(q, o.Reply)
		pairs.officialPoints += officialScore
		c, ok := candidate[q.ID]
		if !ok || !c.Complete {
			continue
		}
		pairs.paired++
		candidateScore := judgeBenchmarkAnswer(q, c.Reply)
		pairs.candidatePoints += candidateScore
		pairs.officialScores = append(pairs.officialScores, officialScore)
		pairs.candidateScores = append(pairs.candidateScores, candidateScore)
	}
	return pairs
}

// scoreCoverage is the floor for a comparable score: enough official answers, enough pairs, and enough of the official
// answers paired with the candidate.
func scoreCoverage(total, officialAnswered, paired int) bool {
	if officialAnswered < int(math.Ceil(benchmarkCoverage*float64(total))) {
		return false
	}
	return paired >= benchmarkMinPairs &&
		paired >= int(math.Ceil(benchmarkCoverage*float64(officialAnswered)))
}

// benchmarkConfidence turns the paired fractions into the score's statistical confidence. The paired differences carry
// the sampling noise: the standard error of the ratio of the two runs' means, by the delta method. It covers the
// candidate's wrong answers and the official run's own misses.
func benchmarkConfidence(officialScores, candidateScores []float64) (float64, bool) {
	paired := len(officialScores)
	meanOfficial := sum(officialScores) / float64(paired)
	if meanOfficial <= 0 {
		return 0, false
	}
	meanCandidate := sum(candidateScores) / float64(paired)
	ratio := meanCandidate / meanOfficial
	variance := (varianceOf(candidateScores, meanCandidate) +
		ratio*ratio*varianceOf(officialScores, meanOfficial) -
		2*ratio*covarianceOf(officialScores, candidateScores, meanOfficial, meanCandidate)) /
		(float64(paired) * meanOfficial * meanOfficial)
	variance = max(0, variance) // a floating-point residue, not a negative variance
	halfWidth := benchmarkCI * 100 * math.Sqrt(variance)
	return max(0, min(1, 1-halfWidth/100)), true
}

// answersByQuestion indexes a run and rejects a question outside this set or answered twice: an unknown id makes the
// run belong to another set, and two answers for one question make it ambiguous rather than choosing one.
func answersByQuestion(set *BenchmarkSet, answers []BenchmarkAnswer) (map[string]BenchmarkAnswer, bool) {
	known := make(map[string]struct{}, len(set.Questions))
	for _, q := range set.Questions {
		known[q.ID] = struct{}{}
	}
	out := make(map[string]BenchmarkAnswer, len(answers))
	for _, a := range answers {
		if _, ok := known[a.Question]; !ok {
			return nil, false
		}
		if _, ok := out[a.Question]; ok {
			return nil, false
		}
		out[a.Question] = a
	}
	return out, true
}

func sum(values []float64) float64 {
	var total float64
	for _, v := range values {
		total += v
	}
	return total
}

func varianceOf(values []float64, mean float64) float64 {
	if len(values) < 2 {
		return 0
	}
	var total float64
	for _, v := range values {
		total += (v - mean) * (v - mean)
	}
	return total / float64(len(values)-1)
}

func covarianceOf(a, b []float64, meanA, meanB float64) float64 {
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	var total float64
	for i := range a {
		total += (a[i] - meanA) * (b[i] - meanB)
	}
	return total / float64(len(a)-1)
}

// benchmarkRunAllowed is the boundary every recorded run crosses: a platform endpoint, this set, a time, and a
// generation format the endpoint actually publishes.
func benchmarkRunAllowed(ep suppliers.Routable, run BenchmarkRun, set *BenchmarkSet) bool {
	return ep.Platform() && run.Version == set.Version && !run.At.IsZero() &&
		benchmarkFormat(run.Format) && slices.Contains(ep.Formats, run.Format)
}

// benchmarkFormat reports whether a format can answer the benchmark's text questions. Embeddings, rerank and legacy
// completions cannot follow the chat-style instructions, so an endpoint with only those formats has no product score.
func benchmarkFormat(f protocol.Format) bool {
	return f == protocol.Chat || f == protocol.Messages || f == protocol.Responses
}

// officialBenchmark is the official direct endpoint's run for one catalog model, the anchor every endpoint of that
// model is scored against. It is kept in memory; persistence and cross-node adoption follow the same path as
// verification (docs/domains/quality.md「产品成绩（固定测评题）」).
type officialBenchmark struct {
	run BenchmarkRun
}

// benchmarkState is one endpoint's score and the official baseline it was computed against. A later baseline makes the
// score incomparable rather than merely old, so it is withheld until the endpoint is run again.
type benchmarkState struct {
	version    string
	baseline   time.Time
	score      float64
	confidence float64
	at         time.Time
}

// measures is the score routing and the public list read: zero and no confidence without a run on the current
// configuration, against the current baseline, in this question set, and younger than benchmarkMaxAge. The confidence
// decays linearly to zero at the age limit; the score and its time remain for "last measured" until it is replaced.
func (b benchmarkState) measures(now time.Time, version string, baseline time.Time, current bool) (score, confidence float64, at time.Time) {
	if !current || b.at.IsZero() || b.version != version || !b.baseline.Equal(baseline) {
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
func (b benchmarkState) productScore(now time.Time, baseline officialBenchmark, hasBaseline, current bool) (score, confidence float64, at time.Time) {
	if !hasBaseline {
		return 0, 0, time.Time{}
	}
	score, confidence, at = b.measures(now, fixedBenchmark().Version, baseline.run.At, current)
	if now.Sub(baseline.run.At) >= benchmarkMaxAge {
		return 0, 0, at
	}
	return score, confidence, at
}

// RecordOfficialBenchmark records the official direct endpoint's run for a catalog model: the anchor its other endpoints
// are scored against. Only a platform endpoint can be the official run; a tenant's own key measures that tenant's
// endpoint, not the official service. The caller must pass the catalog author's own API, not a cloud host or a relay:
// this package can see only the platform flag. A baseline older than the one in force is rejected, so a late result
// cannot replace a newer anchor. It also records the endpoint's own 100 through RecordBenchmark when its snapshot is
// current.
func (m *Monitor) RecordOfficialBenchmark(ep suppliers.Routable, run BenchmarkRun) bool {
	set := fixedBenchmark()
	if ep.Model == (uuid.UUID{}) || !benchmarkRunAllowed(ep, run, set) {
		return false
	}
	// A baseline must itself meet the coverage and points floor a candidate is held to; a partial official run would
	// silently lower the anchor every other endpoint is measured against.
	if _, ok := scoreBenchmark(set, run, run); !ok {
		return false
	}
	m.mu.Lock()
	if old, ok := m.benchmarks[ep.Model]; ok && !run.At.After(old.run.At) {
		m.mu.Unlock()
		return false
	}
	m.benchmarks[ep.Model] = officialBenchmark{run: run}
	m.mu.Unlock()
	// The official endpoint is the anchor, so its own score is 100 by definition. Record it through the same path a
	// candidate uses; if its snapshot is not current yet, its score waits for the next published run.
	_, _ = m.RecordBenchmark(ep, run)
	return true
}

// RecordBenchmark scores an endpoint's run against its model's official baseline and records it. It returns false for
// a tenant endpoint (the platform does not spend a tenant's key on a benchmark), a run of another question set, without
// a baseline, against an anchor that has already aged out, from another configuration than the one the snapshot
// published, older than the score already recorded, or that does not cover enough questions. The score is evidence for
// the configuration it was run on; a later Prepare that changes the model, upstream name or formats clears it.
func (m *Monitor) RecordBenchmark(ep suppliers.Routable, run BenchmarkRun) (BenchmarkResult, bool) {
	set := fixedBenchmark()
	if !benchmarkRunAllowed(ep, run, set) {
		return BenchmarkResult{}, false
	}
	m.mu.RLock()
	baseline, ok := m.benchmarks[ep.Model]
	m.mu.RUnlock()
	if !ok || run.Format != baseline.run.Format || run.At.Sub(baseline.run.At) >= benchmarkMaxAge {
		return BenchmarkResult{}, false
	}
	score, ok := scoreBenchmark(set, baseline.run, run)
	if !ok {
		return BenchmarkResult{}, false
	}
	e := m.of(ep.ID)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current != configOf(ep) {
		return BenchmarkResult{}, false
	}
	if !e.benchmark.at.IsZero() && !run.At.After(e.benchmark.at) {
		return BenchmarkResult{}, false
	}
	e.benchmark = benchmarkState{
		version: set.Version, baseline: baseline.run.At,
		score: score.Score, confidence: score.Confidence, at: score.At,
	}
	return score, true
}
