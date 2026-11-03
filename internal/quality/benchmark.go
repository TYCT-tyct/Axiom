package quality

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

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

//go:embed benchmark-v2.json
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
	return ok && reflect.DeepEqual(got, want)
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

// cloneBenchmarkRun detaches a recorded run's answers from the caller's slice, so a reused or mutated input cannot
// change the baseline after it has been accepted.
func cloneBenchmarkRun(run BenchmarkRun) BenchmarkRun {
	run.Answers = slices.Clone(run.Answers)
	return run
}
