package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"slices"
	"strconv"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// toMessages writes a Chat request as an Anthropic Messages request (Anthropic《Messages API reference》, read
// 2026-10-14; README.md「跨格式转换」). Strings are copied as written. The body is written into one writer, which
// starts at the size of the Chat body and draws every growth from b first.
func toMessages(c *canon, t Target, w Rewrites, b *budget, body []byte) ([]byte, error) {
	m := &messagesWriter{c: c, t: t, o: newWriter(len(body)+4096, b)}
	if err := m.check(); err != nil {
		return nil, err
	}
	o := m.o
	o.str(`{"model":`).quoted(t.Model).str(`,"max_tokens":`).int(m.maxTokens)
	for _, write := range []func() error{m.system, m.messages, m.tools, m.toolChoice, m.thinking, m.sampling} {
		if err := write(); err != nil {
			return nil, err
		}
	}
	if t.Streams { // the gateway reads every answer as a stream, and routing sends a conversion only to an endpoint that streams
		o.str(`,"stream":true`)
	}
	if c.User != nil {
		o.str(`,"metadata":{"user_id":`)
		// appendMetadataUserID writes the identifier and its outer quoting in place, within this.
		if o.room(3 * (quoted(w.withDefaults().IdentityLen) + quoted(len(w.Device)))) {
			var err error
			if o.buf, err = appendMetadataUserID(o.buf, w, *c.User); err != nil {
				return nil, err
			}
		}
		o.byte('}')
	}
	o.byte('}')
	return o.buf, o.err
}

type messagesWriter struct {
	c          *canon
	t          Target
	o          *writer
	maxTokens  int64
	budget     int64 // the thinking budget of a manual-thinking model, 0 for none
	effort     string
	firstTurn  int // where the leading system messages end
	thinkingOn bool
	args       jsonx.Validator // a tool call's arguments are checked through it, reusing its decoder
}

// manualRatios are OpenRouter's shares of max_tokens for a thinking budget by effort (OpenRouter《Reasoning
// tokens》, read 2026-10-29).
var manualRatios = map[string]float64{"max": 0.95, "xhigh": 0.95, "high": 0.8, "medium": 0.5, "low": 0.2, "minimal": 0.1}

// adaptiveEfforts are the efforts output_config.effort takes (Anthropic《Effort》, read 2026-10-29).
var adaptiveEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// check settles what the request asks of the model before anything is written.
func (m *messagesWriter) check() error {
	c := m.c
	if !isNull(c.Verbosity) {
		return &NotSupported{Reason: "verbosity has no Messages counterpart"}
	}
	if c.ServiceTier != nil && *c.ServiceTier != "auto" && *c.ServiceTier != "default" {
		return &NotSupported{Reason: "service_tier " + *c.ServiceTier + " has no Messages counterpart"}
	}
	m.maxTokens = c.maxOutput()
	if m.maxTokens == 0 {
		m.maxTokens = m.t.MaxOutput
	}
	if m.maxTokens <= 0 {
		return &NotSupported{Reason: "no output cap: the request sets none and the model has no default"}
	}
	for m.firstTurn < len(c.Messages) && isSystem(c.Messages[m.firstTurn].Role) {
		m.firstTurn++
	}
	if m.firstTurn < len(c.Messages) && c.Messages[m.firstTurn].Role != "user" {
		return &NotSupported{Reason: "the conversation does not start with a user message"}
	}
	return m.checkThinking()
}

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

func isSystem(role string) bool { return role == "system" || role == "developer" }

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

// system writes the leading system and developer messages as the system blocks.
func (m *messagesWriter) system() error {
	if m.firstTurn == 0 {
		return nil
	}
	o := m.o
	o.str(`,"system":[`)
	for _, msg := range m.c.Messages[:m.firstTurn] {
		mark := len(o.buf)
		if o.last() != '[' {
			o.byte(',')
		}
		at := len(o.buf)
		if err := m.blocks(msg, true); err != nil {
			return err
		}
		if len(o.buf) == at { // nothing to send: no comma either
			o.cut(mark)
		}
	}
	o.byte(']')
	return o.err
}

// messages writes the conversation: consecutive tool results become one user message, as Anthropic wants them.
func (m *messagesWriter) messages() error {
	o := m.o
	o.str(`,"messages":[`)
	msgs := m.c.Messages[m.firstTurn:]
	for i := 0; i < len(msgs) && o.err == nil; i++ {
		mark := len(o.buf)
		if o.last() != '[' {
			o.byte(',')
		}
		var err error
		switch msg := msgs[i]; msg.Role {
		case "tool":
			n := 1
			for i+n < len(msgs) && msgs[i+n].Role == "tool" {
				n++
			}
			err = m.toolResults(msgs[i : i+n])
			i += n - 1
		case "assistant":
			err = m.assistantTurn(msg)
		case "user":
			err = m.turn("user", msg)
		default:
			err = m.midSystem(msgs, i)
		}
		if err != nil {
			return err
		}
		if len(o.buf) <= mark+1 { // an assistant turn with nothing to send, which Messages refuses
			o.cut(mark)
		}
	}
	o.byte(']')
	return o.err
}

