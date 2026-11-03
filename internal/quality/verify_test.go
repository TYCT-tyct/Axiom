package quality

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers"
)

// testModel is the model name the tests claim unless a sample says otherwise.
const testModel = "claude-3-5-sonnet-20241022"

// testChallenge is a fixed question whose answers the recorded samples below use.
func testChallenge() challenge {
	return challenge{
		nonce: "a1b2c3d4e5f60718", a: 17, b: 25,
		factorX: 4873, factorY: 926, product: 4512398,
		origin: "k3y9p2", reverse: "2p9y3k", check: 6,
		claimed: Claimed{Author: "Anthropic", Others: []string{"OpenAI", "Google"}},
		model:   testModel,
	}
}

// fullAnswer is a line-based answer with every known value, for tests about one field.
func fullAnswer(c challenge, model, author string) string {
	return c.nonce + "\n" + strconv.Itoa(c.a+c.b) + "\n" + answerTail(c, model, author)
}

// answerTail is what follows the sum: model, author, product, reverse and the check object.
func answerTail(c challenge, model, author string) string {
	return model + "\n" + author + "\n" + strconv.Itoa(c.product) + "\n" + c.reverse + "\n" +
		`{"ok":true,"n":` + strconv.Itoa(c.check) + `}`
}

func TestJudgeVerification(t *testing.T) {
	t.Parallel()
	c := testChallenge()
	sum := strconv.Itoa(c.a + c.b)
	for name, tc := range map[string]struct {
		reply string
		want  verifyOutcome
	}{
		"all answers":           {fullAnswer(c, testModel, "Anthropic"), verifyPassed},
		"numbered and wrapped":  {"1. `" + c.nonce + "`\n2. " + sum + "\n3. " + testModel + "\n4. Anthropic.\n5. " + strconv.Itoa(c.product) + "\n6. " + c.reverse + "\n7. {\"ok\":true,\"n\":6}", verifyPassed},
		"the sum in words":      {c.nonce + "\n" + sum + " (three plus four)\n" + answerTail(c, testModel, "Anthropic"), verifyPassed},
		"the claimed author":    {fullAnswer(c, testModel, "Anthropic, not OpenAI"), verifyPassed},
		"another author":        {fullAnswer(c, testModel, "OpenAI"), verifyContradicted},
		"another author alone":  {"google", verifyContradicted},
		"a wrong sum":           {c.nonce + "\n8\n" + answerTail(c, testModel, "Anthropic"), verifyUnknown},
		"no nonce":              {sum + "\n" + answerTail(c, testModel, "Anthropic"), verifyUnknown},
		"no author":             {c.nonce + "\n" + sum + "\n" + testModel + "\n" + strconv.Itoa(c.product) + "\n" + c.reverse + "\n{\"ok\":true,\"n\":6}", verifyUnknown},
		"a refusal":             {"I cannot answer that.", verifyUnknown},
		"the nonce's own digit": {c.nonce + "\n\nAnthropic", verifyUnknown}, // the 7 is inside the nonce, not an answer
		"a longer word":         {fullAnswer(c, testModel, "anthropicx"), verifyUnknown},
		"metadata is not Meta":  {fullAnswer(c, testModel, "metadata"), verifyUnknown},
		"sum inside a number":   {c.nonce + "\n17\n" + answerTail(c, testModel, "Anthropic"), verifyUnknown},
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
	c := testChallenge()
	c.nonce, c.a, c.b = "ab", 1, 2
	reply := fullAnswer(c, testModel, "Anthropic")
	if got := judgeVerification(suppliers.TestResult{Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}, Reply: reply}, c); got != verifyUnknown {
		t.Errorf("a failed generation judged %s, want inconclusive", got)
	}
	if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: "anthropic"}, c); got != verifyUnknown {
		t.Errorf("a pass without the known answers judged %s", got)
	}
}

// TestJudgeVerificationCustomUpstreamName: the line fallback does not depend on a known model family when the answer
// names the claimed upstream exactly.
func TestJudgeVerificationCustomUpstreamName(t *testing.T) {
	t.Parallel()
	c := testChallenge()
	c.model = "acme-chat-2026-01-01"
	if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: fullAnswer(c, c.model, "Anthropic")}, c); got != verifyPassed {
		t.Errorf("a custom upstream name judged %s, want passed", got)
	}
}

