package protocol

import (
	"cmp"
	"encoding/json/jsontext" //nolint:depguard // as in canon.go: an ID is decoded only to match it
	"errors"

	"axiom/internal/catalog"
)

// toResponses writes a Chat request as an OpenAI Responses request (OpenAI《Create a model response》, read
// 2026-10-14; README.md「跨格式转换」). Strings are copied as written. The upstream stores nothing, as for a Responses
// client (README.md「同格式改写」). The body is written into one writer, as toMessages's is.
func toResponses(c *canon, t Target, w Rewrites, b *budget, body []byte) ([]byte, error) {
	if !isNull(c.Stop) {
		return nil, &NotSupported{Reason: "stop has no Responses counterpart"}
	}
	effort, err := c.targetEffort(cmp.Or(c.maxOutput(), t.MaxOutput))
	if err != nil {
		return nil, err
	}
	o := newWriter(len(body)+4096, b)
	o.str(`{"model":`).quoted(t.Model).str(`,"input":[`)
	if err := input(o, c, t.CacheBreakpoints == catalog.ClaimYes); err != nil {
		return nil, err
	}
	o.str(`],"store":false`)
	if t.EncryptedReasoning == catalog.ClaimYes {
		o.str(`,"include":["` + encrypted + `"]`)
	}
	for _, write := range []func(*writer, *canon) error{responsesTools, responsesChoice, responsesText} {
		if err := write(o, c); err != nil {
			return nil, err
		}
	}
	responsesSettings(o, c, t, effort)
	for _, id := range []struct {
		name  string
		value *string
	}{{"user", c.User}, {"safety_identifier", c.SafetyIdentifier}, {"prompt_cache_key", c.PromptCacheKey}} {
		if id.value != nil {
			o.str(`,"`).str(id.name).str(`":`).quoted(w.Identity(*id.value))
		}
	}
	o.byte('}')
	return o.buf, o.err
}

// input writes the messages as input items; breakpoints says whether the model takes OpenAI's cache breakpoints.
func input(o *writer, c *canon, breakpoints bool) error {
	for i, msg := range c.Messages {
		if i > 0 {
			o.byte(',')
		}
		if err := inputItems(o, msg, breakpoints); err != nil {
			return err
		}
		if o.err != nil {
			return o.err
		}
	}
	return nil
}

// responsesSettings writes the cap, sampling, reasoning, streaming and service tier. A request that sets no cap gets
// t.MaxOutput, if there is one.
func responsesSettings(o *writer, c *canon, t Target, effort string) {
	if n := cmp.Or(c.maxOutput(), t.MaxOutput); n > 0 {
		o.str(`,"max_output_tokens":`).int(n)
	}
	for _, member := range []struct {
		name  string
		value []byte
	}{{"temperature", c.Temperature}, {"top_p", c.TopP}} {
		if !isNull(member.value) {
			o.str(`,"`).str(member.name).str(`":`).raw(member.value)
		}
	}
	writeResponsesReasoning(o, c, effort)
	if t.Streams { // the gateway reads every answer as a stream, and routing sends a conversion only to an endpoint that streams
		o.str(`,"stream":true`)
	}
	if c.ServiceTier != nil {
		o.str(`,"service_tier":`).quoted(*c.ServiceTier)
	}
}

func writeResponsesReasoning(o *writer, c *canon, effort string) {
	if effort != "" {
		o.str(`,"reasoning":{"effort":`).quoted(effort)
		if effort != "none" && (c.Reasoning == nil || c.Reasoning.Exclude == nil || !*c.Reasoning.Exclude) {
			o.str(`,"summary":"auto"`) // so that the client sees the reasoning, as Chat clients do
		}
		o.byte('}')
	}
	if c.effort() == "" && c.Reasoning != nil && c.Reasoning.Exclude != nil && !*c.Reasoning.Exclude {
		o.str(`,"reasoning":{"summary":"auto"}`)
	}
}

