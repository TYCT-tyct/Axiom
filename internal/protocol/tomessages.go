package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"slices"
	"strconv"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// toMessages writes a Chat request as an Anthropic Messages request (Anthropic《Messages API reference》, read
// 2026-10-14; README.md「跨格式转换」). Strings are copied as written.
func toMessages(c *canon, t Target, w Rewrites) ([]byte, error) {
	m := &messagesWriter{c: c, t: t}
	if err := m.check(); err != nil {
		return nil, err
	}
	out := appendQuoted(append([]byte(nil), `{"model":`...), t.Model)
	out = strconv.AppendInt(append(out, `,"max_tokens":`...), m.maxTokens, 10)
	var err error
	for _, write := range []func([]byte) ([]byte, error){
		m.system, m.messages, m.tools, m.toolChoice, m.thinking, m.sampling,
	} {
		if out, err = write(out); err != nil {
			return nil, err
		}
	}
	if t.Streams { // the gateway reads every answer as a stream, and routing sends a conversion only to an endpoint that streams
		out = append(out, `,"stream":true`...)
	}
	if c.User != nil {
		out = appendQuoted(append(out, `,"metadata":{"user_id":`...), w.Identity(*c.User))
		out = append(out, '}')
	}
	return append(out, '}'), nil
}

type messagesWriter struct {
	c          *canon
	t          Target
	maxTokens  int64
	budget     int64 // the thinking budget of a manual-thinking model, 0 for none
	effort     string
	firstTurn  int // where the leading system messages end
	thinkingOn bool
}

// manualRatios are OpenRouter's shares of max_tokens for a thinking budget by effort (OpenRouter《Reasoning
// tokens》, read 2026-10-14).
var manualRatios = map[string]float64{"xhigh": 0.95, "high": 0.8, "medium": 0.5, "low": 0.2, "minimal": 0.1}

// adaptiveEfforts are the efforts output_config.effort takes (Anthropic《Effort》, read 2026-10-14).
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
	m.effort = m.c.effort()
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
	if m.c.budget() != nil {
		return &NotSupported{Reason: "a thinking budget for a model that sets its own"}
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
			if len(msg.ToolCalls) > 0 && len(replayable(msg.ReasoningDetails)) == 0 {
				return &NotSupported{Reason: "manual thinking after a tool call without its reasoning to replay"}
			}
			break
		}
	}
	return nil
}

// replayable are the reasoning entries an assistant message can hand back to Anthropic: its own format, signed or
// redacted. They go first in the message: Anthropic binds each thinking block to everything before it, and a Chat
// message cannot say where between its text and calls the reasoning was (README.md「跨格式转换」).
func replayable(details []canonReasoningDetail) []canonReasoningDetail {
	var out []canonReasoningDetail
	for _, d := range details {
		switch {
		case d.Format != "anthropic-claude-v1":
		case d.Type == "reasoning.text" && !isNull(d.Signature) && !isNull(d.Text):
			out = append(out, d)
		case d.Type == "reasoning.encrypted" && !isNull(d.Data):
			out = append(out, d)
		}
	}
	return out
}

// system writes the leading system and developer messages as the system blocks.
func (m *messagesWriter) system(out []byte) ([]byte, error) {
	if m.firstTurn == 0 {
		return out, nil
	}
	out = append(out, `,"system":[`...)
	for _, msg := range m.c.Messages[:m.firstTurn] {
		b, err := blocks(msg, true)
		if err != nil {
			return nil, err
		}
		if len(b) > 0 && out[len(out)-1] != '[' {
			out = append(out, ',')
		}
		out = append(out, b...)
	}
	return append(out, ']'), nil
}

// messages writes the conversation: consecutive tool results become one user message, as Anthropic wants them.
func (m *messagesWriter) messages(out []byte) ([]byte, error) {
	out = append(out, `,"messages":[`...)
	msgs := m.c.Messages[m.firstTurn:]
	for i := 0; i < len(msgs); i++ {
		mark := len(out)
		if out[len(out)-1] != '[' {
			out = append(out, ',')
		}
		var err error
		switch msg := msgs[i]; msg.Role {
		case "tool":
			n := 1
			for i+n < len(msgs) && msgs[i+n].Role == "tool" {
				n++
			}
			out, err = toolResults(out, msgs[i:i+n])
			i += n - 1
		case "assistant":
			out, err = assistantTurn(out, msg)
		case "user":
			out, err = turn(out, "user", msg)
		default:
			out, err = m.midSystem(out, msgs, i)
		}
		if err != nil {
			return nil, err
		}
		if len(out) <= mark+1 { // an assistant turn with nothing to send, which Messages refuses
			out = out[:mark]
		}
	}
	return append(out, ']'), nil
}

