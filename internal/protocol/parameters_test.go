package protocol_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/protocol"
)

// TestRequestParameters: provider.require_parameters reads the parameters a request actually uses, including the
// capability parameters, treats a null as absent, reads an empty tools array as no tools, and names the first member
// whose support the catalog does not declare. Responses' text verbosity and format, and Messages' output_config, are
// read because Inspect reads them for the request but not for the parameter inventory.
func TestRequestParameters(t *testing.T) {
	t.Parallel()
	set := func(params ...protocol.Parameter) (s protocol.ParameterSet) {
		for _, p := range params {
			s = s.With(p)
		}
		return s
	}
	for name, tc := range map[string]struct {
		format  protocol.Format
		body    string
		want    protocol.ParameterSet
		unknown string
		found   bool
	}{
		"nothing":                       {protocol.Chat, `{"model":"m","messages":[]}`, 0, "", false},
		"a parameter":                   {protocol.Chat, `{"temperature":0.5}`, set(protocol.ParamTemperature), "", false},
		"both sampling":                 {protocol.Chat, `{"temperature":0.5,"top_p":0.9}`, set(protocol.ParamTemperature, protocol.ParamTopP), "", false},
		"an output cap":                 {protocol.Chat, `{"max_output_tokens":10}`, set(protocol.ParamMaxTokens), "", false},
		"an alias":                      {protocol.Chat, `{"stop_sequences":["END"]}`, set(protocol.ParamStop), "", false},
		"a null is absent":              {protocol.Chat, `{"temperature":null}`, 0, "", false},
		"a control":                     {protocol.Chat, `{"metadata":{"user_id":"u"},"stream":true}`, 0, "", false},
		"a nested parameter":            {protocol.Chat, `{"messages":[{"temperature":1}]}`, 0, "", false},
		"empty tools":                   {protocol.Chat, `{"tools":[]}`, 0, "", false},
		"null capabilities":             {protocol.Chat, `{"tools":null,"tool_choice":null,"parallel_tool_calls":null,"response_format":null,"reasoning":null,"reasoning_effort":null}`, 0, "", false},
		"tool parameters":               {protocol.Chat, `{"tools":[{}],"tool_choice":"auto","parallel_tool_calls":false}`, set(protocol.ParamTools, protocol.ParamToolChoice, protocol.ParamParallelToolCalls), "", false},
		"legacy tools":                  {protocol.Chat, `{"functions":[{}],"function_call":"auto"}`, set(protocol.ParamTools, protocol.ParamToolChoice), "", false},
		"reply format":                  {protocol.Chat, `{"response_format":{"type":"json_object"}}`, set(protocol.ParamResponseFormat), "", false},
		"reasoning":                     {protocol.Chat, `{"reasoning":{"effort":"low"}}`, set(protocol.ParamReasoning, protocol.ParamReasoningEffort), "", false},
		"reasoning without effort":      {protocol.Chat, `{"reasoning":{"enabled":true}}`, set(protocol.ParamReasoning), "", false},
		"reasoning effort off":          {protocol.Chat, `{"reasoning":{"effort":"none"}}`, set(protocol.ParamReasoning), "", false},
		"an empty reasoning effort":     {protocol.Chat, `{"reasoning_effort":""}`, 0, "", false},
		"a reasoning effort":            {protocol.Chat, `{"reasoning_effort":"high"}`, set(protocol.ParamReasoningEffort), "", false},
		"messages output config":        {protocol.Messages, `{"output_config":{"format":{"type":"json_schema"},"effort":"high"}}`, set(protocol.ParamResponseFormat, protocol.ParamReasoningEffort), "", false},
		"a null unknown":                {protocol.Chat, `{"seed":null}`, 0, "", false},
		"an unknown":                    {protocol.Chat, `{"seed":7}`, 0, "seed", true},
		"the first unknown":             {protocol.Chat, `{"seed":7,"logprobs":true}`, 0, "seed", true},
		"chat verbosity":                {protocol.Chat, `{"model":"m","messages":[],"verbosity":"high"}`, 0, "verbosity", true},
		"messages verbosity":            {protocol.Messages, `{"model":"m","messages":[],"verbosity":"high"}`, 0, "verbosity", true},
		"responses text verbosity":      {protocol.Responses, `{"model":"m","input":"hi","text":{"verbosity":"high"}}`, 0, "verbosity", true},
		"responses text verbosity null": {protocol.Responses, `{"model":"m","input":"hi","text":{"verbosity":null}}`, 0, "", false},
		"responses text format":         {protocol.Responses, `{"model":"m","input":"hi","text":{"format":{"type":"json_object"}}}`, set(protocol.ParamResponseFormat), "", false},
		"embeddings structure":          {protocol.Embeddings, `{"model":"m","input":["a","b"],"provider":{"require_parameters":true}}`, 0, "", false},
		"rerank structure":              {protocol.Rerank, `{"model":"m","query":"q","documents":["d"],"provider":{"require_parameters":true}}`, 0, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, unknown, found := protocol.RequestParameters(tc.format, []byte(tc.body))
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parameters (-want +got):\n%s", diff)
			}
			if unknown != tc.unknown || found != tc.found {
				t.Errorf("unknown %q, found %t, want %q, %t", unknown, found, tc.unknown, tc.found)
			}
		})
	}
}

// TestRequestParametersEmptyName: an empty member name is a valid JSON name and an undeclared parameter, not the
// absence of one. It must be reported before a later temperature or seed is read, so the caller refuses the request.
func TestRequestParametersEmptyName(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"before a parameter": `{"":1,"temperature":0.5}`,
		"before an unknown":  `{"":1,"seed":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, unknown, found := protocol.RequestParameters(protocol.Chat, []byte(body))
			if !found || unknown != "" {
				t.Errorf("unknown %q, found %t, want an empty name found", unknown, found)
			}
		})
	}
}

// TestRequestParametersBoundsAName: a refused name is the client's and may be long; the message repeats at most 64
// bytes of it, ending mid-rune safely.
func TestRequestParametersBoundsAName(t *testing.T) {
	t.Parallel()
	long := make([]byte, 80)
	for i := range long {
		long[i] = 'a'
	}
	for name, text := range map[string]string{
		"ascii":    string(long),
		"mid-rune": strings.Repeat("é", 33), // 66 bytes; the 64-byte cut splits the last one
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, unknown, found := protocol.RequestParameters(protocol.Chat, []byte(`{"`+text+`":true}`))
			if !found || len(unknown) > 64+len("…") || unknown[len(unknown)-len("…"):] != "…" || !utf8.ValidString(unknown) {
				t.Errorf("unknown %q, found %t, want at most 64 valid bytes and an ellipsis", unknown, found)
			}
		})
	}
}