// inputItems writes one message as input items: system and developer messages in place, the reasoning an assistant
// message hands back first, then its text and function calls.
func inputItems(o *writer, msg canonMessage, breakpoints bool) error {
	switch msg.Role {
	case "system", "developer", "user":
		o.str(`{"type":"message","role":"`).str(msg.Role).str(`","content":[`)
		if err := inputParts(o, msg, breakpoints); err != nil {
			return err
		}
		o.str("]}")
		return nil
	case "tool":
		o.str(`{"type":"function_call_output","call_id":`).quoted(*msg.ToolCallID).str(`,"output":`)
		if msg.Content.Kind() == '"' {
			o.raw(msg.Content).byte('}')
			return nil
		}
		o.byte('[')
		if err := inputParts(o, msg, breakpoints); err != nil {
			return err
		}
		o.str("]}")
		return nil
	case "assistant":
		return assistantItems(o, msg, breakpoints)
	}
	return &NotSupported{Reason: "role " + msg.Role + " has no Responses counterpart"}
}

// inputParts writes a message's content parts as input content, comma-separated.
func inputParts(o *writer, msg canonMessage, breakpoints bool) error {
	for i, p := range msg.Parts {
		if i > 0 {
			o.byte(',')
		}
		if err := inputPart(o, p, breakpoints); err != nil {
			return err
		}
		if o.err != nil {
			return o.err
		}
	}
	return nil
}

// writeBreakpoint writes the cache breakpoint a text part's cache_control becomes on a model that takes them. Its TTL
// and every other marker are left out, as OpenRouter does (docs/domains/gateway.md「转发规则」).
func writeBreakpoint(o *writer, p canonPart, breakpoints bool) {
	if breakpoints && !isNull(p.CacheControl) {
		o.str(`,"prompt_cache_breakpoint":{"mode":"explicit"}`)
	}
}

// inputPart writes one part.
func inputPart(o *writer, p canonPart, breakpoints bool) error {
	switch {
	case p.Type == "text":
		o.str(responsesTextHead).raw(p.Text)
		writeBreakpoint(o, p, breakpoints)
		o.byte('}')
		return nil
	case p.Type == "image_url" && p.ImageURL != nil:
		o.str(responsesImageHead).quoted(p.ImageURL.URL)
		if p.ImageURL.Detail != "" {
			o.str(`,"detail":`).quoted(p.ImageURL.Detail)
		}
		o.byte('}')
		return nil
	case p.Type == "file" && p.File != nil:
		if ok, err := inputFilePart(o, p.File.FileData, p.File.FileID, p.File.Filename); ok || err != nil {
			return err
		}
	}
	return &NotSupported{Reason: "a " + p.Type + " part has no Responses counterpart"}
}

// inputFilePart writes a file part: the data inline, or the id the upstream issued sent back as it is, never as content
// and never to another provider (routing keeps it to the client's pin). It reports false when the part has neither.
func inputFilePart(o *writer, data, id *string, filename jsontext.Value) (bool, error) {
	switch {
	case data != nil && id != nil:
		return false, &NotSupported{Reason: "a file part carries both data and an id"}
	case data != nil:
		o.str(responsesFileHead).quoted(*data)
	case id != nil:
		o.str(responsesFileIDHead).quoted(*id)
	default:
		return false, nil
	}
	if !isNull(filename) {
		o.str(`,"filename":`).raw(filename)
	}
	o.byte('}')
	return true, nil
}

