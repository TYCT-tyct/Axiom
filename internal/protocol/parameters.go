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

// A seventeenth parameter would have no bit: Has would report it absent and With would drop it. This fails to compile
// first.
var _ [16 - NumParameters]struct{}

// ParameterSet is a set of parameters.
type ParameterSet uint16

// Has reports whether s holds p.
func (s ParameterSet) Has(p Parameter) bool { return s&(1<<p) != 0 }

// With returns the set with p added.
func (s ParameterSet) With(p Parameter) ParameterSet { return s | 1<<p }

// HasAll reports whether s holds every parameter in other.
func (s ParameterSet) HasAll(other ParameterSet) bool { return s&other == other }

// parameterNames are the request members whose support the parameter list alone decides: sampling, the output cap and
// stopping. A format's own spelling maps to the same bit; the output cap has three names, stopping has two. Capability
// parameters are read by requestParameter itself, which can see an empty tools array and a null value.
var parameterNames = map[string]Parameter{
	"temperature": ParamTemperature, "top_p": ParamTopP,
	"max_tokens": ParamMaxTokens, "max_completion_tokens": ParamMaxTokens, "max_output_tokens": ParamMaxTokens,
	"stop": ParamStop, "stop_sequences": ParamStop,
}

// requestControls are the top-level members that are not model parameters and are not read as capability parameters:
// the formats' structure, the routing and accounting fields the gateway consumes, and vendor configuration the gateway
// turns into capabilities. They ask nothing of the endpoint's parameter list.
var requestControls = map[string]struct{}{
	"model": {}, "models": {}, "fallbacks": {}, "messages": {}, "system": {}, "input": {}, "contents": {},
	"instructions": {}, "conversation": {}, "prompt": {}, "query": {}, "documents": {},
	"stream": {}, "stream_options": {}, "provider": {}, "preset": {}, "session_id": {}, "plugins": {}, "transforms": {},
	"route": {}, "usage": {}, "axiom": {}, "prompt_cache_key": {}, "user": {}, "metadata": {}, "store": {},
	"safety_identifier": {}, "cache_control": {}, "context_management": {}, "mcp_servers": {}, "speed": {},
	"text": {}, "previous_response_id": {}, "include": {}, "truncation": {}, "background": {},
	"service_tier": {}, "max_tool_calls": {}, "web_search_options": {}, "n": {},
	"thinking": {}, "output_config": {},
	"response_format": {}, "output_format": {}, "tools": {}, "functions": {}, "tool_choice": {}, "function_call": {},
	"parallel_tool_calls": {}, "reasoning": {}, "reasoning_effort": {},
}

// RequestParameters reports the parameters a request actually uses: sampling, output-cap and stopping controls, and the
// capability parameters tools, reply format and reasoning. It also returns the first top-level member that is neither a
// parameter nor a request control: that member's support is not in the catalog. A null member is absent, and an empty
// tools or functions array asks for no tools, as everywhere else. The third result says an undeclared member was found:
// an empty member name is itself undeclared, so an empty unknown string cannot stand for success. It is a second pass
// over a body Inspect has already validated, so only a request that asks for provider.require_parameters pays for it.
//
// Responses carries verbosity and format inside its text object, and Messages carries format and effort inside
// output_config; Inspect reads those for the request but not for the parameter inventory, so the scan reads them here.
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
		member, miss, err := s.requestParameter(f, &set)
		if err != nil {
			return set, "", false
		}
		if miss {
			return set, member, true
		}
	}
	return set, "", false
}

// requestParameter reads one top-level member. A sampling, output-cap or stopping parameter the catalog declares joins
// set, as does a capability parameter the request actually uses; a Responses text object is read for verbosity, which
// is not declared; any other member neither declared nor a request control stops the scan. It consumes the member's
// value in every case.
func (s *scanner) requestParameter(f Format, set *ParameterSet) (string, bool, error) {
	present := s.dec.PeekKind() != 'n'
	if member, miss, handled, err := s.capabilityParameter(f, set, present); handled {
		return member, miss, err
	}
	if p, ok := parameterNames[string(s.name)]; ok { // a map lookup with this conversion does not allocate
		if present {
			*set = set.With(p)
		}
	} else if _, ok := requestControls[string(s.name)]; !ok && present {
		return parameterName(string(s.name)), true, nil
	}
	return "", false, s.skip()
}

