package quality

import (
	"os"
	"strconv"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers"
)

// testModel is the catalog model the tests claim unless a sample says otherwise.
const testModel = "claude-3-5-sonnet-20241022"

// testChallenge is a fixed question whose answers the recorded samples below use.
func testChallenge() challenge {
	return challenge{
		nonce: "a1b2c3d4e5f60718", a: 17, b: 25,
		factorX: 4873, factorY: 926, product: 4512398,
		origin: "k3y9p2", reverse: "2p9y3k", check: 6,
		claimed: Claimed{Author: "Anthropic", Others: []string{"OpenAI", "Google"}, Model: testModel},
		model:   testModel,
	}
}

// answerObject is a complete JSON answer.
func answerObject(c challenge, model, author string) map[string]any {
	return map[string]any{
		"nonce": c.nonce, "sum": c.a + c.b, "model": model, "author": author,
		"product": c.product, "reverse": c.reverse,
		"check": map[string]any{"ok": true, "n": c.check},
	}
}

// answerJSON is answerObject with one field changed, for tests about that field.
func answerJSON(c challenge, model, author string, change func(map[string]any)) string {
	object := answerObject(c, model, author)
	if change != nil {
		change(object)
	}
	raw, err := jsonx.MarshalCanonical(object)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// fullAnswer is a complete JSON answer for tests about one field.
func fullAnswer(c challenge, model, author string) string {
	return answerJSON(c, model, author, nil)
}

func TestJudgeVerification(t *testing.T) {
	t.Parallel()
	c := testChallenge()
	for name, tc := range map[string]struct {
		reply string
		want  verifyOutcome
	}{
		"all answers":          {fullAnswer(c, testModel, "Anthropic"), verifyPassed},
		"wrapped in prose":     {"Here is the answer:\n```json\n" + fullAnswer(c, testModel, "Anthropic") + "\n```", verifyPassed},
		"the claimed author":   {fullAnswer(c, testModel, "Anthropic, not OpenAI"), verifyPassed},
		"another author":       {fullAnswer(c, testModel, "OpenAI"), verifyContradicted},
		"a cheaper variant":    {fullAnswer(c, "claude-3-5-haiku", "Anthropic"), verifyContradicted},
		"an unknown alias":     {fullAnswer(c, "custom-mini-alias", "Anthropic"), verifyUnknown},
		"an audio variant":     {fullAnswer(c, "claude-3-5-sonnet-audio-preview", "Anthropic"), verifyUnknown},
		"an instruct variant":  {fullAnswer(c, "claude-3-5-sonnet-instruct", "Anthropic"), verifyUnknown},
		"a chat variant":       {fullAnswer(c, "claude-3-5-sonnet-chat", "Anthropic"), verifyUnknown},
		"a wrong sum":          {answerJSON(c, testModel, "Anthropic", func(o map[string]any) { o["sum"] = 8 }), verifyUnknown},
		"a wrong product":      {answerJSON(c, testModel, "Anthropic", func(o map[string]any) { o["product"] = c.product * 10 }), verifyUnknown},
		"a longer reverse":     {answerJSON(c, testModel, "Anthropic", func(o map[string]any) { o["reverse"] = c.reverse + "x" }), verifyUnknown},
		"the original string":  {answerJSON(c, testModel, "Anthropic", func(o map[string]any) { o["reverse"] = c.origin }), verifyUnknown},
		"a wrong count":        {answerJSON(c, testModel, "Anthropic", func(o map[string]any) { o["check"] = map[string]any{"ok": true, "n": c.check + 1} }), verifyUnknown},
		"no check":             {answerJSON(c, testModel, "Anthropic", func(o map[string]any) { delete(o, "check") }), verifyUnknown},
		"a cross-field answer": {answerJSON(c, testModel, "Anthropic", func(o map[string]any) { o["product"] = strconv.Itoa(c.product) + c.reverse }), verifyUnknown},
		"no nonce":             {answerJSON(c, testModel, "Anthropic", func(o map[string]any) { delete(o, "nonce") }), verifyUnknown},
		"no author":            {answerJSON(c, testModel, "Anthropic", func(o map[string]any) { delete(o, "author") }), verifyUnknown},
		"a refusal":            {"I cannot answer that.", verifyUnknown},
		"a line list":          {c.nonce + "\n42\n" + testModel + "\nAnthropic\n" + strconv.Itoa(c.product) + "\n" + c.reverse + "\n" + `{"ok":true,"n":6}`, verifyUnknown},
		"empty":                {"", verifyUnknown},
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

// TestJudgeVerificationDoesNotContradictUnknownModelLine: a size word only proves a cheaper tier when the rest of the
// model line, including its version, is the same. An unknown alias or a different version stays inconclusive and does
// not clear Verified.
func TestJudgeVerificationDoesNotContradictUnknownModelLine(t *testing.T) {
	t.Parallel()
	c := testChallenge()
	c.model = "gpt-4o"
	c.claimed.Model = c.model
	for _, model := range []string{"gpt-custom-mini", "gpt-4.1-mini"} {
		if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: fullAnswer(c, model, "Anthropic")}, c); got != verifyUnknown {
			t.Errorf("unknown model line %q judged %s, want inconclusive", model, got)
		}
	}
	if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: fullAnswer(c, "gpt-4o-mini", "Anthropic")}, c); got != verifyContradicted {
		t.Errorf("the cheaper sibling judged %s, want contradicted", got)
	}
}