// assistantItems writes an assistant message: each reasoning item it hands back with encrypted content, which is
// all the upstream can use when it stores nothing, then its text and function calls. An empty turn still takes its
// place in the conversation.
func assistantItems(o *writer, msg canonMessage, breakpoints bool) error {
	start := len(o.buf)
	if err := reasoningItems(o, msg.ReasoningDetails); err != nil {
		return err
	}
	mark := len(o.buf)
	o.commaAfter(start).str(`{"type":"message","role":"assistant","content":[`)
	at := len(o.buf)
	if err := outputText(o, msg, breakpoints); err != nil {
		return err
	}
	switch {
	case len(o.buf) > at:
		o.str("]}")
	case len(msg.ToolCalls) == 0 && mark == start:
		o.str(`{"type":"output_text","text":""}]}`)
	default:
		o.cut(mark)
	}
	for _, call := range msg.ToolCalls {
		o.commaAfter(start).str(`{"type":"function_call","call_id":`).quoted(call.ID)
		o.str(`,"name":`).raw(call.Function.Name).str(`,"arguments":`).raw(call.Function.Arguments).byte('}')
	}
	return o.err
}

// outputText writes an assistant message's text as output_text parts, comma-separated; empty text writes nothing.
func outputText(o *writer, msg canonMessage, breakpoints bool) error {
	start := len(o.buf)
	for _, p := range msg.Parts {
		switch {
		case p.Type == "refusal":
			o.commaAfter(start).str(responsesRefusalHead).raw(p.Text).byte('}')
		case p.Type != "text":
			return &NotSupported{Reason: "a " + p.Type + " part in an assistant message"}
		case string(p.Text) == `""`:
		default:
			o.commaAfter(start).str(`{"type":"output_text","text":`).raw(p.Text)
			writeBreakpoint(o, p, breakpoints)
			o.byte('}')
		}
	}
	return nil
}

// reasoningItems writes the reasoning items of OpenRouter's openai-responses-v1 entries, gathered by item ID in their
// order, comma-separated. An item without encrypted content is left out: the upstream stores nothing to look its ID
// up in. Each item's summary is gathered in a writer of its own, drawn from the same budget.
func reasoningItems(o *writer, details []canonReasoningDetail) error {
	gathered, err := gatherItems(details, o.b)
	if err != nil {
		return err
	}
	start := len(o.buf)
	for _, item := range gathered {
		if item.encrypted != nil {
			o.commaAfter(start).str(`{"type":"reasoning","id":`).raw(item.id).str(`,"summary":[`).raw(item.summary.buf)
			o.str(`],"encrypted_content":`).raw(item.encrypted).byte('}')
		}
		o.b.give(rounded(int64(cap(item.summary.buf))))
	}
	return o.err
}

// gatherItems gathers the entries of each item, by the ID decoded, so that two ways of escaping it are one item.
func gatherItems(details []canonReasoningDetail, b *budget) ([]reasoningItem, error) {
	var gathered []reasoningItem
	var at map[string]int
	for _, d := range details {
		if d.Format != "openai-responses-v1" || isNull(d.ID) {
			continue
		}
		i, err := itemIndex(&at, &gathered, d.ID, b)
		if err != nil {
			return nil, err
		}
		item := &gathered[i]
		switch {
		case d.Type == "reasoning.summary" && !isNull(d.Summary):
			item.summary.commaAfter(0).str(`{"type":"summary_text","text":`).raw(d.Summary).byte('}')
			if item.summary.err != nil {
				return nil, item.summary.err
			}
		case d.Type == "reasoning.encrypted" && !isNull(d.Data):
			item.encrypted = d.Data
		}
	}
	return gathered, nil
}

// itemIndex is where the item of id is in gathered, adding it when it is new.
func itemIndex(at *map[string]int, gathered *[]reasoningItem, id jsontext.Value, b *budget) (int, error) {
	if err := b.draw(rounded(int64(len(id)))); err != nil {
		return 0, err
	}
	defer b.give(rounded(int64(len(id)))) // the decoded key is kept by the map, its scratch is not
	key, err := jsontext.AppendUnquote(make([]byte, 0, len(id)), id)
	if err != nil {
		key = id // not a string: matched as written
	}
	if i, seen := (*at)[string(key)]; seen {
		return i, nil
	}
	if *at == nil {
		if err := b.draw(mapMemory); err != nil {
			return 0, err
		}
		*at = map[string]int{}
	}
	if err := b.draw(mapEntryMemory + rounded(int64(len(key)))); err != nil {
		return 0, err
	}
	i := len(*gathered)
	(*at)[string(key)] = i
	if *gathered, err = growWithin(*gathered, b); err != nil {
		return 0, err
	}
	(*gathered)[i] = reasoningItem{id: id, summary: writer{b: b}}
	return i, nil
}

