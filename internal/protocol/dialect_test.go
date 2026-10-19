package protocol_test

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// An endpoint's dialect is what its spec says (catalog.Dialect), and a spec that says nothing has not said no. These
// tables hold each field of Target to the three answers a spec can give — yes, no, nothing — for a request that
// exercises it. Nothing is a refusal (*NotSupported) where the request needs the answer, and is written as a no where a
// yes would only add to the request (README.md「跨格式转换」).

// outcome is what a request becomes: "unsupported", or a part its body must have ("+") or must not have ("-").
type outcome string

type dialectCase struct {
	name     string
	from, to protocol.Format
	body     string
	// field is the claim the case is about; yes, no and unset are what the request becomes under each.
	field          func(*protocol.Target) *catalog.Claim
	yes, no, unset outcome
}

func (c dialectCase) run(t *testing.T) {
	t.Helper()
	for claim, want := range map[catalog.Claim]outcome{catalog.ClaimYes: c.yes, catalog.ClaimNo: c.no, catalog.ClaimUnset: c.unset} {
		t.Run(c.name+"/"+claimName(claim), func(t *testing.T) {
			checkOutcome(t, want, c.from, c.to, c.body, func(tg *protocol.Target) { *c.field(tg) = claim })
		})
	}
}

func claimName(c catalog.Claim) string { return [...]string{"unset", "no", "yes"}[c] }