func turn(out []byte, role string, msg canonMessage) ([]byte, error) {
	b, err := blocks(msg, role != "user")
	if err != nil {
		return nil, err
	}
	out = append(append(append(out, `{"role":"`...), role...), `","content":[`...)
	return append(append(out, b...), "]}"...), nil
}

// midSystem writes a system message after the conversation started, in place, where Anthropic allows one: right
// after a user turn, results included, and before the next assistant turn or at the end (《Mid-conversation system
// messages》"Limitations", read 2026-10-13).
func (m *messagesWriter) midSystem(out []byte, msgs []canonMessage, i int) ([]byte, error) {
	if !isSystem(msgs[i].Role) {
		return nil, &NotSupported{Reason: "role " + msgs[i].Role + " has no Messages counterpart"}
	}
	prev := msgs[i-1].Role
	next := ""
	if i+1 < len(msgs) {
		next = msgs[i+1].Role
	}
	switch {
	case m.t.MidSystem == catalog.ClaimNo:
		return nil, &NotSupported{Reason: "the model takes no system message after the conversation started"}
	case m.t.MidSystem == catalog.ClaimUnset:
		return nil, unspecified("a system message after the conversation started", "whether the model takes one (mid_system)")
	case prev != "user" && prev != "tool", next != "" && next != "assistant":
		return nil, &NotSupported{Reason: "a system message where Anthropic does not allow one"}
	case prev == "tool" && m.t.UntrustedSystemAfterTools == catalog.ClaimYes:
		return nil, &NotSupported{Reason: "the model does not trust a system message right after tool results"}
	case prev == "tool" && m.t.UntrustedSystemAfterTools == catalog.ClaimUnset:
		return nil, unspecified("a system message right after tool results", "whether the model trusts one (untrusted_system_after_tools)")
	}
	return turn(out, "system", msgs[i])
}

// assistantTurn writes an assistant message: the reasoning to replay first, then its text and tool calls. One with
// none of them writes nothing.
func assistantTurn(out []byte, msg canonMessage) ([]byte, error) {
	begin := len(out)
	out = append(out, `{"role":"assistant","content":[`...)
	first := len(out)
	for _, d := range replayable(msg.ReasoningDetails) {
		out = commaAfter(out, first)
		if d.Type == "reasoning.encrypted" {
			out = append(append(append(out, `{"type":"redacted_thinking","data":`...), d.Data...), '}')
			continue
		}
		out = append(append(append(out, `{"type":"thinking","thinking":`...), d.Text...), `,"signature":`...)
		out = append(append(out, d.Signature...), '}')
	}
	b, err := blocks(msg, true)
	if err != nil {
		return nil, err
	}
	if len(b) > 0 {
		out = append(commaAfter(out, first), b...)
	}
	for _, call := range msg.ToolCalls {
		input, err := arguments(call.Function.Arguments)
		if err != nil {
			return nil, err
		}
		out = appendQuoted(append(commaAfter(out, first), `{"type":"tool_use","id":`...), toolUseID(call.ID))
		out = append(append(append(append(out, `,"name":`...), call.Function.Name...), `,"input":`...), input...)
		out = append(out, '}')
	}
	if len(out) == first {
		return out[:begin], nil
	}
	return append(out, "]}"...), nil
}

// commaAfter appends a comma when out holds something after first.
func commaAfter(out []byte, first int) []byte {
	if len(out) > first {
		return append(out, ',')
	}
	return out
}

// toolResults writes consecutive tool messages as one user message of tool_result blocks.
func toolResults(out []byte, msgs []canonMessage) ([]byte, error) {
	out = append(out, `{"role":"user","content":[`...)
	for i, msg := range msgs {
		if i > 0 {
			out = append(out, ',')
		}
		out = appendQuoted(append(out, `{"type":"tool_result","tool_use_id":`...), toolUseID(*msg.ToolCallID))
		out = append(out, `,"content":`...)
		if msg.Content.Kind() == '"' {
			out = append(out, msg.Content...)
		} else {
			b, err := blocks(msg, false)
			if err != nil {
				return nil, err
			}
			out = append(append(append(out, '['), b...), ']')
		}
		out = append(out, '}')
	}
	return append(out, "]}"...), nil
}