// TestJudgeVerificationUsesEffectiveName: the catalog can have a stable slug unlike the name the model reports. The
// vendor-native effective name is an alternate comparison target, so an opaque catalog slug cannot block a genuine
// answer.
func TestJudgeVerificationUsesEffectiveName(t *testing.T) {
	t.Parallel()
	c := testChallenge()
	c.model = "catalog/opaque-id"
	c.name = "Vendor Model 4o"
	c.claimed.Model = c.model
	c.claimed.Name = c.name
	if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: fullAnswer(c, c.name, "Anthropic")}, c); got != verifyPassed {
		t.Errorf("a vendor-native effective name judged %s, want passed", got)
	}
}

// TestJudgeVerificationCustomCatalogName: equality is enough even when the catalog name has no family word, so a custom
// model identifier can pass without being recognized by a family heuristic.
func TestJudgeVerificationCustomCatalogName(t *testing.T) {
	t.Parallel()
	c := testChallenge()
	c.model = "acme-chat-2026-01-01"
	if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: fullAnswer(c, c.model, "Anthropic")}, c); got != verifyPassed {
		t.Errorf("a custom catalog name judged %s, want passed", got)
	}
}

// TestModelVerdict: a date snapshot is the same model; another size or tier of a recognized family is not; an alias
// that cannot be resolved is unknown rather than a fake.
func TestModelVerdict(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		answer, claim string
		want          identityVerdict
	}{
		"the dated snapshot":        {"GPT-4o 2024-08-06", "gpt-4o-2024-08-06", identityMatched},
		"the compact date":          {"gpt-4o 20240806", "gpt-4o-2024-08-06", identityMatched},
		"the provider prefix":       {"OpenAI GPT-4o", "openai/gpt-4o", identityMatched},
		"a mapped catalog name":     {"GPT-4o", "openai/gpt-4o", identityMatched},
		"a cheaper sibling":         {"gpt-4o-mini-2024-07-18", "gpt-4o-2024-08-06", identityContradicted},
		"a smaller o-series":        {"o1-mini", "o1", identityContradicted},
		"a different claude tier":   {"Claude 3.5 Haiku", "claude-3-5-sonnet-20241022", identityContradicted},
		"the same claude tier":      {"Claude 3.5 Sonnet", "claude-3-5-sonnet-20241022", identityMatched},
		"a different gemini tier":   {"Gemini 2.5 Flash", "gemini-2.5-pro", identityContradicted},
		"a different llama size":    {"Llama 3.3 8B", "llama-3.3-70b", identityContradicted},
		"a different llama version": {"Llama 3.1 8B", "llama-3.3-70b", identityUnknown},
		"a preview snapshot":        {"Gemini 2.5 Pro", "gemini-2.5-pro-preview-06-05", identityMatched},
		"a dated build":             {"DeepSeek-V3", "deepseek-v3-0324", identityMatched},
		"a three digit build":       {"Gemini 1.5 Pro", "gemini-1.5-pro-002", identityMatched},
		"a plus variant":            {"Command R+", "command-r-plus-08-2024", identityMatched},
		"the same instruct model":   {"Llama 3 70B Instruct", "llama-3-70b-instruct", identityMatched},
		"a missing instruct word":   {"Llama 3 70B", "llama-3-70b-instruct", identityUnknown},
		"an audio variant":          {"gpt-4o-audio-preview", "gpt-4o", identityUnknown},
		"an instruct variant":       {"Llama 3 70B Instruct", "llama-3-70b", identityUnknown},
		"a chat variant":            {"llama-2-70b-chat", "llama-2-70b", identityUnknown},
		"an unknown alias":          {"custom-mini-alias", "gpt-4o", identityUnknown},
		"an unknown family alias":   {"custom-gpt-mini", "gpt-4o", identityUnknown},
		"an alias with a size":      {"gpt-custom-mini", "gpt-4o", identityUnknown},
		"a version with a size":     {"gpt-4.1-mini", "gpt-4o", identityUnknown},
		"an unknown prefix":         {"acme-gpt-4o", "gpt-4o", identityUnknown},
		"an unknown size alias":     {"custom-70b", "llama-3-70b", identityUnknown},
		"a shorter alias":           {"DeepSeek-V3", "deepseek-chat", identityUnknown},
		"a different version":       {"GPT-4", "gpt-4o", identityUnknown},
		"an unrecognized suffix":    {"gpt-4o-128", "gpt-4o", identityUnknown},
		"an invalid date suffix":    {"gpt-4o-1280", "gpt-4o", identityUnknown},
		"a month-day build":         {"gpt-4o-1106", "gpt-4o", identityMatched},
		"a shorter answer":          {"GPT-4o", "gpt-4o-mini", identityUnknown},
		"an empty answer":           {"", "gpt-4o", identityUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := modelVerdict(tc.answer, tc.claim); got != tc.want {
				t.Errorf("modelVerdict(%q, %q) = %v, want %v", tc.answer, tc.claim, got, tc.want)
			}
		})
	}
}