func (m *messagesWriter) turn(role string, msg canonMessage) error {
	m.o.str(`{"role":"`).str(role).str(`","content":[`)
	if err := m.blocks(msg, role != "user"); err != nil {
		return err
	}
	m.o.str("]}")
	return nil
}

// midSystem writes a system message after the conversation started, in place, where Anthropic allows one: right
// after a user turn, results included, and before the next assistant turn or at the end (《Mid-conversation system
// messages》"Limitations", read 2026-10-13).
func (m *messagesWriter) midSystem(msgs []canonMessage, i int) error {
	if !isSystem(msgs[i].Role) {
		return &NotSupported{Reason: "role " + msgs[i].Role + " has no Messages counterpart"}
	}
	prev := msgs[i-1].Role
	next := ""
	if i+1 < len(msgs) {
		next = msgs[i+1].Role
	}
	switch {
	case m.t.MidSystem == catalog.ClaimNo:
		return &NotSupported{Reason: "the model takes no system message after the conversation started"}
	case m.t.MidSystem == catalog.ClaimUnset:
		return unspecified("a system message after the conversation started", "whether the model takes one (mid_system)")
	case prev != "user" && prev != "tool", next != "" && next != "assistant":
		return &NotSupported{Reason: "a system message where Anthropic does not allow one"}
	case prev == "tool" && m.t.UntrustedSystemAfterTools == catalog.ClaimYes:
		return &NotSupported{Reason: "the model does not trust a system message right after tool results"}
	case prev == "tool" && m.t.UntrustedSystemAfterTools == catalog.ClaimUnset:
		return unspecified("a system message right after tool results", "whether the model trusts one (untrusted_system_after_tools)")
	}
	return m.turn("system", msgs[i])
}

// assistantTurn writes an assistant message: the reasoning to replay first, then its text and tool calls. One with
// none of them writes nothing.
func (m *messagesWriter) assistantTurn(msg canonMessage) error {
	o := m.o
	begin := len(o.buf)
	o.str(`{"role":"assistant","content":[`)
	first := len(o.buf)
	for _, d := range msg.ReasoningDetails {
		switch {
		case !replayable(d):
		case d.Type == "reasoning.encrypted":
			o.commaAfter(first).str(`{"type":"redacted_thinking","data":`).raw(d.Data).byte('}')
		default:
			o.commaAfter(first).str(`{"type":"thinking","thinking":`).raw(d.Text).str(`,"signature":`).raw(d.Signature).byte('}')
		}
	}
	mark := len(o.buf)
	o.commaAfter(first)
	at := len(o.buf)
	if err := m.blocks(msg, true); err != nil {
		return err
	}
	if len(o.buf) == at {
		o.cut(mark)
	}
	for _, call := range msg.ToolCalls {
		o.commaAfter(first).str(`{"type":"tool_use","id":`).toolUseID(call.ID)
		o.str(`,"name":`).raw(call.Function.Name).str(`,"input":`)
		if err := m.arguments(call.Function.Arguments); err != nil {
			return err
		}
		o.byte('}')
		if o.err != nil {
			return o.err
		}
	}
	if len(o.buf) == first {
		o.cut(begin)
		return nil
	}
	o.str("]}")
	return nil
}

// arguments writes a call's arguments, a JSON string holding a JSON object, as the object, unquoted in place; empty
// arguments are an empty object, as models that call a function without parameters may send.
func (m *messagesWriter) arguments(v jsontext.Value) error {
	o := m.o
	if !o.room(len(v)) {
		return o.err
	}
	at := len(o.buf)
	s, err := jsontext.AppendUnquote(o.buf, v) // never longer than v: it fits in the room made for it
	if err != nil {
		return &NotSupported{Reason: "a tool call's arguments are not a string"}
	}
	args := bytes.TrimSpace(s[at:])
	switch {
	case len(args) == 0:
		o.buf = append(s[:at], "{}"...)
	case args[0] != '{' || m.args.Validate(args) != nil:
		return &NotSupported{Reason: "a tool call's arguments are not a JSON object"}
	default:
		o.buf = append(s[:at], args...) // copy within the array: args lies in it, at or after at
	}
	return nil
}

// toolResults writes consecutive tool messages as one user message of tool_result blocks.
func (m *messagesWriter) toolResults(msgs []canonMessage) error {
	o := m.o
	o.str(`{"role":"user","content":[`)
	for i, msg := range msgs {
		if i > 0 {
			o.byte(',')
		}
		o.str(`{"type":"tool_result","tool_use_id":`).toolUseID(*msg.ToolCallID).str(`,"content":`)
		if msg.Content.Kind() == '"' {
			o.raw(msg.Content)
		} else {
			o.byte('[')
			if err := m.blocks(msg, false); err != nil {
				return err
			}
			o.byte(']')
		}
		o.byte('}')
	}
	o.str("]}")
	return nil
}

