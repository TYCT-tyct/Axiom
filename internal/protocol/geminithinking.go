package protocol

import (
	"slices"
	"strings"

	"axiom/internal/platform/jsonx"
)

// geminiThinker is how a model takes thinking: by the levels it lists, or, without levels, by a budget within its range;
// whether a budget of 0 turns its thinking off, and whether it thinks when not asked to.
type geminiThinker struct {
	levels               []string
	minBudget, maxBudget int64
	off, thinksByDefault bool
}

// geminiThinkers are the models whose thinking the conversion writes, as Google's《Gemini thinking》lists them (read
// 2026-11-27): the levels each takes, and the 2.5 models' budget ranges and defaults. A model not listed, or a setting
// its row cannot say, is sent through Google's OpenAI-compatible layer, which maps it itself.
var geminiThinkers = map[string]geminiThinker{
	"gemini-2.5-pro":               {minBudget: 128, maxBudget: 32768, thinksByDefault: true},
	"gemini-2.5-flash":             {minBudget: 1, maxBudget: 24576, off: true, thinksByDefault: true},
	"gemini-2.5-flash-lite":        {minBudget: 512, maxBudget: 24576, off: true},
	"gemini-4-carbon":              {levels: []string{"low", "medium", "high"}, thinksByDefault: true},
	"gemini-4-argon":               {levels: []string{"low", "medium", "high"}, thinksByDefault: true},
	"gemini-4-neon":                {levels: []string{"low", "medium", "high", "xhigh"}, thinksByDefault: true},
	"gemini-4-hydrogen":            {levels: []string{"low", "medium", "high", "xhigh"}, thinksByDefault: true},
	"gemini-3.8-flash":             {levels: []string{"low", "medium", "high"}, thinksByDefault: true},
	"gemini-3.6-flash":             {levels: []string{"minimal", "low", "medium", "high"}, thinksByDefault: true},
	"gemini-3.5-flash-lite":        {levels: []string{"minimal", "low", "medium", "high"}, thinksByDefault: true},
	"gemini-3.1-pro-preview":       {levels: []string{"low", "medium", "high"}, thinksByDefault: true},
	"gemini-3.1-flash-lite":        {levels: []string{"minimal", "low", "medium", "high"}, thinksByDefault: true},
	"gemini-3.1-flash-lite-image":  {levels: []string{"minimal", "high"}, thinksByDefault: true},
	"gemini-3-flash-preview":       {levels: []string{"minimal", "low", "medium", "high"}, thinksByDefault: true},
	"gemini-3-pro-preview":         {levels: []string{"low", "high"}, thinksByDefault: true},
	"gemini-robotics-er-2-preview": {levels: []string{"minimal", "low", "medium", "high"}, thinksByDefault: true},
}

// geminiEffortBudgets are the 2.5 models' budgets for a reasoning effort, Google's OpenAI-compatible layer's own
// (《OpenAI compatibility》Thinking, read 2026-11-27), so a request thinks as much whichever way it is sent.
var geminiEffortBudgets = map[string]int64{"minimal": 1024, "low": 1024, "medium": 8192, "high": 24576}

// geminiThinking writes the thinking the request asks for in the terms its model takes (geminiThinkers): effort none
// or reasoning disabled is a budget of 0, a reasoning budget the thinking budget, any other effort the model's level
// or budget for it; thoughts come back unless the client hid them. What the model's row cannot say exactly is not
// supported.
func (c *canon) geminiThinking(config map[string]jsonx.Raw, model string) error {
	a := c.thinkingAsked()
	if !a.asked {
		return nil
	}
	m, ok := geminiThinkers[strings.TrimPrefix(model, "models/")]
	if !ok {
		return &NotSupported{Reason: "the model's thinking settings are not known here"}
	}
	if a.effort == "none" {
		if !m.off {
			return &NotSupported{Reason: "the model's thinking cannot be turned off"}
		}
		config["thinkingConfig"] = jsonValue(requestObject{"thinkingBudget": jsonx.Raw("0")})
		return nil
	}
	t := requestObject{}
	name, amount, err := m.amount(a)
	if err != nil {
		return err
	}
	if name != "" {
		t[name] = amount
	}
	if a.thoughts {
		t["includeThoughts"] = jsonx.Raw("true")
	}
	if len(t) > 0 {
		config["thinkingConfig"] = jsonValue(t)
	}
	return nil
}

// amount is how much the model is asked to think, as the thinkingConfig member that says it, none for its default.
func (m geminiThinker) amount(a thinkingAsk) (string, jsonx.Raw, error) {
	switch {
	case a.budget != nil:
		if m.levels != nil || *a.budget < m.minBudget || *a.budget > m.maxBudget {
			return "", nil, &NotSupported{Reason: "the model takes no such thinking budget"}
		}
		return "thinkingBudget", jsonValue(*a.budget), nil
	case a.effort != "" && m.levels != nil:
		if !slices.Contains(m.levels, a.effort) {
			return "", nil, &NotSupported{Reason: "reasoning effort " + a.effort + " is not one of the model's levels"}
		}
		return "thinkingLevel", jsonValue(a.effort), nil
	case a.effort != "":
		budget, ok := geminiEffortBudgets[a.effort]
		if !ok {
			return "", nil, &NotSupported{Reason: "reasoning effort " + a.effort + " has no budget on the model"}
		}
		return "thinkingBudget", jsonValue(budget), nil
	case !m.thinksByDefault: // reasoning on, at the model's default, which is off
		return "", nil, &NotSupported{Reason: "the model does not think unless given a budget"}
	}
	return "", nil, nil
}

// thinkingAsk is what the request's reasoning settings ask of Gemini's thinking.
type thinkingAsk struct {
	asked    bool   // any reasoning setting is present
	effort   string // the effort, none for reasoning disabled
	budget   *int64 // the reasoning budget
	thoughts bool   // the thoughts come back
}

func (c *canon) thinkingAsked() thinkingAsk {
	var a thinkingAsk
	if c.ReasoningEffort != nil && *c.ReasoningEffort != "" { // an empty effort is none set, as elsewhere
		a.asked, a.effort, a.thoughts = true, *c.ReasoningEffort, true
	}
	r := c.Reasoning
	if r == nil {
		return a
	}
	a.asked, a.thoughts = true, r.Exclude == nil || !*r.Exclude
	switch {
	case r.Enabled != nil && !*r.Enabled:
		a.effort = "none"
	case r.MaxTokens != nil:
		a.budget = r.MaxTokens
	case r.Effort != nil:
		a.effort = *r.Effort
	}
	return a
}
