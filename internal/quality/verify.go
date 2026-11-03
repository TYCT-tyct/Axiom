package quality

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// Verification is what the health loop knows about an endpoint's claim to serve the model its catalog entry names
// (docs/domains/quality.md「真伪检测」). A check is a real generation with a random challenge: the answer must echo a
// random string, give the sum and product of random numbers, reverse a random string, count its characters, name the
// model's author, and name the exact model and variant it serves. Three outcomes stay apart: unknown (never checked,
// the last pass is older than the configured maximum age, or an answer that says nothing), passed, and contradicted;
// verifyFailures consecutive contradictions are what the docs call a fake.
const (
	verifyFailures = 3        // consecutive contradictions that conclude an endpoint is fake
	verifyOutput   = 256      // tokens an answer may use: a JSON object with seven short fields
	verifyReply    = 16 << 10 // bytes of the answer kept for judging
)

// Claimed is what the catalog says an endpoint serves: the author's display name, which a verification answer must
// carry, every other author's display name, which instead contradicts the claim, the catalog model's stable identity,
// and the vendor-native effective name when the catalog has one. The upstream name can be a supplier alias (gpt-4
// mapping to gpt-4o), so the model comparison uses these catalog values, not the name sent upstream.
type Claimed struct {
	Author string
	Others []string
	Model  string // the catalog slug, a stable identity
	Name   string // the vendor-native name the model should report, when known
}

// comparable reports whether an answer can be checked: the author and at least one catalog model name are known.
func (c Claimed) comparable() bool {
	return c.Author != "" && (c.Model != "" || c.Name != "")
}

// verification is an endpoint's last conclusive check and its schedule. passed and checked move only on a conclusive
// answer; a contradiction clears passed at once, so routing fails closed, and verifyFailures of them in a row are the
// conclusion that the endpoint is fake.
type verification struct {
	claim     claim         // the configuration the evidence is about
	checked   time.Time     // the last conclusive check
	next      time.Time     // when the next check is due
	passed    bool          // the last conclusive check passed
	failures  int           // consecutive contradictions
	outcome   verifyOutcome // what the last check said, so a settings change can recompute its next time
	attempted time.Time     // when the last check was scheduled or its result recorded, the retry time base
}

// claim is what a check's evidence is about: the catalog model it names, the upstream name that serves it, and the
// format the check used. A different claim is different configuration, and old evidence says nothing about it.
type claim struct {
	model    uuid.UUID
	upstream string
	format   protocol.Format
}

// claimOf is the claim the endpoint's current configuration makes for verification, which checks the endpoint's first
// generation format: a pass says the model behind the name is what it claims, not that every format behaves alike.
func claimOf(ep suppliers.Routable) claim {
	return claim{model: ep.Model, upstream: ep.Upstream, format: ep.Formats[0]}
}

// factClaim binds a measured fact to the format it was measured in, not the endpoint's first format: a detector calls
// the endpoint in one call format, and its result says nothing about another (docs/domains/quality.md「实测的能力与上限」).
func factClaim(ep suppliers.Routable, format protocol.Format) claim {
	return claim{model: ep.Model, upstream: ep.Upstream, format: format}
}

// verified says the endpoint passed a check that is still recent and has not been contradicted since.
func (v verification) verified(now time.Time, maxAge time.Duration) bool {
	return v.passed && v.failures == 0 && !v.checked.IsZero() && now.Sub(v.checked) <= maxAge
}

// confidence is how much a pass still counts: full at checked, 0 at the maximum age.
func (v verification) confidence(now time.Time, maxAge time.Duration) float64 {
	if !v.verified(now, maxAge) {
		return 0
	}
	return max(0, 1-now.Sub(v.checked).Seconds()/maxAge.Seconds())
}

// due reports whether a check is due: one never made, or one whose schedule has passed.
func (v verification) due(now time.Time) bool { return v.next.IsZero() || !now.Before(v.next) }

