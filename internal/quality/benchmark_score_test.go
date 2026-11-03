package quality

import (
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

func TestJudgeBenchmarkAnswer(t *testing.T) {
	t.Parallel()
	question := func(checks ...BenchmarkCheck) BenchmarkQuestion {
		return BenchmarkQuestion{ID: "q", Category: "test", Prompt: "?", MaxOutput: 16, Checks: checks}
	}
	for _, c := range []struct {
		name   string
		checks []BenchmarkCheck
		reply  string
		want   float64
	}{
		{"equals trims surrounding space", []BenchmarkCheck{{Kind: checkEquals, Value: "391"}}, " 391\n", 1},
		{"equals is exact", []BenchmarkCheck{{Kind: checkEquals, Value: "391"}}, "The answer is 391", 0},
		{"contains ignores case", []BenchmarkCheck{{Kind: checkContains, Value: "sequence"}}, "A Sequence number orders segments.", 1},
		{"contains is a substring", []BenchmarkCheck{{Kind: checkContains, Value: "acknowledge"}}, "The server does not confirm.", 0},
		{"one_of ignores case and space", []BenchmarkCheck{{Kind: checkOneOf, Values: []string{"yes", "no"}}}, " YES ", 1},
		{"one_of needs one whole value", []BenchmarkCheck{{Kind: checkOneOf, Values: []string{"yes"}}}, "yes, because", 0},
		{
			"json fields",
			[]BenchmarkCheck{{Kind: checkJSONFields, Fields: map[string]string{"name": "Ada", "age": "36"}}},
			`{"name":"Ada","age":36}`, 1,
		},
		{
			"json fields in a fence",
			[]BenchmarkCheck{{Kind: checkJSONFields, Fields: map[string]string{"ok": "true", "count": "3"}}},
			"```json\n{\"ok\":true,\"count\":3}\n```", 1,
		},
		{
			"json fields missing",
			[]BenchmarkCheck{{Kind: checkJSONFields, Fields: map[string]string{"name": "Ada"}}},
			`{"age":36}`, 0,
		},
		{
			"json fields wrong",
			[]BenchmarkCheck{{Kind: checkJSONFields, Fields: map[string]string{"age": "37"}}},
			`{"age":"36"}`, 0,
		},
		{"json equals", []BenchmarkCheck{{Kind: checkJSONEquals, Value: "[1,4,9,16]"}}, `[1, 4, 9, 16]`, 1},
		{"json equals order", []BenchmarkCheck{{Kind: checkJSONEquals, Value: "[1,4,9,16]"}}, `[4,1,9,16]`, 0},
		{"a partial multi-check answer", []BenchmarkCheck{
			{Kind: checkContains, Value: "sequence"},
			{Kind: checkContains, Value: "acknowledge"},
		}, "A sequence number orders the segments.", 0.5},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := judgeBenchmarkAnswer(question(c.checks...), c.reply); got != c.want {
				t.Errorf("judge = %v, want %v", got, c.want)
			}
		})
	}
}

// syntheticSet makes n single-check questions that all expect "yes", so the scoring tests do not depend on the embedded
// question set's contents.
func syntheticSet(n int) *BenchmarkSet {
	questions := make([]BenchmarkQuestion, n)
	for i := range questions {
		questions[i] = BenchmarkQuestion{
			ID: fmt.Sprintf("q%02d", i), Category: "test", Prompt: "?", MaxOutput: 16,
			Checks: []BenchmarkCheck{{Kind: checkEquals, Value: "yes"}},
		}
	}
	return &BenchmarkSet{Version: "test-v1", Hash: "test-hash", Questions: questions}
}

// syntheticRun answers the first answered questions, the first correct of them with "yes" and the rest with "no".
func syntheticRun(set *BenchmarkSet, at time.Time, answered, correct int) BenchmarkRun {
	run := BenchmarkRun{Version: set.Version, At: at}
	for i, q := range set.Questions {
		if i >= answered {
			break
		}
		reply := "no"
		if i < correct {
			reply = "yes"
		}
		run.Answers = append(run.Answers, BenchmarkAnswer{Question: q.ID, Reply: reply, Complete: true})
	}
	return run
}