// tools writes the function tools.
func (m *messagesWriter) tools() error {
	if len(m.c.Tools) == 0 {
		return nil
	}
	m.o.str(`,"tools":[`)
	for i, tool := range m.c.Tools {
		if i > 0 {
			m.o.byte(',')
		}
		if err := m.tool(tool); err != nil {
			return err
		}
	}
	m.o.byte(']')
	return m.o.err
}

// tool writes a function tool; a strict one only with a schema in Anthropic's subset.
func (m *messagesWriter) tool(tool canonTool) error {
	if tool.Type != "function" {
		return &NotSupported{Reason: "a " + tool.Type + " tool has no Messages counterpart"}
	}
	o := m.o
	f := tool.Function
	o.str(`{"name":`).raw(f.Name)
	if !isNull(f.Description) {
		o.str(`,"description":`).raw(f.Description)
	}
	schema := f.Parameters
	if isNull(schema) {
		schema = jsontext.Value(`{"type":"object","properties":{}}`)
	}
	o.str(`,"input_schema":`).raw(schema)
	if f.Strict != nil && *f.Strict {
		if err := anthropicSchema(schema, o.b); err != nil {
			return err
		}
		o.str(`,"strict":true`)
	}
	if !isNull(tool.CacheControl) {
		o.str(`,"cache_control":`).raw(tool.CacheControl)
	}
	o.byte('}')
	return nil
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

// writeFormat writes output_config's format member for a structured reply.
func (m *messagesWriter) writeFormat(schema jsontext.Value) {
	m.o.str(`"format":{"type":"json_schema","schema":`).raw(schema).byte('}')
}

// replyFormat is the schema of a structured reply, nil for none.
func (m *messagesWriter) replyFormat() (jsontext.Value, error) {
	f := m.c.ResponseFormat
	switch {
	case f == nil || f.Type == "text":
		return nil, nil
	case f.Type != "json_schema" || f.JSONSchema == nil:
		return nil, &NotSupported{Reason: "response_format " + f.Type + " has no Messages counterpart"}
	}
	if err := anthropicSchema(f.JSONSchema.Schema, m.o.b); err != nil {
		return nil, err
	}
	return f.JSONSchema.Schema, nil
}

// samplingRefusal is why the model cannot take what, a sampling other than the default, nil when it can: thinking
// refuses it, a model whose spec says it takes the default only refuses it, and a spec that says nothing leaves a
// request the upstream may refuse.
func (m *messagesWriter) samplingRefusal(what string) error {
	switch {
	case m.thinkingOn || m.t.DefaultSamplingOnly == catalog.ClaimYes:
		return &NotSupported{Reason: what + ", which the model refuses"}
	case m.t.DefaultSamplingOnly == catalog.ClaimUnset:
		return unspecified(what, "whether the model takes it (default_sampling_only)")
	}
	return nil
}

// forcedRefusal is why the model cannot take a tool choice that forces a call, nil when it can: thinking refuses it,
// and so does a model whose spec says it takes none.
func (m *messagesWriter) forcedRefusal() error {
	const what = "a forced tool choice"
	switch {
	case m.thinkingOn || m.t.ForcedToolChoice == catalog.ClaimNo:
		return &NotSupported{Reason: what + ", which the model refuses"}
	case m.t.ForcedToolChoice == catalog.ClaimUnset:
		return unspecified(what, "whether the model takes it (forced_tool_choice)")
	}
	return nil
}

// sampling writes temperature, top_p, stop sequences and the service tier. A value other than the default is refused
// where the model or thinking refuses it, and one outside Anthropic's 0 to 1.
func (m *messagesWriter) sampling() error {
	c, o := m.c, m.o
	for _, s := range []struct {
		name string
		v    jsontext.Value
	}{{"temperature", c.Temperature}, {"top_p", c.TopP}} {
		if numberIs(s.v, 1) {
			continue
		}
		if err := m.samplingRefusal(s.name + " other than the default"); err != nil {
			return err
		}
		if err := o.b.draw(rounded(int64(len(s.v)))); err != nil { // string(s.v)
			return err
		}
		if f, err := strconv.ParseFloat(string(s.v), 64); err != nil || f > 1 || f < 0 {
			return &NotSupported{Reason: s.name + " outside 0 to 1"}
		}
		o.str(`,"`).str(s.name).str(`":`).raw(s.v)
	}
	if c.Stop.Kind() == '"' {
		o.str(`,"stop_sequences":[`).raw(c.Stop).byte(']')
	} else if c.Stop.Kind() == '[' {
		o.str(`,"stop_sequences":`).raw(c.Stop)
	}
	if c.ServiceTier != nil {
		tier := `"auto"`
		if *c.ServiceTier == "default" {
			tier = `"standard_only"`
		}
		o.str(`,"service_tier":`).str(tier)
	}
	return o.err
}