// blocks writes a message's content parts as content blocks, comma-separated; text alone when textOnly. Empty text
// writes nothing, as Anthropic refuses an empty text block.
func blocks(msg canonMessage, textOnly bool) ([]byte, error) {
	var out []byte
	var err error
	for _, p := range msg.Parts {
		if (p.Type == "text" || p.Type == "refusal") && (string(p.Text) == `""` || isNull(p.Text)) {
			continue
		}
		if len(out) > 0 {
			out = append(out, ',')
		}
		if out, err = block(out, p, textOnly); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func block(out []byte, p canonPart, textOnly bool) ([]byte, error) {
	var err error
	switch {
	case p.Type == "text", p.Type == "refusal": // a refusal is text the assistant said: Anthropic's own come as text
		out = append(append(out, messagesTextHead...), p.Text...)
	case textOnly:
		return nil, &NotSupported{Reason: "a " + p.Type + " part where Messages takes text only"}
	case p.Type == "image_url" && p.ImageURL != nil:
		out, err = imageBlock(out, p)
	case p.Type == "file" && p.File != nil && p.File.FileData != nil:
		out, err = documentBlock(out, *p.File.FileData)
	default:
		return nil, &NotSupported{Reason: "a " + p.Type + " part has no Messages counterpart"}
	}
	if err != nil {
		return nil, err
	}
	if !isNull(p.CacheControl) {
		out = append(append(out, `,"cache_control":`...), p.CacheControl...)
	}
	return append(out, '}'), nil
}

// imageBlock writes an image block but its closing brace: base64 for a data URL, a URL otherwise; Anthropic has no
// low detail.
func imageBlock(out []byte, p canonPart) ([]byte, error) {
	if p.ImageURL.Detail == "low" {
		return nil, &NotSupported{Reason: "image detail low has no Messages counterpart"}
	}
	out = append(out, messagesImageHead...)
	if media, data, ok := dataURL(p.ImageURL.URL); ok {
		out = appendQuoted(append(out, `{"type":"base64","media_type":`...), media)
		out = appendQuoted(append(out, `,"data":`...), data)
	} else {
		out = appendQuoted(append(out, `{"type":"url","url":`...), p.ImageURL.URL)
	}
	return append(out, '}'), nil
}

// documentBlock writes a PDF's data URL as a document block but its closing brace.
func documentBlock(out []byte, fileData string) ([]byte, error) {
	media, data, ok := dataURL(fileData)
	if !ok || media != "application/pdf" {
		return nil, &NotSupported{Reason: "a file that is not PDF data"}
	}
	out = append(out, messagesFileHead...)
	return append(appendQuoted(out, data), '}'), nil
}

// tools writes the function tools.
func (m *messagesWriter) tools(out []byte) ([]byte, error) {
	if len(m.c.Tools) == 0 {
		return out, nil
	}
	out = append(out, `,"tools":[`...)
	for i, tool := range m.c.Tools {
		if i > 0 {
			out = append(out, ',')
		}
		var err error
		if out, err = messagesTool(out, tool); err != nil {
			return nil, err
		}
	}
	return append(out, ']'), nil
}

// messagesTool writes a function tool; a strict one only with a schema in Anthropic's subset.
func messagesTool(out []byte, tool canonTool) ([]byte, error) {
	if tool.Type != "function" {
		return nil, &NotSupported{Reason: "a " + tool.Type + " tool has no Messages counterpart"}
	}
	f := tool.Function
	out = append(append(out, `{"name":`...), f.Name...)
	if !isNull(f.Description) {
		out = append(append(out, `,"description":`...), f.Description...)
	}
	schema := f.Parameters
	if isNull(schema) {
		schema = jsontext.Value(`{"type":"object","properties":{}}`)
	}
	out = append(append(out, `,"input_schema":`...), schema...)
	if f.Strict != nil && *f.Strict {
		if err := anthropicSchema(schema); err != nil {
			return nil, err
		}
		out = append(out, `,"strict":true`...)
	}
	if !isNull(tool.CacheControl) {
		out = append(append(out, `,"cache_control":`...), tool.CacheControl...)
	}
	return append(out, '}'), nil
}

// toolChoice maps tool_choice and parallel_tool_calls. Anthropic refuses a forced choice while thinking, and some
// models (Claude Opus 5.5, Sonnet 5.5 and Fable 5.1) refuse one at all.
func (m *messagesWriter) toolChoice(out []byte) ([]byte, error) {
	choice, forced, err := messagesChoice(m.c.ToolChoice)
	if err != nil {
		return nil, err
	}
	if forced {
		if err := m.forcedRefusal(); err != nil {
			return nil, err
		}
	}
	parallelOff := m.c.ParallelToolCalls != nil && !*m.c.ParallelToolCalls
	if isNull(m.c.ToolChoice) && !parallelOff || len(m.c.Tools) == 0 {
		return out, nil
	}
	if parallelOff {
		choice += `,"disable_parallel_tool_use":true`
	}
	return append(append(append(out, `,"tool_choice":`...), choice...), '}'), nil
}

// messagesChoice is tool_choice in Messages' terms, but its closing brace, and whether it forces a tool.
func messagesChoice(v jsontext.Value) (choice string, forced bool, err error) {
	if isNull(v) {
		return messagesChoices[`"auto"`], false, nil
	}
	if choice := messagesChoices[string(v)]; choice != "" {
		return choice, string(v) == `"required"`, nil
	}
	var named struct {
		Function struct {
			Name jsontext.Value `json:"name"`
		} `json:"function"`
	}
	if err := jsonx.Unmarshal(v, &named); err != nil || isNull(named.Function.Name) {
		return "", false, &NotSupported{Reason: "a tool_choice Messages cannot express"}
	}
	return `{"type":"tool","name":` + string(named.Function.Name), true, nil
}

var messagesChoices = map[string]string{
	`"auto"`:     `{"type":"` + choiceNames["auto"] + `"`,
	`"none"`:     `{"type":"` + choiceNames["none"] + `"`,
	`"required"`: `{"type":"` + choiceNames["required"] + `"`,
}

// thinking writes the thinking setting and output_config: the effort of an adaptive model, and the schema of a
// structured reply.
func (m *messagesWriter) thinking(out []byte) ([]byte, error) {
	adaptive := m.thinkingOn && m.t.Thinking == catalog.ThinkingAdaptive
	switch {
	case adaptive:
		out = append(out, `,"thinking":{"type":"adaptive"}`...)
	case m.thinkingOn:
		out = strconv.AppendInt(append(out, `,"thinking":{"type":"enabled","budget_tokens":`...), m.budget, 10)
		out = append(out, '}')
	}
	format, err := m.replyFormat()
	if err != nil {
		return nil, err
	}
	switch {
	case adaptive && format != "":
		out = append(append(append(out, `,"output_config":{"effort":"`...), m.effort...), `",`...)
		out = append(append(out, format...), '}')
	case adaptive:
		out = append(append(append(out, `,"output_config":{"effort":"`...), m.effort...), `"}`...)
	case format != "":
		out = append(append(append(out, `,"output_config":{`...), format...), '}')
	}
	return out, nil
}

// replyFormat is output_config's format member for a structured reply, "" for none.
func (m *messagesWriter) replyFormat() (string, error) {
	f := m.c.ResponseFormat
	switch {
	case f == nil || f.Type == "text":
		return "", nil
	case f.Type != "json_schema" || f.JSONSchema == nil:
		return "", &NotSupported{Reason: "response_format " + f.Type + " has no Messages counterpart"}
	}
	if err := anthropicSchema(f.JSONSchema.Schema); err != nil {
		return "", err
	}
	return `"format":{"type":"json_schema","schema":` + string(f.JSONSchema.Schema) + `}`, nil
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
func (m *messagesWriter) sampling(out []byte) ([]byte, error) {
	c := m.c
	for _, s := range []struct {
		name string
		v    jsontext.Value
	}{{"temperature", c.Temperature}, {"top_p", c.TopP}} {
		if numberIs(s.v, 1) {
			continue
		}
		if err := m.samplingRefusal(s.name + " other than the default"); err != nil {
			return nil, err
		}
		if f, err := strconv.ParseFloat(string(s.v), 64); err != nil || f > 1 || f < 0 {
			return nil, &NotSupported{Reason: s.name + " outside 0 to 1"}
		}
		out = append(append(append(append(out, ',', '"'), s.name...), `":`...), s.v...)
	}
	if c.Stop.Kind() == '"' {
		out = append(append(append(out, `,"stop_sequences":[`...), c.Stop...), ']')
	} else if c.Stop.Kind() == '[' {
		out = append(append(out, `,"stop_sequences":`...), c.Stop...)
	}
	if c.ServiceTier != nil {
		tier := `"auto"`
		if *c.ServiceTier == "default" {
			tier = `"standard_only"`
		}
		out = append(append(out, `,"service_tier":`...), tier...)
	}
	return out, nil
}