//nolint:gocognit // the table and its assertions are the test
func TestScoreBenchmark(t *testing.T) {
	t.Parallel()
	set := syntheticSet(20)
	official := syntheticRun(set, t0, 20, 20)
	for _, c := range []struct {
		name       string
		set        *BenchmarkSet
		official   BenchmarkRun
		candidate  BenchmarkRun
		wantOK     bool
		wantScore  float64
		wantConf   float64
		wantPoints float64
	}{
		{"official is 100", set, official, official, true, 100, 1, 20},
		{"a perfect candidate is capped at official", set, syntheticRun(set, t0, 20, 18), syntheticRun(set, t0, 20, 20), true, 100, 0.833, 20},
		{"a weaker candidate keeps the ratio", set, official, syntheticRun(set, t0, 20, 15), true, 75, 0.805, 15},
		{"a candidate that answered nothing", set, official, syntheticRun(set, t0, 20, 0), true, 0, 1, 0},
		{"a candidate under the coverage floor has no score", set, official, syntheticRun(set, t0, 17, 17), false, 0, 0, 0},
		{"an official under the coverage floor has no score", set, syntheticRun(set, t0, 17, 17), syntheticRun(set, t0, 17, 17), false, 0, 0, 0},
		{"fewer than the minimum pairs has no score", set, official, syntheticRun(set, t0, 19, 19), false, 0, 0, 0},
		{"an official that passed nothing has no score", set, syntheticRun(set, t0, 20, 0), syntheticRun(set, t0, 20, 20), false, 0, 0, 0},
		{"a candidate under the 30-question floor", syntheticSet(30), syntheticRun(syntheticSet(30), t0, 30, 30), syntheticRun(syntheticSet(30), t0, 30, 27), true, 90, 0.891, 27},
		{"a candidate that answered 90% correctly", syntheticSet(30), syntheticRun(syntheticSet(30), t0, 30, 30), syntheticRun(syntheticSet(30), t0, 27, 27), true, 90, 0.9, 27},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, ok := scoreBenchmark(c.set, c.official, c.candidate)
			if ok != c.wantOK {
				t.Fatalf("scored = %v, want %v (%+v)", ok, c.wantOK, got)
			}
			if !ok {
				return
			}
			if math.Abs(got.Score-c.wantScore) > 1e-9 {
				t.Errorf("score = %v, want %v", got.Score, c.wantScore)
			}
			if math.Abs(got.Confidence-c.wantConf) > 1e-3 {
				t.Errorf("confidence = %v, want %v", got.Confidence, c.wantConf)
			}
			if got.Points != c.wantPoints {
				t.Errorf("points = %v, want %v", got.Points, c.wantPoints)
			}
		})
	}
}

func TestScoreBenchmarkRejectsMalformedRuns(t *testing.T) {
	t.Parallel()
	set := syntheticSet(20)
	official := syntheticRun(set, t0, 20, 20)
	candidate := syntheticRun(set, t0, 20, 20)
	duplicate := candidate
	duplicate.Answers = append(duplicate.Answers, candidate.Answers[0])
	noTime := candidate
	noTime.At = time.Time{}
	other := candidate
	other.Version = "other"
	emptyQuestion := candidate
	emptyQuestion.Answers = append(emptyQuestion.Answers[:0:0], candidate.Answers...)
	emptyQuestion.Answers[0].Question = ""
	unknownQuestion := candidate
	unknownQuestion.Answers = append(unknownQuestion.Answers[:0:0], candidate.Answers...)
	unknownQuestion.Answers[0].Question = "not-in-this-set"
	otherFormat := candidate
	otherFormat.Format = protocol.Chat
	for name, run := range map[string]BenchmarkRun{
		"another version":  other,
		"another format":   otherFormat,
		"duplicate":        duplicate,
		"empty question":   emptyQuestion,
		"unknown question": unknownQuestion,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got, ok := scoreBenchmark(set, official, run); ok {
				t.Fatalf("scored a malformed run: %+v", got)
			}
		})
	}
	noTimeOfficial := official
	noTimeOfficial.At = time.Time{}
	if got, ok := scoreBenchmark(set, noTimeOfficial, noTime); ok {
		t.Fatalf("scored a run without a time: %+v", got)
	}
}

