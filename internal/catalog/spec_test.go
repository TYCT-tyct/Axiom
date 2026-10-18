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
