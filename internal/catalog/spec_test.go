package catalog

import (
	"strings"
	"testing"
)

// servingCase is one spec, and what Serving() makes of it: the whole of it when every key is there, and nothing when one
// is not, unless the parse refuses the spec first.
type servingCase struct {
	name string
	spec string
	want Serving
	ok   bool
	err  string
}

var servingCases = []servingCase{
	{
		name: "every key",
		spec: `{"items":["input","output"],"tiers":[],"context_length":200000,"max_output_tokens":64000,
			"streams":true,"abilities":["tools","images"]}`,
		want: Serving{Context: 200000, MaxOutput: 64000, Streams: true, Abilities: AbilitySet(1<<AbilityTools | 1<<AbilityImages)},
		ok:   true,
	},
	{name: "no context_length", spec: `{"items":[],"tiers":[],"max_output_tokens":64000,"streams":true,"abilities":[]}`},
	{name: "no max_output_tokens", spec: `{"items":[],"tiers":[],"context_length":200000,"streams":true,"abilities":[]}`},
	{name: "no streams", spec: `{"items":[],"tiers":[],"context_length":200000,"max_output_tokens":64000,"abilities":[]}`},
	{name: "no abilities", spec: `{"items":[],"tiers":[],"context_length":200000,"max_output_tokens":64000,"streams":true}`},
	{name: "null declares nothing", spec: `{"context_length":null,"max_output_tokens":64000,"streams":true,"abilities":[]}`},
	// An ability the catalog does not know is refused here rather than silently left out: the endpoint would otherwise
	// serve requests that need it and answer wrong.
	{
		name: "unknown ability",
		spec: `{"context_length":1,"max_output_tokens":1,"streams":true,"abilities":["tools","telepathy"]}`,
		err:  `unknown name "telepathy"`,
	},
	{
		name: "ability twice",
		spec: `{"context_length":1,"max_output_tokens":1,"streams":true,"abilities":["tools","tools"]}`,
		err:  `"tools" is listed twice`,
	},
	{
		name: "context_length of 0",
		spec: `{"context_length":0,"max_output_tokens":1,"streams":true,"abilities":[]}`,
		err:  "context_length must be above 0",
	},
	{
		name: "max_output_tokens of 0",
		spec: `{"context_length":1,"max_output_tokens":0,"streams":true,"abilities":[]}`,
		err:  "max_output_tokens must be above 0",
	},
}

// TestServing: what an endpoint of a model offers is read from the spec's own keys, and a spec that leaves one of them
// out says nothing about it: the endpoint is then not routable, rather than routable with a guessed limit
// (docs/domains/routing.md「一个请求的路由步骤」).
func TestServing(t *testing.T) {
	t.Parallel()
	for _, tc := range servingCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkServing(t, tc)
		})
	}
}

func checkServing(t *testing.T, tc servingCase) {
	t.Helper()
	s, err := parseSpec([]byte(tc.spec))
	if tc.err != "" {
		if err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Fatalf("parseSpec error = %v, want one with %q", err, tc.err)
		}
		return
	}
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	got, ok := Model{serving: s.serving, serves: s.serves}.Serving()
	if ok != tc.ok || got != tc.want {
		t.Errorf("Serving() = %+v, %t, want %+v, %t", got, ok, tc.want, tc.ok)
	}
}

// TestParseAbility: the spec's names are the abilities' own, and what they stand for writes back the same.
func TestParseAbility(t *testing.T) {
	t.Parallel()
	for _, a := range []Ability{
		AbilityTools, AbilityImages, AbilityDocuments, AbilityReasoning, AbilityPromptCache,
		AbilityStructuredOutput, AbilityWebSearch, AbilityCodeExecution,
	} {
		got, err := ParseAbility(a.String())
		if err != nil || got != a {
			t.Errorf("ParseAbility(%q) = %v, %v, want %v", a.String(), got, err, a)
		}
	}
	if _, err := ParseAbility("telepathy"); err == nil {
		t.Error("ParseAbility(telepathy) took a name the catalog does not have")
	}
}

// TestDialect: each key of how requests to the model are written is read on its own, and a key the spec leaves out (or
// sets to null) is unset, which is not a no: absent and false are different claims. A name the catalog does not know,
// or a value of the wrong type, is refused when the spec is saved, not ignored.
func TestDialect(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		spec string
		want Dialect
		err  string
	}{
		{name: "nothing said", spec: `{}`},
		{name: "null says nothing", spec: `{"thinking":null,"mid_system":null,"encrypted_reasoning":null}`},
		{
			name: "every key, yes",
			spec: `{"thinking":"adaptive","encrypted_reasoning":true,"mid_system":true,"untrusted_system_after_tools":true,
				"default_sampling_only":true,"forced_tool_choice":true,"cache_breakpoints":true}`,
			want: Dialect{
				Thinking: ThinkingAdaptive, EncryptedReasoning: ClaimYes, MidSystem: ClaimYes, UntrustedSystemAfterTools: ClaimYes,
				DefaultSamplingOnly: ClaimYes, ForcedToolChoice: ClaimYes, CacheBreakpoints: ClaimYes,
			},
		},
		{
			name: "every key, no",
			spec: `{"thinking":"none","encrypted_reasoning":false,"mid_system":false,"untrusted_system_after_tools":false,
				"default_sampling_only":false,"forced_tool_choice":false,"cache_breakpoints":false}`,
			want: Dialect{
				Thinking: ThinkingNone, EncryptedReasoning: ClaimNo, MidSystem: ClaimNo, UntrustedSystemAfterTools: ClaimNo,
				DefaultSamplingOnly: ClaimNo, ForcedToolChoice: ClaimNo, CacheBreakpoints: ClaimNo,
			},
		},
		{name: "manual thinking alone", spec: `{"thinking":"manual"}`, want: Dialect{Thinking: ThinkingManual}},
		{name: "one no among the unset", spec: `{"mid_system":false}`, want: Dialect{MidSystem: ClaimNo}},
		{name: "unknown thinking", spec: `{"thinking":"telepathy"}`, err: `unknown name "telepathy"`},
		{name: "empty thinking", spec: `{"thinking":""}`, err: `unknown name ""`},
		{name: "a claim that is no bool", spec: `{"mid_system":"yes"}`, err: "spec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := parseSpec([]byte(tc.spec))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("parseSpec error = %v, want one with %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSpec: %v", err)
			}
			if got := (Model{dialect: s.dialect}).Dialect(); got != tc.want {
				t.Errorf("Dialect() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