// checkOutcome prepares body for an endpoint that is claudeLike but for change, and holds the result to want.
func checkOutcome(t *testing.T, want outcome, from, to protocol.Format, body string, change func(*protocol.Target)) {
	t.Helper()
	r, err := protocol.Inspect(from, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	target := claudeLike(to)
	target.MaxOutput = 4096
	change(&target)
	u, err := protocol.Prepare(&r, []byte(body), target, protocol.Rewrites{Identity: derive})
	ns, _ := errors.AsType[*protocol.NotSupported](err)
	switch {
	case want == "unsupported":
		if ns == nil {
			t.Errorf("got %s, %v; want the endpoint to refuse the request", u.Body, err)
		}
	case err != nil:
		t.Errorf("got %v, want the request prepared", err)
	case strings.HasPrefix(string(want), "+") && !strings.Contains(string(u.Body), string(want[1:])):
		t.Errorf("the request %s lacks %s", u.Body, want[1:])
	case strings.HasPrefix(string(want), "-") && strings.Contains(string(u.Body), string(want[1:])):
		t.Errorf("the request %s has %s", u.Body, want[1:])
	}
}

func TestAnUnsetClaimIsNeverAnAnswer(t *testing.T) {
	const (
		forcedTool = `,"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"required"`
		cached     = `{"role":"user","content":[{"type":"text","text":"Hi","cache_control":{"type":"ephemeral"}}]}`
		midSystem  = `[` + userHi + `,{"role":"system","content":"x"}]`
		afterTool  = `[` + userHi + `,` + callTurn + `,{"role":"system","content":"x"}]`
	)
	for _, c := range []dialectCase{
		{
			"a system message mid-conversation", protocol.Chat, protocol.Messages, chatBody(`"messages":` + midSystem),
			func(t *protocol.Target) *catalog.Claim { return &t.MidSystem },
			"+" + `"role":"system"`, "unsupported", "unsupported",
		},
		{
			"a system message after tool results", protocol.Chat, protocol.Messages, chatBody(`"messages":` + afterTool),
			func(t *protocol.Target) *catalog.Claim { return &t.UntrustedSystemAfterTools },
			"unsupported", "+" + `"role":"system"`, "unsupported",
		},
		{
			"a temperature", protocol.Chat, protocol.Messages, chatBody(`"messages":[` + userHi + `],"temperature":0.5`),
			func(t *protocol.Target) *catalog.Claim { return &t.DefaultSamplingOnly },
			"unsupported", "+" + `"temperature":0.5`, "unsupported",
		},
		{
			"a forced tool", protocol.Chat, protocol.Messages, chatBody(`"messages":[` + userHi + `]` + forcedTool),
			func(t *protocol.Target) *catalog.Claim { return &t.ForcedToolChoice },
			"+" + `"type":"any"`, "unsupported", "unsupported",
		},
		{
			"no sampling at all", protocol.Chat, protocol.Messages, chatBody(`"messages":[` + userHi + `]`),
			func(t *protocol.Target) *catalog.Claim { return &t.DefaultSamplingOnly },
			"+" + `"max_tokens"`, "+" + `"max_tokens"`, "+" + `"max_tokens"`,
		},
		{
			"a cache breakpoint", protocol.Chat, protocol.Responses, chatBody(`"messages":[` + cached + `]`),
			func(t *protocol.Target) *catalog.Claim { return &t.CacheBreakpoints },
			"+" + `"prompt_cache_breakpoint"`, "-" + `"prompt_cache_breakpoint"`, "-" + `"prompt_cache_breakpoint"`,
		},
		{
			"encrypted reasoning, converted", protocol.Chat, protocol.Responses, chatBody(`"messages":[` + userHi + `]`),
			func(t *protocol.Target) *catalog.Claim { return &t.EncryptedReasoning },
			"+" + `"reasoning.encrypted_content"`, "-" + `"reasoning.encrypted_content"`, "-" + `"reasoning.encrypted_content"`,
		},
		{
			"encrypted reasoning of a Responses call that stores nothing", protocol.Responses, protocol.Responses,
			`{"model":"m","input":"Hi","store":false}`,
			func(t *protocol.Target) *catalog.Claim { return &t.EncryptedReasoning },
			"+" + `"reasoning.encrypted_content"`, "-" + `"reasoning.encrypted_content"`, "-" + `"reasoning.encrypted_content"`,
		},
	} {
		c.run(t)
	}
}

// TestThinkingIsAsTheSpecSaysOrNotAtAll: a request that asks for reasoning is written the way the spec says the model
// is asked, and when the spec says none or nothing there is no way to ask. A model that thinks with a budget is never
// sent the adaptive form that the zero value used to stand for.
func TestThinkingIsAsTheSpecSaysOrNotAtAll(t *testing.T) {
	asks := chatBody(`"messages":[` + userHi + `],"max_tokens":10000,"reasoning":{"effort":"high"}`)
	asksNot := chatBody(`"messages":[` + userHi + `]`)
	for _, c := range []struct {
		name     string
		thinking catalog.Thinking
		body     string
		want     []outcome
	}{
		{"adaptive", catalog.ThinkingAdaptive, asks, []outcome{`+"thinking":{"type":"adaptive"}`, `-budget_tokens`}},
		{"manual", catalog.ThinkingManual, asks, []outcome{`+"thinking":{"type":"enabled","budget_tokens":8000}`, `-adaptive`}},
		{"none", catalog.ThinkingNone, asks, []outcome{"unsupported"}},
		{"unset", catalog.ThinkingUnset, asks, []outcome{"unsupported"}},
		// A request that asks for no reasoning needs no answer, so a spec that says nothing does not stand in its way.
		{"unset, none asked", catalog.ThinkingUnset, asksNot, []outcome{`-thinking`}},
		{"none, none asked", catalog.ThinkingNone, asksNot, []outcome{`-thinking`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, want := range c.want {
				checkOutcome(t, want, protocol.Chat, protocol.Messages, c.body, func(tg *protocol.Target) { tg.Thinking = c.thinking })
			}
		})
	}
}

// TestAZeroTargetAnswersNothing: a Target nobody filled in is not a model that takes the defaults: a request that needs
// an answer of its spec is refused, whichever it is.
func TestAZeroTargetAnswersNothing(t *testing.T) {
	for name, body := range map[string]string{
		"reasoning":  chatBody(`"messages":[` + userHi + `],"max_tokens":10000,"reasoning":{"effort":"high"}`),
		"mid-system": chatBody(`"messages":[` + userHi + `,{"role":"system","content":"x"}]`),
		"sampling":   chatBody(`"messages":[` + userHi + `],"top_p":0.5`),
	} {
		t.Run(name, func(t *testing.T) {
			checkOutcome(t, "unsupported", protocol.Chat, protocol.Messages, body, func(tg *protocol.Target) {
				*tg = protocol.Target{Format: protocol.Messages, Model: "up", MaxOutput: 4096}
			})
		})
	}
}

// TestSamplingAndForcedToolAreDecidedApart: a model that refuses a temperature other than the default (Claude Haiku
// 5.5) can still take a forced tool, and one that refuses a forced tool (Claude Opus 5.5) is no judge of the sampling.
func TestSamplingAndForcedToolAreDecidedApart(t *testing.T) {
	forced := chatBody(`"messages":[` + userHi + `],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"required"`)
	warm := chatBody(`"messages":[` + userHi + `],"temperature":0.5`)
	checkOutcome(t, "+"+`"type":"any"`, protocol.Chat, protocol.Messages, forced, func(tg *protocol.Target) {
		tg.DefaultSamplingOnly = catalog.ClaimYes
	})
	checkOutcome(t, "+"+`"temperature":0.5`, protocol.Chat, protocol.Messages, warm, func(tg *protocol.Target) {
		tg.ForcedToolChoice = catalog.ClaimNo
	})
}

// refusalOf is what an endpoint that says nothing of its model (but for change) answers to a Chat request.
func refusalOf(t *testing.T, body string, change func(*protocol.Target)) *protocol.NotSupported {
	t.Helper()
	r, err := protocol.Inspect(protocol.Chat, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	target := protocol.Target{Format: protocol.Messages, Model: "up", MaxOutput: 4096}
	change(&target)
	_, err = protocol.Prepare(&r, []byte(body), target, protocol.Rewrites{Identity: derive})
	ns, _ := errors.AsType[*protocol.NotSupported](err)
	if ns == nil {
		t.Fatalf("got %v, want a refusal", err)
	}
	return ns
}

// TestAnUnsetClaimIsTheOperatorsToKnow: the reason a refusal gives is what the client may be told, and says nothing of
// the spec; which key is missing is in Detail, which only the log gets.
func TestAnUnsetClaimIsTheOperatorsToKnow(t *testing.T) {
	nothing := func(*protocol.Target) {}
	for name, c := range map[string]struct {
		body   string
		change func(*protocol.Target)
	}{
		"reasoning":  {chatBody(`"messages":[` + userHi + `],"max_tokens":10000,"reasoning":{"effort":"high"}`), nothing},
		"mid-system": {chatBody(`"messages":[` + userHi + `,{"role":"system","content":"x"}]`), nothing},
		"after tool": {chatBody(`"messages":[` + userHi + `,` + callTurn + `,{"role":"system","content":"x"}]`), func(tg *protocol.Target) { tg.MidSystem = catalog.ClaimYes }},
		"sampling":   {chatBody(`"messages":[` + userHi + `],"top_p":0.5`), nothing},
		"forced":     {chatBody(`"messages":[` + userHi + `],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"required"`), nothing},
	} {
		t.Run(name, func(t *testing.T) {
			ns := refusalOf(t, c.body, c.change)
			if !strings.HasSuffix(ns.Reason, " yet") || strings.Contains(ns.Reason, "spec") {
				t.Errorf("Reason %q is not for the client", ns.Reason)
			}
			if !strings.Contains(ns.Detail, "the spec does not say") {
				t.Errorf("Detail %q does not name the missing key", ns.Detail)
			}
		})
	}
}
