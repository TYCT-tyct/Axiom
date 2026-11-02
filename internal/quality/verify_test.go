package quality

import (
	"strconv"
	"testing"
	"time"

	"axiom/internal/suppliers"
)

func TestJudgeVerification(t *testing.T) {
	t.Parallel()
	c := challenge{
		nonce: "aaaaaaaa7aaaaaaaaaaaaaaaaaaaaaaa", a: 3, b: 4,
		claimed: Claimed{Author: "anthropic", Others: []string{"openai", "google"}},
	}
	sum := strconv.Itoa(c.a + c.b)
	for name, tc := range map[string]struct {
		reply string
		want  verifyOutcome
	}{
		"three answers":         {c.nonce + "\n" + sum + "\nAnthropic", verifyPassed},
		"numbered and wrapped":  {"1. `" + c.nonce + "`\n2. " + sum + "\n3. Anthropic.", verifyPassed},
		"the sum in words":      {c.nonce + "\n" + sum + " (three plus four)\nAnthropic", verifyPassed},
		"the claimed author":    {c.nonce + "\n" + sum + "\nAnthropic, not OpenAI", verifyPassed},
		"another author":        {c.nonce + "\n" + sum + "\nOpenAI", verifyContradicted},
		"another author alone":  {"google", verifyContradicted},
		"a wrong sum":           {c.nonce + "\n8\nAnthropic", verifyUnknown},
		"no nonce":              {sum + "\nAnthropic", verifyUnknown},
		"no author":             {c.nonce + "\n" + sum, verifyUnknown},
		"a refusal":             {"I cannot answer that.", verifyUnknown},
		"the nonce's own digit": {c.nonce + "\n\nAnthropic", verifyUnknown}, // the 7 is inside the nonce, not an answer
		"a longer word":         {c.nonce + "\n" + sum + "\nanthropicx", verifyUnknown},
		"metadata is not Meta":  {c.nonce + "\n" + sum + "\nmetadata", verifyUnknown},
		"sum inside a number":   {c.nonce + "\n17\nAnthropic", verifyUnknown},
		"empty":                 {"", verifyUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: tc.reply}, c)
			if got != tc.want {
				t.Errorf("judged %s, want %s", got, tc.want)
			}
		})
	}
}

// TestJudgeVerificationNeedsAPassedGeneration: a failed or cut-off generation says nothing about the claim, whatever
// its text happened to be.
func TestJudgeVerificationNeedsAPassedGeneration(t *testing.T) {
	t.Parallel()
	c := challenge{nonce: "ab", a: 1, b: 2, claimed: Claimed{Author: "anthropic"}}
	reply := "ab\n3\nAnthropic"
	if got := judgeVerification(suppliers.TestResult{Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}, Reply: reply}, c); got != verifyUnknown {
		t.Errorf("a failed generation judged %s, want inconclusive", got)
	}
	if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: "anthropic"}, c); got != verifyUnknown {
		t.Errorf("a pass without the known answers judged %s", got)
	}
}

// verificationView is a verification read as the assertions see it, so each expectation is one comparable value.
type verificationView struct {
	verified    bool
	due, failed bool
	confidence  float64
}

func (v verification) view(at time.Time) verificationView {
	return verificationView{verified: v.verified(at, defaultVerifyMaxAge), due: v.due(at), failed: v.failed(), confidence: v.confidence(at, defaultVerifyMaxAge)}
}

func (v verificationView) want(t *testing.T, at time.Time, want verificationView) {
	t.Helper()
	if v != want {
		t.Fatalf("at %v: verified %v, confidence %v, due %v, failed %v; want %+v", at, v.verified, v.confidence, v.due, v.failed, want)
	}
}

// TestVerificationConfidenceAndDue: a pass is full confidence at once, is not verified after verifyMaxAge, and is due
// again after verifyEvery; an endpoint never checked is unverified and due.
func TestVerificationConfidenceAndDue(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var v verification
	v.view(at).want(t, at, verificationView{due: true})
	v.passed, v.checked, v.next = true, at, at.Add(defaultVerifyEvery)
	v.view(at).want(t, at, verificationView{verified: true, confidence: 1})
	v.view(at.Add(defaultVerifyEvery)).want(t, at.Add(defaultVerifyEvery), verificationView{verified: true, confidence: .75, due: true})
	v.view(at.Add(defaultVerifyMaxAge+time.Second)).want(t, at.Add(defaultVerifyMaxAge+time.Second), verificationView{due: true})
	v.passed = false // one contradiction clears it at once, but is not yet a fake
	v.failures = 1
	v.view(at).want(t, at, verificationView{})
	v.failures = verifyFailures
	v.view(at).want(t, at, verificationView{failed: true})
	v.passed, v.failures = true, 0
	v.view(at).want(t, at, verificationView{verified: true, confidence: 1})
}