// failed reports whether the endpoint is concluded fake: verifyFailures contradictions with no pass since.
func (v verification) failed() bool { return v.failures >= verifyFailures }

// verifyOutcome is what one check said: a pass, a contradiction of the claim, or nothing that says either way.
type verifyOutcome uint8

const (
	verifyUnknown verifyOutcome = iota // a refusal, a transport failure, or an answer that does not fit the question
	verifyPassed
	verifyContradicted
)

func (o verifyOutcome) String() string {
	switch o {
	case verifyPassed:
		return "passed"
	case verifyContradicted:
		return "contradicted"
	case verifyUnknown:
		return "inconclusive"
	}
	return "unknown"
}

// challenge is one verification question: the random values to compute, the catalog model to compare with, and the
// claim's author. The prompt deliberately does not contain the model name, so an answer that copies the claim instead
// of knowing it is what the check detects.
type challenge struct {
	nonce   string
	a, b    int
	factorX int
	factorY int
	product int
	origin  string
	reverse string
	check   int
	claimed Claimed
	model   string // the catalog identity, also the comparison target when Name is empty
	name    string // the vendor-native effective name, when the catalog has one
}

// newChallenge makes a question from the loop's randomness, which tests control.
func newChallenge(rand func() float64, claimed Claimed) challenge {
	pick := func(n int) int { return min(int(rand()*float64(n)), n-1) }
	model := strings.TrimSpace(claimed.Model)
	name := strings.TrimSpace(claimed.Name)
	if model == "" {
		model = name
	}
	c := challenge{
		nonce:   strconv.FormatUint(uint64(rand()*(1<<53)), 16) + strconv.FormatUint(uint64(rand()*(1<<53)), 16),
		a:       pick(64),
		b:       pick(64),
		factorX: 1000 + pick(9000),
		factorY: 100 + pick(900),
		claimed: claimed,
		model:   model,
		name:    name,
	}
	c.product = c.factorX * c.factorY
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	length := 6 + pick(4)
	var b strings.Builder
	b.Grow(length)
	for range length {
		b.WriteByte(alphabet[pick(len(alphabet))])
	}
	c.origin = b.String()
	c.reverse = reverse(c.origin)
	c.check = length
	return c
}

// prompt is the question: one JSON object whose values a relay that forwards, replays or rewrites the answer gets
// wrong or cannot know. The claimed model is not named, so the answer has to come from the model itself.
func (c challenge) prompt() string {
	return "Reply with one JSON object and nothing else, filling every value:\n" +
		`{"nonce":"` + c.nonce + `",` +
		`"sum":<the number ` + strconv.Itoa(c.a) + ` + ` + strconv.Itoa(c.b) + `>,` +
		`"model":"<the exact model identifier you are serving as, including its version>",` +
		`"author":"<the company that made you>",` +
		`"product":<the number ` + strconv.Itoa(c.factorX) + ` * ` + strconv.Itoa(c.factorY) + `>,` +
		`"reverse":"<the characters ` + c.origin + ` in reverse order>",` +
		`"check":{"ok":true,"n":<the number of characters in ` + c.origin + `>}}`
}

// judgeVerification reads an answer against the challenge. A pass is every known answer, the claimed author and the
// claimed model line and variant. A contradiction is an answer naming another author of the catalog and not the
// claimed one, or naming a different variant of the claimed model. Everything else is unknown: a
// refusal, a truncated answer, a different format, a wrong arithmetic answer or an unrecognized alias says nothing
// conclusive about the claim, and never counts as a failure (docs/domains/quality.md「真伪检测」).
func judgeVerification(result suppliers.TestResult, c challenge) verifyOutcome {
	if result.Outcome() != suppliers.TestPassed || result.Reply == "" {
		return verifyUnknown
	}
	object, ok := firstObject(result.Reply)
	if !ok || object["nonce"] == nil {
		// The question asks for one JSON object. Prose, line lists and partial objects say nothing conclusive: matching
		// fragments would let a wrong product or a longer reversed string pass.
		return verifyUnknown
	}
	return judgeObject(object, c)
}

