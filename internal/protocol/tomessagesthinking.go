package protocol

import (
	"slices"

	"axiom/internal/catalog"
)

// How a request that reasons asks a Messages model to think: adaptively at an effort, or within a budget.

// manualRatios are OpenRouter's shares of max_tokens for a thinking budget by effort (OpenRouter《Reasoning
// tokens》, read 2026-10-29).
var manualRatios = map[string]float64{"max": 0.95, "xhigh": 0.95, "high": 0.8, "medium": 0.5, "low": 0.2, "minimal": 0.1}

// adaptiveEfforts are the efforts output_config.effort takes (Anthropic《Effort》, read 2026-10-29).
var adaptiveEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// checkThinking maps the reasoning the request asks for onto the model's way of thinking.
func (m *messagesWriter) checkThinking() error {
	var err error
	m.effort, err = m.c.targetEffort(m.maxTokens)
	if err != nil {
		return err
	}
	m.thinkingOn = m.effort != "" && m.effort != "none"
	if !m.thinkingOn {
		return nil
	}
	switch m.t.Thinking {
	case catalog.ThinkingAdaptive:
		return m.checkAdaptive()
	case catalog.ThinkingManual:
		return m.checkManual()
	case catalog.ThinkingNone:
		return &NotSupported{Reason: "the model does not think"}
	case catalog.ThinkingUnset:
	}
	// Not one of the two ways to ask is the model's: guessing one is a request the upstream refuses, or a budget it
	// was never asked to spend.
	return unspecified("reasoning", "how the model is asked to think (thinking)")
}

func (c *canon) budget() *int64 {
	if c.Reasoning == nil {
		return nil
	}
	return c.Reasoning.MaxTokens
}

// checkAdaptive holds the effort to the ones output_config.effort takes; such a model sets its own budget.
func (m *messagesWriter) checkAdaptive() error {
	if m.effort == "minimal" {
		m.effort = "low"
	}
	if !slices.Contains(adaptiveEfforts, m.effort) {
		return &NotSupported{Reason: "effort " + m.effort + " has no Messages counterpart"}
	}
	return nil
}

// checkManual works out the budget of a manual-thinking model as OpenRouter does, which must fit under max_tokens,
// and holds the request to Anthropic's rule that the last assistant turn, when it called tools, starts with its
// thinking (《Extended thinking》"Turn structure in manual mode"): without reasoning to replay there, the request goes
// to a model that thinks on its own.
func (m *messagesWriter) checkManual() error {
	if b := m.c.budget(); b != nil {
		m.budget = *b
	} else if ratio, ok := manualRatios[m.effort]; ok {
		m.budget = max(min(int64(float64(m.maxTokens)*ratio), 128000), 1024)
	} else {
		return &NotSupported{Reason: "effort " + m.effort + " has no budget"}
	}
	if m.budget < 1024 || m.budget >= m.maxTokens {
		return &NotSupported{Reason: "the thinking budget does not fit under max_tokens"}
	}
	for _, msg := range slices.Backward(m.c.Messages) {
		if msg.Role == "assistant" {
			if len(msg.ToolCalls) > 0 && !slices.ContainsFunc(msg.ReasoningDetails, replayable) {
				return &NotSupported{Reason: "manual thinking after a tool call without its reasoning to replay"}
			}
			break
		}
	}
	return nil
}

// replayable reports whether an assistant message can hand a reasoning entry back to Anthropic: its own format, signed
// or redacted. They go first in the message: Anthropic binds each thinking block to everything before it, and a Chat
// message cannot say where between its text and calls the reasoning was (README.md「跨格式转换」).
func replayable(d canonReasoningDetail) bool {
	switch {
	case d.Format != "anthropic-claude-v1":
		return false
	case d.Type == "reasoning.text":
		return !isNull(d.Signature) && !isNull(d.Text)
	case d.Type == "reasoning.encrypted":
		return !isNull(d.Data)
	}
	return false
}

func (m *messagesWriter) thinkingSetting() {
	o := m.o
	adaptive := m.thinkingOn && m.t.Thinking == catalog.ThinkingAdaptive
	switch {
	case adaptive:
		o.str(`,"thinking":{"type":"adaptive"}`)
	case m.thinkingOn:
		o.str(`,"thinking":{"type":"enabled","budget_tokens":`).int(m.budget).byte('}')
	}
	if m.thinkingOn && m.c.Reasoning != nil && m.c.Reasoning.Exclude != nil {
		if o.err != nil {
			return
		}
		o.cut(len(o.buf) - 1)
		if *m.c.Reasoning.Exclude {
			o.str(`,"display":"omitted"}`)
		} else {
			o.str(`,"display":"summarized"}`)
		}
	} else if m.effort == "none" {
		o.str(`,"thinking":{"type":"disabled"}`)
	}
}

// thinking writes the thinking setting and output_config, including a structured reply schema.
func (m *messagesWriter) thinking() error {
	m.thinkingSetting()
	o := m.o
	adaptive := m.thinkingOn && m.t.Thinking == catalog.ThinkingAdaptive
	schema, err := m.replyFormat()
	if err != nil {
		return err
	}
	switch {
	case adaptive && schema != nil:
		o.str(`,"output_config":{"effort":"`).str(m.effort).str(`",`)
		m.writeFormat(schema)
		o.byte('}')
	case adaptive:
		o.str(`,"output_config":{"effort":"`).str(m.effort).str(`"}`)
	case schema != nil:
		o.str(`,"output_config":{`)
		m.writeFormat(schema)
		o.byte('}')
	}
	return o.err
}
