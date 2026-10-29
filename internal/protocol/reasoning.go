package protocol

import "math"

// effort keeps omission distinct from an explicit disable. Disable wins over strength,
// independently of JSON member order (OpenRouter Reasoning Tokens, read 2026-10-29).
func (c *canon) effort() string {
	effort := c.Reasoning.effort()
	if effort == "none" {
		return effort
	}
	if c.ReasoningEffort != nil {
		return *c.ReasoningEffort
	}
	return effort
}

func (r *canonReasoning) effort() string {
	if r == nil {
		return ""
	}
	switch {
	case r.Enabled != nil && !*r.Enabled:
		return "none"
	case r.Effort != nil:
		return *r.Effort
	case r.Enabled != nil && *r.Enabled, r.MaxTokens != nil:
		return "medium"
	}
	return ""
}

// targetEffort approximates a budget only for targets with no budget knob. These
// shares come from OpenRouter; ties choose the lower share, including xhigh/max.
func (c *canon) targetEffort(maxOutput int64) (string, error) {
	effort := c.effort()
	if c.budget() != nil && *c.budget() <= 0 {
		return "", shapeError("reasoning.max_tokens must be positive")
	}
	if c.budget() == nil || effort == "none" || c.ReasoningEffort != nil || c.Reasoning.Effort != nil {
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