// capabilityParameters are the top-level capability parameters that only their presence matters to the inventory. The
// tool array needs a value check and reasoning and effort need a nested read, so those are in capabilityParameter.
var capabilityParameters = map[string]Parameter{
	"tool_choice": ParamToolChoice, "function_call": ParamToolChoice,
	"parallel_tool_calls": ParamParallelToolCalls,
	"response_format":     ParamResponseFormat, "output_format": ParamResponseFormat,
}

// capabilityParameter reads the capability parameters. handled reports that the member was one of them and its value
// has been consumed.
func (s *scanner) capabilityParameter(f Format, set *ParameterSet, present bool) (member string, miss, handled bool, err error) {
	if member, miss, handled, err := s.nestedParameter(f, set, present); handled {
		return member, miss, true, err
	}
	name := string(s.name)
	if p, ok := capabilityParameters[name]; ok {
		if present {
			*set = set.With(p)
		}
		return "", false, true, s.skip()
	}
	switch name {
	case "tools", "functions":
		used, err := s.nonEmptyArray()
		if used {
			*set = set.With(ParamTools)
		}
		return "", false, true, err
	case "reasoning":
		if present {
			*set = set.With(ParamReasoning)
		}
		return "", false, true, s.reasoningParameters(set)
	case "reasoning_effort":
		return "", false, true, s.effort(set)
	}
	return "", false, false, nil
}

// nestedParameter reads Responses' text and Messages' output_config, the two objects Inspect reads for the request but
// not for the parameter inventory. It consumes the whole value when handled.
func (s *scanner) nestedParameter(f Format, set *ParameterSet, present bool) (member string, miss, handled bool, err error) {
	name := string(s.name)
	if present && f == Responses && name == "text" {
		verbosity, err := s.textParameters(set)
		if err != nil {
			return "", false, true, err
		}
		if verbosity {
			return "verbosity", true, true, nil
		}
		return "", false, true, nil
	}
	if present && name == "output_config" {
		return "", false, true, s.outputConfig(set)
	}
	return "", false, false, nil
}

// nonEmptyArray consumes an array or null and reports whether the array had an element. An empty tools or functions
// array is the request's existing way of saying it has no tools, so it asks for no tool parameter.
func (s *scanner) nonEmptyArray() (used bool, err error) {
	err = s.eachElement(func() error {
		used = true
		return s.skip()
	})
	return used, err
}

// reasoningParameters reads the reasoning object for the effort it may carry. It consumes the whole value, including
// its closing brace, and adds ParamReasoningEffort only when the effort is a setting: an empty string or "none" is the
// request saying it does not use reasoning effort, as Inspect reads it.
func (s *scanner) reasoningParameters(set *ParameterSet) error {
	if s.dec.PeekKind() == 'n' {
		return s.token()
	}
	return s.eachMember(func() error {
		if string(s.name) == "effort" {
			return s.effort(set)
		}
		return s.skip()
	})
}

// effort reads a reasoning effort string or null, adding the parameter bit for a value that asks for one.
func (s *scanner) effort(set *ParameterSet) error {
	v, err := s.str()
	if err == nil && len(v) > 0 && string(v) != "none" {
		*set = set.With(ParamReasoningEffort)
	}
	return err
}

// textParameters reads a Responses text object for verbosity and format, which Inspect reads only for the request's
// capabilities. It consumes the whole value, including its closing brace. Verbosity has no catalog parameter, so it is
// reported as unknown; format is ParamResponseFormat.
func (s *scanner) textParameters(set *ParameterSet) (verbosity bool, err error) {
	err = s.eachMember(func() error {
		switch string(s.name) {
		case "verbosity":
			if s.dec.PeekKind() != 'n' {
				verbosity = true
			}
			return s.skip()
		case "format":
			if s.dec.PeekKind() != 'n' {
				*set = set.With(ParamResponseFormat)
			}
			return s.skip()
		}
		return s.skip()
	})
	return verbosity, err
}

// outputConfig reads Messages' output_config for the reply format and reasoning effort it carries. It consumes the
// whole value, including its closing brace.
func (s *scanner) outputConfig(set *ParameterSet) error {
	return s.eachMember(func() error {
		switch string(s.name) {
		case "format":
			if s.dec.PeekKind() != 'n' {
				*set = set.With(ParamResponseFormat)
			}
			return s.skip()
		case "effort":
			return s.effort(set)
		}
		return s.skip()
	})
}

// parameterName bounds what an error message repeats of a name the client chose.
func parameterName(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	return strings.ToValidUTF8(name[:limit], "?") + "…"
}
