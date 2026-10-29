package gateway

import (
	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// parameterSupport is the parameters an endpoint declares it takes, from the model's spec and dialect: the same set
// the model list publishes as supported_parameters, so provider.require_parameters and discovery cannot disagree. A
// spec that says nothing about sampling takes temperature and top_p, as the model list claims; a spec without the
// reasoning ability, or with thinking "none", does not take reasoning.
func parameterSupport(s catalog.Serving, d catalog.Dialect) (p protocol.ParameterSet) {
	if d.DefaultSamplingOnly != catalog.ClaimYes {
		p = p.With(protocol.ParamTemperature)
		p = p.With(protocol.ParamTopP)
	}
	p = p.With(protocol.ParamMaxTokens)
	p = p.With(protocol.ParamStop)
	if s.Abilities.Has(catalog.AbilityStructuredOutput) {
		p = p.With(protocol.ParamResponseFormat)
	}
	if s.Abilities.Has(catalog.AbilityTools) {
		p = p.With(protocol.ParamTools)
		p = p.With(protocol.ParamToolChoice)
		p = p.With(protocol.ParamParallelToolCalls)
	}
	if reasons(d, s.Abilities) {
		p = p.With(protocol.ParamReasoning)
		p = p.With(protocol.ParamReasoningEffort)
	}
	return p
}

// orParameters is parameterSupport in OpenRouter's names and order (spec `Parameter`): the sampling and output
// controls every model here takes, then the ones its abilities claim. A parameter the gateway would pass through but
// the catalog says nothing about for this model (logprobs, seed, verbosity, …) is not claimed: a client reads the list
// to decide what it may send.
func orParameters(p protocol.ParameterSet) []string {
	var names []string
	if p.Has(protocol.ParamTemperature) {
		names = append(names, "temperature", "top_p")
	}
	if p.Has(protocol.ParamMaxTokens) {
		names = append(names, "max_tokens", "max_completion_tokens")
	}
	if p.Has(protocol.ParamResponseFormat) {
		names = append(names, "response_format")
	}
	if p.Has(protocol.ParamStop) {
		names = append(names, "stop")
	}
	if p.Has(protocol.ParamTools) {
		names = append(names, "tools", "tool_choice", "parallel_tool_calls")
	}
	if p.Has(protocol.ParamReasoning) {
		names = append(names, "reasoning", "reasoning_effort")
	}
	return names
}