// TestJudgeVerificationNames: a company display name need not appear verbatim—"Mistral" names "Mistral AI"—but a
// substring of a longer word or another company's name is not a pass.
func TestJudgeVerificationNames(t *testing.T) {
	t.Parallel()
	c := challenge{nonce: "beef", a: 2, b: 2, claimed: Claimed{Author: "Mistral AI", Others: []string{"OpenAI", "Meta"}}}
	for name, tc := range map[string]struct {
		author, reply string
		want          verifyOutcome
	}{
		"the full name":        {"Mistral AI", "Mistral AI", verifyPassed},
		"the short name":       {"Mistral AI", "Mistral", verifyPassed},
		"another company":      {"Mistral AI", "OpenAI", verifyContradicted},
		"the other company":    {"Mistral AI", "I am Meta's model", verifyContradicted},
		"a word inside Meta":   {"Mistral AI", "metadata", verifyUnknown},
		"too short to shorten": {"OpenAI", "Open", verifyUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ch := c
			ch.claimed.Author = tc.author
			reply := ch.nonce + "\n" + strconv.Itoa(ch.a+ch.b) + "\n" + tc.reply
			if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: reply}, ch); got != tc.want {
				t.Errorf("judged %s, want %s", got, tc.want)
			}
		})
	}
}

func TestContainsName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		s, name string
		want    bool
	}{
		{"Mistral AI", "Mistral AI", true},
		{"made by Mistral", "Mistral AI", true},
		{"xAI", "xAI", true},
		{"made by Z.ai", "Z.ai", true},
		{"01.AI", "01.AI", true},
		{"Google DeepMind", "Google", true},
		{"Meta", "Meta", true},
		{"metadata", "Meta", false},
		{"open source", "OpenAI", false},
		{"Anthropic's Claude", "Anthropic", true},
		{"", "Meta", false},
	} {
		if got := containsName(tc.s, tc.name); got != tc.want {
			t.Errorf("containsName(%q, %q) = %v, want %v", tc.s, tc.name, got, tc.want)
		}
	}
}

func TestAnswerSum(t *testing.T) {
	t.Parallel()
	const nonce = "ab12cd"
	for name, tc := range map[string]struct {
		reply string
		want  int
		ok    bool
	}{
		"on its own line":          {nonce + "\n7\nAnthropic", 7, true},
		"after a list number":      {nonce + "\n2. 7\nAnthropic", 7, true},
		"a numbered wrong answer":  {nonce + "\n2. 99\nAnthropic", 2, false},
		"the number is the answer": {nonce + "\n2. 2\nAnthropic", 2, true},
		"an echoed expression":     {nonce + "\n1 + 1\nAnthropic", 2, false},
		"an equation":              {nonce + "\n1 + 1 = 2\nAnthropic", 2, true},
		"a lone numbered answer":   {nonce + "\n2.\nAnthropic", 2, true},
		"a label before it":        {nonce + "\nThe sum is 7.\nAnthropic", 7, true},
		"on the nonce's line":      {nonce + " 7\nAnthropic", 7, true},
		"no answer":                {nonce + "\nAnthropic", 7, false},
		"no nonce line":            {"7", 7, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := answerSum(tc.reply, nonce, tc.want); got != tc.ok {
				t.Errorf("answerSum(%q, %d) = %v, want %v", tc.reply, tc.want, got, tc.ok)
			}
		})
	}
}

// TestJudgeVerificationNumbering: the number in front of an answer is a list marker, not the answer, and an echoed
// expression answers nothing.
func TestJudgeVerificationNumbering(t *testing.T) {
	t.Parallel()
	c := challenge{nonce: "cc11", a: 1, b: 1, claimed: Claimed{Author: "anthropic"}}
	for name, tc := range map[string]struct {
		reply string
		want  verifyOutcome
	}{
		"the line number is not the sum": {c.nonce + "\n2. 99\nAnthropic", verifyUnknown},
		"the expression is no answer":    {c.nonce + "\n1 + 1\nAnthropic", verifyUnknown},
		"the equation answered":          {c.nonce + "\n1 + 1 = 2\nAnthropic", verifyPassed},
		"the numbered answer":            {c.nonce + "\n2. 2\nAnthropic", verifyPassed},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: tc.reply}, c); got != tc.want {
				t.Errorf("judged %s, want %s", got, tc.want)
			}
		})
	}
}
