package bedrock

import (
	"slices"
	"strings"

	"axiom/internal/platform/jsonx"
)

// The Chat members Converse has no member of its own for: those that hold Chat's default ask nothing and are dropped,
// and a reasoning effort is the model's own parameter where the model takes one.

// additional is the model's own parameters, which Converse passes on as they are.
type additional struct {
	ReasoningConfig *reasoningConfig `json:"reasoningConfig,omitzero"`
}

// reasoningConfig is Nova 2's extended thinking
// (https://docs.aws.amazon.com/nova/latest/nova2-userguide/extended-thinking.html, read 2026-11-27).
type reasoningConfig struct {
	Type               string `json:"type"`
	MaxReasoningEffort string `json:"maxReasoningEffort"`
}

// inert reports whether each member Converse has no word for is absent or holds Chat's default (OpenAI《Create chat
// completion》), compared in canonical form; verbosity has no value that asks for nothing.
func inert(in chatRequest) bool {
	for _, m := range []struct {
		v        jsonx.Raw
		defaults []string
	}{
		{in.N, []string{"1"}}, {in.Logprobs, []string{"false"}},
		{in.FrequencyPenalty, []string{"0"}}, {in.PresencePenalty, []string{"0"}}, {in.LogitBias, []string{"{}"}},
		{in.Modalities, []string{`["text"]`}}, {in.ServiceTier, []string{`"auto"`, `"default"`}},
		{in.ResponseFormat, []string{`{"type":"text"}`}}, {in.Verbosity, nil},
	} {
		if absent(m.v) {
			continue
		}
		if c, err := jsonx.MarshalCanonical(m.v); err != nil || !slices.Contains(m.defaults, string(c)) {
			return false
		}
	}
	return true
}

// novaEfforts are Nova 2's reasoning efforts by Chat's reasoning_effort, each the nearest Nova has; none is Nova's
// default, no reasoning.
var novaEfforts = map[string]string{"none": "", "minimal": "low", "low": "low", "medium": "medium", "high": "high", "xhigh": "high"}

// reasoning is the reasoning configuration of a Chat reasoning_effort for model. Nova 2 Lite is the one model behind
// Converse that takes an effort; to any other the effort asks nothing it can act on, and it is dropped. Nova refuses its
// high effort beside an output cap or a sampling, and every call the gateway sends has a cap, so that effort goes to
// another endpoint rather than to an error from Bedrock.
func reasoning(model string, effort *string, c inferenceConfig) (*reasoningConfig, error) {
	if effort == nil || !strings.Contains(model, "amazon.nova-2-lite") {
		return nil, nil
	}
	level, ok := novaEfforts[*effort]
	switch {
	case !ok, level == "high" && (c.MaxTokens != nil || c.Temperature != nil || c.TopP != nil):
		return nil, errUnconvertible
	case level == "":
		return nil, nil
	}
	return &reasoningConfig{Type: "enabled", MaxReasoningEffort: level}, nil
}