// TestModelVerdict: a date snapshot is the same model; another size or tier of the same author is not; an alias that
// cannot be resolved is unknown rather than a fake.
func TestModelVerdict(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		answer, claim string
		want          identityVerdict
	}{
		"the dated snapshot":      {"GPT-4o 2024-08-06", "gpt-4o-2024-08-06", identityMatched},
		"the compact date":        {"gpt-4o 20240806", "gpt-4o-2024-08-06", identityMatched},
		"the provider prefix":     {"OpenAI GPT-4o", "openai/gpt-4o", identityMatched},
		"a cheaper sibling":       {"gpt-4o-mini-2024-07-18", "gpt-4o-2024-08-06", identityContradicted},
		"a smaller o-series":      {"o1-mini", "o1", identityContradicted},
		"a different claude tier": {"Claude 3.5 Haiku", "claude-3-5-sonnet-20241022", identityContradicted},
		"the same claude tier":    {"Claude 3.5 Sonnet", "claude-3-5-sonnet-20241022", identityMatched},
		"a different gemini tier": {"Gemini 2.5 Flash", "gemini-2.5-pro", identityContradicted},
		"a different llama size":  {"Llama 3.1 8B", "llama-3.3-70b", identityContradicted},
		"a preview snapshot":      {"Gemini 2.5 Pro", "gemini-2.5-pro-preview-06-05", identityMatched},
		"a dated build":           {"DeepSeek-V3", "deepseek-v3-0324", identityMatched},
		"a three digit build":     {"Gemini 1.5 Pro", "gemini-1.5-pro-002", identityMatched},
		"a plus variant":          {"Command R+", "command-r-plus-08-2024", identityMatched},
		"the same instruct model": {"Llama 3 70B Instruct", "llama-3-70b-instruct", identityMatched},
		"a missing instruct word": {"Llama 3 70B", "llama-3-70b-instruct", identityUnknown},
		"a shorter alias":         {"DeepSeek-V3", "deepseek-chat", identityUnknown},
		"a different version":     {"GPT-4", "gpt-4o", identityUnknown},
		"a shorter answer":        {"GPT-4o", "gpt-4o-mini", identityUnknown},
		"an empty answer":         {"", "gpt-4o", identityUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := modelVerdict(tc.answer, tc.claim); got != tc.want {
				t.Errorf("modelVerdict(%q, %q) = %v, want %v", tc.answer, tc.claim, got, tc.want)
			}
		})
	}
}

// recordedSample is one reply replayed by TestJudgeVerificationRecordedSamples.
type recordedSample struct {
	Name   string   `json:"name"`
	Model  string   `json:"model"`
	Author string   `json:"author"`
	Others []string `json:"others"`
	Reply  string   `json:"reply"`
	Want   string   `json:"want"`
}

// TestJudgeVerificationRecordedSamples replays the recorded replies in testdata: the claimed model passes, a cheaper
// model of the same author contradicts, and an alias or a refusal is inconclusive.
func TestJudgeVerificationRecordedSamples(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/verification_samples.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Note    string           `json:"note"`
		Samples []recordedSample `json:"samples"`
	}
	if err := jsonx.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.Note == "" || len(file.Samples) == 0 {
		t.Fatal("the recorded samples have no provenance note or no samples")
	}
	for _, sample := range file.Samples {
		t.Run(sample.Name, func(t *testing.T) {
			t.Parallel()
			c := testChallenge()
			c.model = sample.Model
			c.claimed = Claimed{Author: sample.Author, Others: sample.Others}
			got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: sample.Reply}, c)
			if got.String() != sample.Want {
				t.Errorf("judged %s, want %s", got, sample.Want)
			}
		})
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
	c := testChallenge()
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
			ch.claimed.Others = []string{"OpenAI", "Meta", "Google"}
			if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: fullAnswer(ch, testModel, tc.reply)}, ch); got != tc.want {
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
		{"Google", "Google LLC", true},
		{"Meta", "Meta Platforms, Inc.", true},
		{"AI", "Mistral AI", false},
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
	c := testChallenge()
	c.nonce, c.a, c.b = "cc11", 1, 1
	for name, tc := range map[string]struct {
		reply string
		want  verifyOutcome
	}{
		"the line number is not the sum": {c.nonce + "\n2. 99\n" + answerTail(c, testModel, "Anthropic"), verifyUnknown},
		"the expression is no answer":    {c.nonce + "\n1 + 1\n" + answerTail(c, testModel, "Anthropic"), verifyUnknown},
		"the equation answered":          {c.nonce + "\n1 + 1 = 2\n" + answerTail(c, testModel, "Anthropic"), verifyPassed},
		"the numbered answer":            {c.nonce + "\n2. 2\n" + answerTail(c, testModel, "Anthropic"), verifyPassed},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: tc.reply}, c); got != tc.want {
				t.Errorf("judged %s, want %s", got, tc.want)
			}
		})
	}
}

// TestJudgeVerificationWrongProbe: a correct identity with a wrong known answer is inconclusive, not a contradiction:
// an arithmetic slip must not be reported as a swapped model.
func TestJudgeVerificationWrongProbe(t *testing.T) {
	t.Parallel()
	c := testChallenge()
	good := fullAnswer(c, testModel, "Anthropic")
	for name, reply := range map[string]string{
		"a wrong product": strings.Replace(good, strconv.Itoa(c.product), strconv.Itoa(c.product+1), 1),
		"the original":    strings.Replace(good, c.reverse, c.origin, 1),
		"a wrong count":   strings.Replace(good, `"n":`+strconv.Itoa(c.check), `"n":`+strconv.Itoa(c.check+1), 1),
		"no check":        strings.Replace(good, `{"ok":true,"n":`+strconv.Itoa(c.check)+`}`, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: reply}, c); got != verifyUnknown {
				t.Errorf("judged %s, want inconclusive", got)
			}
		})
	}
}