// judgeObject reads the JSON answer the prompt asks for. A missing or mistyped field is unknown, not a wrong answer:
// format differences must not turn a genuine endpoint into a fake.
func judgeObject(object map[string]any, c challenge) verifyOutcome {
	switch {
	case objectAuthor(object, c) == identityContradicted, objectModel(object, c) == identityContradicted:
		return verifyContradicted
	case objectAnswers(object, c):
		return verifyPassed
	}
	return verifyUnknown
}

// objectAuthor is the author field's verdict.
func objectAuthor(object map[string]any, c challenge) identityVerdict {
	author, _ := object["author"].(string)
	return authorVerdict(author, c.claimed)
}

// objectModel is the model field's verdict.
func objectModel(object map[string]any, c challenge) identityVerdict {
	model, _ := object["model"].(string)
	return modelVerdictAny(model, c.model, c.name)
}

// objectAnswers reports the known answers and the claimed identity all at once.
func objectAnswers(object map[string]any, c challenge) bool {
	nonce, _ := object["nonce"].(string)
	sum, sumOK := objectInt(object["sum"])
	product, productOK := objectInt(object["product"])
	reverseAnswer, _ := object["reverse"].(string)
	check, _ := object["check"].(map[string]any)
	checkOK, checkN, checkNOK := checkAnswer(check)
	return !slices.Contains([]bool{
		strings.EqualFold(nonce, c.nonce),
		sumOK && sum == c.a+c.b,
		objectAuthor(object, c) == identityMatched,
		objectModel(object, c) == identityMatched,
		productOK && product == c.product,
		strings.EqualFold(strings.TrimSpace(reverseAnswer), c.reverse),
		checkOK && checkNOK && checkN == c.check,
	}, false)
}

// checkAnswer reads the nested check object: a true ok and the expected character count.
func checkAnswer(check map[string]any) (ok bool, n int, nOK bool) {
	if check == nil {
		return false, 0, false
	}
	ok, _ = check["ok"].(bool)
	n, nOK = objectInt(check["n"])
	return ok, n, nOK
}

// objectInt reads an integer from a decoded JSON value. A string is accepted too: a model that quotes a number has
// still answered it.
func objectInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	case int64:
		return int(v), true
	case int:
		return v, true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(v))
		return i, err == nil
	}
	return 0, false
}

// firstObject returns the first valid JSON object in reply, skipping prose and code fences before it.
func firstObject(reply string) (map[string]any, bool) {
	for start := 0; start < len(reply); {
		at := strings.IndexByte(reply[start:], '{')
		if at < 0 {
			return nil, false
		}
		start += at
		if raw, ok := balancedObject(reply[start:]); ok {
			var object map[string]any
			if err := jsonx.Unmarshal([]byte(raw), &object); err == nil && object != nil {
				return object, true
			}
		}
		start++
	}
	return nil, false
}

// balancedObject returns the first balanced object in s, respecting quoted strings and escapes.
func balancedObject(s string) (string, bool) {
	depth, quoted, escaped := 0, false, false
	for i := range len(s) {
		switch {
		case quoted:
			switch {
			case escaped:
				escaped = false
			case s[i] == '\\':
				escaped = true
			case s[i] == '"':
				quoted = false
			}
		case s[i] == '"':
			quoted = true
		case s[i] == '{':
			depth++
		case s[i] == '}':
			depth--
			if depth == 0 {
				return s[:i+1], true
			}
		}
	}
	return "", false
}

// reverse returns s with its bytes in reverse order; the challenge's alphabet is ASCII.
func reverse(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

// backoff is retry doubled failures-1 times, capped at every.
func backoff(failures int, retry, every time.Duration) time.Duration {
	if failures < 1 {
		return retry
	}
	if failures > 16 {
		return every
	}
	return min(retry<<uint(failures-1), every)
}
