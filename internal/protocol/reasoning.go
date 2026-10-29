package protocol

import "math"

// effort keeps omission distinct from an explicit disable. Disable wins over strength,
// independently of JSON member order (OpenRouter Reasoning Tokens, read 2026-10-29). An empty effort is no setting:
// the writers emit nothing for it, and the summary does not count it as asking for reasoning (see
// reasoningEffort in grammar.go), so it must not hide an effort the reasoning object does set.
func (c *canon) effort() string {
	effort := c.Reasoning.effort()
	if effort == "none" || c.ReasoningEffort == nil || *c.ReasoningEffort == "" {
		return effort
	}
	return *c.ReasoningEffort
}

func (r *canonReasoning) effort() string {
	if r == nil {
		return ""
	}
	switch {
	case r.Enabled != nil && !*r.Enabled:
		return "none"
	case r.Effort != nil && *r.Effort != "":
		return *r.Effort
	case r.Enabled != nil && *r.Enabled, r.MaxTokens != nil:
		return "medium"
	}
	return ""
}

// effortSet reports whether the request writes an effort that needs no approximation from a budget.
func (c *canon) effortSet() bool {
	return c.ReasoningEffort != nil && *c.ReasoningEffort != "" ||
		c.Reasoning != nil && c.Reasoning.Effort != nil && *c.Reasoning.Effort != ""
}

// targetEffort approximates a budget only for targets with no budget knob. These
// shares come from OpenRouter; ties choose the lower share, including xhigh/max.
func (c *canon) targetEffort(maxOutput int64) (string, error) {
	effort := c.effort()
	if c.budget() != nil && *c.budget() <= 0 {
		return "", shapeError("reasoning.max_tokens must be positive")
	}
	if c.budget() == nil || effort == "none" || c.effortSet() {
		return effort, nil
	}
	if maxOutput <= 0 {
		return "", &NotSupported{Reason: "a reasoning budget needs an output cap to map to effort"}
	}
	budget := float64(*c.budget())
	nearest, distance := "", math.Inf(1)
	for _, level := range []string{"minimal", "low", "medium", "high", "xhigh"} {
		if d := math.Abs(budget - float64(maxOutput)*manualRatios[level]); d < distance {
			nearest, distance = level, d
		}
	}
	return nearest, nil
}