// reasoningItem gathers an item's summaries and encrypted content.
type reasoningItem struct {
	id, encrypted jsontext.Value
	summary       writer
}

// responsesTools writes the function tools. A tool's strict is written even when Chat leaves it out, as false, which
// Chat's omission means: Responses would take its omission as true and rewrite the schema (OpenAI《Function calling》
// "Strict mode", read 2026-10-14).
func responsesTools(o *writer, c *canon) error {
	if len(c.Tools) == 0 {
		return nil
	}
	o.str(`,"tools":[`)
	for i, tool := range c.Tools {
		if tool.Type != "function" {
			return &NotSupported{Reason: "a " + tool.Type + " tool has no Responses counterpart"}
		}
		if i > 0 {
			o.byte(',')
		}
		f := tool.Function
		o.str(`{"type":"function","name":`).raw(f.Name)
		if !isNull(f.Description) {
			o.str(`,"description":`).raw(f.Description)
		}
		if !isNull(f.Parameters) {
			o.str(`,"parameters":`).raw(f.Parameters)
		}
		o.str(`,"strict":`).bool(f.Strict != nil && *f.Strict).byte('}')
	}
	o.byte(']')
	return o.err
}

// responsesChoice writes tool_choice and parallel_tool_calls.
func responsesChoice(o *writer, c *canon) error {
	switch v := c.ToolChoice; {
	case isNull(v):
	case v.Kind() == '"':
		o.str(`,"tool_choice":`).raw(v)
	default:
		var named struct {
			Function struct {
				Name *string `json:"name"`
			} `json:"function"`
		}
		if err := unmarshalWithin(v, &named, o.b); err != nil || named.Function.Name == nil {
			if errors.Is(err, ErrMemory) {
				return err
			}
			return &NotSupported{Reason: "a tool_choice Responses cannot express"}
		}
		o.str(`,"tool_choice":{"type":"function","name":`).quoted(*named.Function.Name).byte('}')
	}
	if c.ParallelToolCalls != nil {
		o.str(`,"parallel_tool_calls":`).bool(*c.ParallelToolCalls)
	}
	return o.err
}

// responsesText writes the reply's format and verbosity as text.
func responsesText(o *writer, c *canon) error {
	mark := len(o.buf)
	o.str(`,"text":{`)
	first := len(o.buf)
	if f := c.ResponseFormat; f != nil && f.Type != "text" {
		switch {
		case f.Type == "json_object":
			o.str(`"format":{"type":"json_object"}`)
		case f.Type == "json_schema" && f.JSONSchema != nil:
			s := f.JSONSchema
			o.str(`"format":{"type":"json_schema","name":`).raw(s.Name).str(`,"schema":`).raw(s.Schema)
			if !isNull(s.Description) {
				o.str(`,"description":`).raw(s.Description)
			}
			if s.Strict != nil {
				o.str(`,"strict":`).bool(*s.Strict)
			}
			o.byte('}')
		default:
			return &NotSupported{Reason: "response_format " + f.Type + " has no Responses counterpart"}
		}
	}
	if !isNull(c.Verbosity) {
		o.commaAfter(first).str(`"verbosity":`).raw(c.Verbosity)
	}
	if len(o.buf) == first {
		o.cut(mark)
		return o.err
	}
	o.byte('}')
	return o.err
}
