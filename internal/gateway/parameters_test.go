package gateway

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// TestParameterSupport: the list provider.require_parameters enforces is the one the model list publishes, derived
// from the spec's abilities and dialect: default sampling takes temperature and top_p away, and so does no reasoning.
func TestParameterSupport(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		serving catalog.Serving
		dialect catalog.Dialect
		want    []protocol.Parameter
	}{
		"sampling allowed":           {catalog.Serving{}, catalog.Dialect{}, []protocol.Parameter{protocol.ParamTemperature, protocol.ParamTopP, protocol.ParamMaxTokens, protocol.ParamStop}},
		"default sampling only":      {catalog.Serving{}, catalog.Dialect{DefaultSamplingOnly: catalog.ClaimYes}, []protocol.Parameter{protocol.ParamMaxTokens, protocol.ParamStop}},
		"tools and structured reply": {catalog.Serving{Abilities: catalog.AbilitySet(1<<catalog.AbilityTools | 1<<catalog.AbilityStructuredOutput)}, catalog.Dialect{}, []protocol.Parameter{protocol.ParamTemperature, protocol.ParamTopP, protocol.ParamMaxTokens, protocol.ParamResponseFormat, protocol.ParamStop, protocol.ParamTools, protocol.ParamToolChoice, protocol.ParamParallelToolCalls}},
		"reasoning":                  {catalog.Serving{Abilities: catalog.AbilitySet(1 << catalog.AbilityReasoning)}, catalog.Dialect{Thinking: catalog.ThinkingManual}, []protocol.Parameter{protocol.ParamTemperature, protocol.ParamTopP, protocol.ParamMaxTokens, protocol.ParamStop, protocol.ParamReasoning, protocol.ParamReasoningEffort}},
		"thinking none":              {catalog.Serving{Abilities: catalog.AbilitySet(1 << catalog.AbilityReasoning)}, catalog.Dialect{Thinking: catalog.ThinkingNone}, []protocol.Parameter{protocol.ParamTemperature, protocol.ParamTopP, protocol.ParamMaxTokens, protocol.ParamStop}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var want protocol.ParameterSet
			for _, p := range tc.want {
				want.Add(p)
			}
			if diff := cmp.Diff(want, parameterSupport(tc.serving, tc.dialect)); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}
