package quality

import (
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"uuid"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// Verification is what the health loop knows about an endpoint's claim to serve the model its catalog entry names
// (docs/domains/quality.md「真伪检测」). A check is a real generation with a random challenge: the answer must echo a
// random string, give the sum of two random numbers, and name the model's author. Three outcomes stay apart: unknown
// (never checked, or the last pass is older than verifyMaxAge), passed, and contradicted; verifyFailures consecutive
// contradictions are what the docs call a fake.
const (
	verifyEvery    = 6 * time.Hour    // how long a pass stands before another check is due
	verifyMaxAge   = 24 * time.Hour   // how long a pass counts at all
	verifyRetry    = 15 * time.Minute // the first retry after a contradiction or an inconclusive answer
	verifyFailures = 3                // consecutive contradictions that conclude an endpoint is fake
	verifyOutput   = 48               // tokens an answer may use: three short lines
	verifyReply    = 4 << 10          // bytes of the answer kept for judging
)

// Claimed is what the catalog says an endpoint serves: the author's display name, which a verification answer must
// carry, and every other author's display name, which instead contradicts the claim.
type Claimed struct {
	Author string
	Others []string
}

// verification is an endpoint's last conclusive check and its schedule. passed and checked move only on a conclusive
// answer; a contradiction clears passed at once, so routing fails closed, and verifyFailures of them in a row are the
// conclusion that the endpoint is fake.
type verification struct {
	claim    claim     // the configuration the evidence is about
	checked  time.Time // the last conclusive check
	next     time.Time // when the next check is due
	passed   bool      // the last conclusive check passed
	failures int       // consecutive contradictions
}

// claim is what a check's evidence is about: the catalog model it names, the upstream name that serves it, and the
// format the check used. A different claim is different configuration, and old evidence says nothing about it.
type claim struct {
	model    uuid.UUID
	upstream string
	format   protocol.Format
}

// claimOf is the claim the endpoint's current configuration makes.
func claimOf(ep suppliers.Routable) claim {
	return claim{model: ep.Model, upstream: ep.Upstream, format: ep.Formats[0]}
}

// verified says the endpoint passed a check that is still recent and has not been contradicted since.
func (v verification) verified(now time.Time) bool {
	return v.passed && v.failures == 0 && !v.checked.IsZero() && now.Sub(v.checked) <= verifyMaxAge
}

// confidence is how much a pass still counts: full at checked, 0 at verifyMaxAge.
func (v verification) confidence(now time.Time) float64 {
	if !v.verified(now) {
		return 0
	}
	return max(0, 1-now.Sub(v.checked).Seconds()/verifyMaxAge.Seconds())
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

// challenge is one verification question: the random string to echo, the two numbers to add, and the claim to check.
type challenge struct {
	nonce   string
	a, b    int
	claimed Claimed
}

// newChallenge makes a question from the loop's randomness, which tests control.
func newChallenge(rand func() float64, claimed Claimed) challenge {
	return challenge{
		nonce:   strconv.FormatUint(uint64(rand()*(1<<53)), 16) + strconv.FormatUint(uint64(rand()*(1<<53)), 16),
		a:       min(int(rand()*64), 63),
		b:       min(int(rand()*64), 63),
		claimed: claimed,
	}
}

// prompt is the question: three answers with known values, each of which a relay that forwards, replays or rewrites
// the answer gets wrong or cannot know.
func (c challenge) prompt() string {
	return "Answer with three short lines and nothing else:\n" +
		"1. the characters " + c.nonce + "\n" +
		"2. the number " + strconv.Itoa(c.a) + " + " + strconv.Itoa(c.b) + "\n" +
		"3. the company that made you, in one word"
}

// judgeVerification reads an answer against the challenge. A pass is all three known answers; a contradiction is an
// answer naming another author of the catalog and not the claimed one, which is evidence of another model. Everything
// else is unknown: a refusal, a truncated answer or an answer formatted differently says nothing about the claim, and
// never counts as a failure (docs/domains/quality.md「真伪检测」).
func judgeVerification(result suppliers.TestResult, c challenge) verifyOutcome {
	if result.Outcome() != suppliers.TestPassed || result.Reply == "" {
		return verifyUnknown
	}
	reply := strings.ToLower(result.Reply)
	nonce := strings.Contains(reply, c.nonce)
	sum := answerSum(reply, c.nonce, c.a+c.b)
	author := containsName(reply, c.claimed.Author)
	switch {
	case nonce && sum && author:
		return verifyPassed
	case !author && containsAnyName(reply, c.claimed.Others):
		return verifyContradicted
	}
	return verifyUnknown
}

// answerSum reads the answer to the sum: the line after the nonce, with a list marker removed, and only if that line
// holds exactly one whole number. A line number is thereby not the answer ("2. 99" answers 99, not 2), and echoing the
// expression ("1 + 1") is no answer for either of its numbers.
func answerSum(reply, nonce string, want int) bool {
	lines := strings.Split(reply, "\n")
	at := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, nonce) })
	if at < 0 {
		return false
	}
	// The sum may share the nonce's line; what follows the nonce is then the answer.
	if n, ok := answerNumber(strings.Replace(numbered(lines[at]), nonce, "", 1)); ok && n == want {
		return true
	}
	for _, line := range lines[at+1:] {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		n, ok := answerNumber(numbered(s))
		return ok && n == want
	}
	return false
}