func TestModelVerdictAny(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		answer string
		claims []string
		want   identityVerdict
	}{
		"both catalog names match":          {"GPT-4o", []string{"openai/gpt-4o", "gpt-4o"}, identityMatched},
		"one effective name matches":        {"Vendor Model 4o", []string{"catalog/opaque-id", "Vendor Model 4o"}, identityMatched},
		"every name contradicts":            {"gpt-4o-mini", []string{"gpt-4o", "gpt-4o"}, identityContradicted},
		"one contradicts, one does not":     {"gpt-4o-mini", []string{"gpt-4o", "catalog/opaque-id"}, identityUnknown},
		"a match with an unknown alternate": {"gpt-4o", []string{"gpt-4o", "gpt-4o-mini"}, identityMatched},
		"conflicting catalog names":         {"gpt-4o-mini", []string{"gpt-4o-mini", "gpt-4o"}, identityUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := modelVerdictAny(tc.answer, tc.claims...); got != tc.want {
				t.Errorf("modelVerdictAny(%q, %v) = %v, want %v", tc.answer, tc.claims, got, tc.want)
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
			c.claimed = Claimed{Author: sample.Author, Others: sample.Others, Model: sample.Model}
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

// TestJudgeVerificationWrongProbe: a correct identity with a wrong known answer is inconclusive, not a contradiction:
// an arithmetic slip must not be reported as a swapped model.
func TestJudgeVerificationWrongProbe(t *testing.T) {
	t.Parallel()
	c := testChallenge()
	for name, change := range map[string]func(map[string]any){
		"a wrong product": func(o map[string]any) { o["product"] = c.product * 10 },
		"an extra digit":  func(o map[string]any) { o["product"] = strconv.Itoa(c.product) + "0" },
		"an extra char":   func(o map[string]any) { o["reverse"] = c.reverse + "x" },
		"the original":    func(o map[string]any) { o["reverse"] = c.origin },
		"a wrong count":   func(o map[string]any) { o["check"] = map[string]any{"ok": true, "n": c.check + 1} },
		"no check":        func(o map[string]any) { delete(o, "check") },
		"a cross-field":   func(o map[string]any) { o["product"] = strconv.Itoa(c.product) + c.reverse },
		"two wrong fields": func(o map[string]any) {
			o["product"] = strconv.Itoa(c.product) + "0"
			o["reverse"] = c.reverse + "x"
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reply := answerJSON(c, testModel, "Anthropic", change)
			if got := judgeVerification(suppliers.TestResult{Streamed: true, Reply: reply}, c); got != verifyUnknown {
				t.Errorf("judged %s, want inconclusive", got)
			}
		})
	}
}