// TestBenchmarkRecordedSamples replays the recorded runs in testdata: the official run is 100 and is the anchor, a
// capable relay scores just below it, a degraded relay scores well below, and a run that answered half the set has no
// score at all. The fixture was written to the set; this machine has no model API credentials.
//
//nolint:gocognit // the fixture's expected table and its assertions are the test
func TestBenchmarkRecordedSamples(t *testing.T) {
	t.Parallel()
	set := fixedBenchmark()
	runs := recordedBenchmarkRuns(t, set)
	official, ok := runs["official"]
	if !ok {
		t.Fatal("no official run")
	}
	for name, want := range map[string]struct {
		score            float64
		minConf, maxConf float64
		scored           bool
	}{
		"official":       {100, 1, 1, true},
		"capable-relay":  {100 * 31.0 / 34.0, 0.85, 0.95, true},
		"degraded-relay": {100 * (23.0 + 2.0/3.0) / 34.0, 0.80, 0.90, true},
		"partial-run":    {0, 0, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := scoreBenchmark(set, official, runs[name])
			if ok != want.scored {
				t.Fatalf("scored = %v, want %v (%+v)", ok, want.scored, got)
			}
			if !ok {
				return
			}
			if math.Abs(got.Score-want.score) > 1e-9 {
				t.Errorf("score = %v, want %v", got.Score, want.score)
			}
			if got.Confidence < want.minConf || got.Confidence > want.maxConf {
				t.Errorf("confidence = %v, want within [%v, %v]", got.Confidence, want.minConf, want.maxConf)
			}
			if got.Questions != len(set.Questions) || got.Answered == 0 {
				t.Errorf("coverage = %d/%d, want the whole set with answers", got.Answered, got.Questions)
			}
		})
	}
}

// recordedBenchmarkRun is one entry of testdata/benchmark_samples.json.
type recordedBenchmarkRun struct {
	Name    string            `json:"name"`
	Role    string            `json:"role"`
	At      time.Time         `json:"at"`
	Answers map[string]string `json:"answers"`
}

// recordedBenchmarkRuns reads and validates the fixture before any score is computed: a typo in a role or an answer to
// a question the set no longer has would otherwise make the replay test pass against different data than it names.
//
//nolint:cyclop,gocognit // one pass over the fixture, with a named refusal for each malformed entry
func recordedBenchmarkRuns(t *testing.T, set *BenchmarkSet) map[string]BenchmarkRun {
	t.Helper()
	raw, err := os.ReadFile("testdata/benchmark_samples.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Note string                 `json:"note"`
		Set  string                 `json:"set"`
		Runs []recordedBenchmarkRun `json:"runs"`
	}
	if err := jsonx.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.Note == "" || len(file.Runs) < 2 {
		t.Fatal("the recorded samples have no provenance note or too few runs")
	}
	if file.Set != set.Version {
		t.Fatalf("the samples are for set %q, want %q", file.Set, set.Version)
	}
	known := make(map[string]bool, len(set.Questions))
	for _, q := range set.Questions {
		known[q.ID] = true
	}
	runs := map[string]BenchmarkRun{}
	for _, r := range file.Runs {
		if r.Role != "official" && r.Role != "candidate" {
			t.Fatalf("run %q has role %q, want official or candidate", r.Name, r.Role)
		}
		if (r.Name == "official") != (r.Role == "official") {
			t.Fatalf("run %q has role %q", r.Name, r.Role)
		}
		run := BenchmarkRun{Version: set.Version, At: r.At}
		for id, reply := range r.Answers {
			if !known[id] {
				t.Fatalf("run %q answers unknown question %q", r.Name, id)
			}
			run.Answers = append(run.Answers, BenchmarkAnswer{Question: id, Reply: reply, Complete: true})
		}
		runs[r.Name] = run
	}
	return runs
}