// numbered removes a leading list marker ("2. ", "2) ") from an answer line, keeping "2." when it is the answer
// itself. Two digits are enough: an answer has three lines, and a longer run is a number, not a marker.
func numbered(s string) string {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && i < 2 && '0' <= s[i] && s[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(s) || !strings.ContainsRune(".)]:-", rune(s[i])) {
		return s
	}
	if rest := strings.TrimSpace(s[i+1:]); rest != "" {
		return rest
	}
	return s
}

// answerNumber reads a line's answer: the part after its last "=" when it writes the equation out ("1 + 1 = 2"),
// otherwise the line itself, which must hold exactly one whole number.
func answerNumber(s string) (int, bool) {
	if i := strings.LastIndexByte(s, '='); i >= 0 {
		s = s[i+1:]
	}
	return singleNumber(s)
}

// singleNumber returns s's only whole decimal number, and whether it has exactly one.
func singleNumber(s string) (int, bool) {
	answer, found := 0, false
	for i := 0; i < len(s); {
		if s[i] < '0' || s[i] > '9' {
			i++
			continue
		}
		j := i
		for j < len(s) && '0' <= s[j] && s[j] <= '9' {
			j++
		}
		if j-i > 9 {
			return 0, false
		}
		v, err := strconv.Atoi(s[i:j])
		if err != nil || found {
			return 0, false
		}
		answer, found = v, true
		i = j
	}
	return answer, found
}

// containsName reports whether s names n. Both sides are compared as runs of letters and digits, lower-cased, so
// "Mistral AI", "Mistral" and "mistral.ai" all name "Mistral AI", while "metadata" does not name "Meta". A run may
// also be a shortened form of the name when it is at least five characters ("Mistral" for "Mistral AI"), and up to
// three adjacent runs are joined so a name split across punctuation or spacing still matches.
func containsName(s, name string) bool {
	want := compact(name)
	if len(want) < 3 {
		return false
	}
	runs := nameRuns(s)
	for i := range runs {
		if runs[i] == want || len(runs[i]) >= 5 && strings.HasPrefix(want, runs[i]) {
			return true
		}
		for j, joined := i+1, runs[i]; j < len(runs) && j < i+3; j++ {
			joined += runs[j]
			if joined == want {
				return true
			}
		}
	}
	return false
}

func containsAnyName(s string, names []string) bool {
	for _, n := range names {
		if containsName(s, n) {
			return true
		}
	}
	return false
}

// compact keeps only the lower-cased letters and digits of a name.
func compact(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// nameRuns splits s into its lower-cased runs of letters and digits.
func nameRuns(s string) []string {
	var runs []string
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		if b.Len() > 0 {
			runs = append(runs, b.String())
			b.Reset()
		}
	}
	if b.Len() > 0 {
		runs = append(runs, b.String())
	}
	return runs
}

// backoff is verifyRetry doubled failures-1 times, capped at verifyEvery.
func backoff(failures int) time.Duration {
	if failures < 1 {
		return verifyRetry
	}
	if failures > 16 {
		return verifyEvery
	}
	return min(verifyRetry<<uint(failures-1), verifyEvery)
}
