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

// TestVerificationConfidenceAndDue: a pass is full confidence at once, is not verified after verifyMaxAge, and is due
// again after verifyEvery; an endpoint never checked is unverified and due.
func TestVerificationConfidenceAndDue(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var v verification
	if v.verified(at) || v.confidence(at) != 0 || !v.due(at) || v.failed() {
		t.Fatalf("a never-checked endpoint: verified %v, confidence %v, due %v, failed %v", v.verified(at), v.confidence(at), v.due(at), v.failed())
	}
	v.passed, v.checked, v.next = true, at, at.Add(verifyEvery)
	if !v.verified(at) || v.confidence(at) != 1 || v.due(at) {
		t.Fatalf("a fresh pass: verified %v, confidence %v, due %v", v.verified(at), v.confidence(at), v.due(at))
	}
	if v.verified(at.Add(verifyMaxAge+time.Second)) || v.confidence(at.Add(verifyMaxAge+time.Second)) != 0 {
		t.Fatal("a pass did not age out")
	}
	if !v.due(at.Add(verifyEvery)) {
		t.Fatal("a pass is not checked again after verifyEvery")
	}
	v.passed = false // one contradiction clears it at once, but is not yet a fake
	v.failures = 1
	if v.verified(at) || v.failed() {
		t.Fatalf("one contradiction: verified %v, failed %v", v.verified(at), v.failed())
	}
	v.failures = verifyFailures
	if !v.failed() {
		t.Fatal("three contradictions are not a fake")
	}
	v.passed, v.failures = true, 0
	if !v.verified(at) {
		t.Fatal("a later pass did not restore the endpoint")
	}
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

func TestContainsNumber(t *testing.T) {
	t.Parallel()
	for s, want := range map[string]bool{"7": true, " 7.": true, "07": true, "17": false, "a7b": true, "70": false, "": false, "the 7th": true} {
		if got := containsNumber(s, 7); got != want {
			t.Errorf("containsNumber(%q) = %v, want %v", s, got, want)
		}
	}
}
