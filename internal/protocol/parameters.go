package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: the tokenizer reads names without building a tree
	"strings"
)

// Parameter is a request parameter whose support an endpoint may declare. The parameters are the ones the model list
// publishes as supported_parameters, so provider.require_parameters and the model list cannot disagree about what an
// endpoint takes. Parameters outside them (seed, logprobs, a provider's own) are not declared by the catalog.
type Parameter uint8

// The declared parameters, in the order the model list writes them.
const (
	ParamTemperature Parameter = iota
	ParamTopP
	ParamMaxTokens // max_tokens, max_completion_tokens and Responses' max_output_tokens are one support
	ParamResponseFormat
	ParamStop
	ParamTools
	ParamToolChoice
	ParamParallelToolCalls
	ParamReasoning
	ParamReasoningEffort
	NumParameters
)

// A seventeenth parameter would have no bit: Has would report it absent and Add would drop it. This fails to compile
// first.
var _ [16 - NumParameters]struct{}

// ParameterSet is a set of parameters.
type ParameterSet uint16

// Has reports whether s holds p.
func (s ParameterSet) Has(p Parameter) bool { return s&(1<<p) != 0 }

// Add puts p in the set.
func (s *ParameterSet) Add(p Parameter) { *s |= 1 << p }

// HasAll reports whether s holds every parameter in other.
func (s ParameterSet) HasAll(other ParameterSet) bool { return s&other == other }

// parameterNames are the request members whose support the parameter list alone decides: sampling, the output cap
// and stopping. A format's own spelling maps to the same bit; the output cap has three names, stopping has two. The
// capability parameters (tools, a reply's format, reasoning) are left to Request.Needs: it already filters endpoints
// whose spec lacks them, and it reads an empty tools array as no tools, which a name alone cannot.
var parameterNames = map[string]Parameter{
	"temperature": ParamTemperature, "top_p": ParamTopP,
	"max_tokens": ParamMaxTokens, "max_completion_tokens": ParamMaxTokens, "max_output_tokens": ParamMaxTokens,
	"stop": ParamStop, "stop_sequences": ParamStop,
}

// requestControls are the top-level members that are not model parameters: the formats' structure, the routing and
// accounting fields the gateway consumes, and the vendor configurations it turns into capabilities (tools, reasoning,
// service tiers). They ask nothing of the endpoint's parameter list.
var requestControls = map[string]struct{}{
	"model": {}, "models": {}, "fallbacks": {}, "messages": {}, "system": {}, "input": {}, "contents": {},
	"instructions": {}, "conversation": {}, "prompt": {},
	"stream": {}, "stream_options": {}, "provider": {}, "preset": {}, "session_id": {}, "plugins": {}, "transforms": {},
	"route": {}, "usage": {}, "axiom": {}, "prompt_cache_key": {}, "user": {}, "metadata": {}, "store": {},
	"safety_identifier": {}, "cache_control": {}, "context_management": {}, "mcp_servers": {}, "speed": {},
	"text": {}, "previous_response_id": {}, "include": {}, "truncation": {}, "background": {},
	"service_tier": {}, "max_tool_calls": {}, "web_search_options": {}, "n": {},
	"thinking": {}, "output_config": {},
	"response_format": {}, "output_format": {}, "tools": {}, "functions": {}, "tool_choice": {}, "function_call": {},
	"parallel_tool_calls": {}, "reasoning": {}, "reasoning_effort": {},
}

// RequestParameters reports the sampling, output-cap and stopping parameters a request sets, and the first top-level
// member that is neither one of them nor a request control: that member's support is not in the catalog. Capability
// parameters are Request.Needs' to enforce. A null member is absent, as everywhere else. The third result says an
// undeclared member was found: an empty member name is itself undeclared, so an empty unknown string cannot stand for
// success. It is a second pass over a body Inspect has already validated, so only a request that asks for
// provider.require_parameters pays for it.
//
// Responses carries verbosity inside its text object, which Inspect reads only for format. The scan reads that one
// nested parameter; text.format stays a capability, enforced by Request.Needs.
func RequestParameters(f Format, body []byte) (set ParameterSet, unknown string, found bool) {
	s := scanner{dec: jsontext.NewDecoder(bytes.NewBuffer(body))}
	if s.dec.PeekKind() != '{' {
		return set, "", false
	}
	if err := s.token(); err != nil {
		return set, "", false
	}
	for s.dec.PeekKind() != '}' {
		if err := s.member(); err != nil {
			return set, "", false
		}
		present := s.dec.PeekKind() != 'n'
		if present && f == Responses && string(s.name) == "text" {
			if verbosity, err := s.textVerbosity(); err != nil {
				return set, "", false
			} else if verbosity {
				return set, "verbosity", true
			}
			continue // textVerbosity consumed the value
		}
		if p, ok := parameterNames[string(s.name)]; ok { // a map lookup with this conversion does not allocate
			if present {
				set.Add(p)
			}
		} else if _, ok := requestControls[string(s.name)]; !ok && present {
			return set, parameterName(string(s.name)), true
		}
		if err := s.skip(); err != nil {
			return set, "", false
		}
	}
	return set, "", false
}

// textVerbosity reads a Responses text object for verbosity, the one model parameter it carries that Inspect does not
// read. It consumes the whole value, including its closing brace.
func (s *scanner) textVerbosity() (found bool, err error) {
	err = s.eachMember(func() error {
		if string(s.name) == "verbosity" && s.dec.PeekKind() != 'n' {
			found = true
		}
		return s.skip()
	})
	return found, err
}

// parameterName bounds what an error message repeats of a name the client chose.
func parameterName(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	return strings.ToValidUTF8(name[:limit], "?") + "…"
}
