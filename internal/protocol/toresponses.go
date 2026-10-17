package protocol

import (
	"cmp"
	"encoding/json/jsontext" //nolint:depguard // as in canon.go: an ID is decoded only to match it
	"strconv"

	"axiom/internal/platform/jsonx"
)

// toResponses writes a Chat request as an OpenAI Responses request (OpenAI《Create a model response》, read
// 2026-10-14; README.md「跨格式转换」). Strings are copied as written. The upstream stores nothing, as for a Responses
// client (README.md「同格式改写」).
func toResponses(c *canon, t Target, w Rewrites) ([]byte, error) {
	if !isNull(c.Stop) {
		return nil, &NotSupported{Reason: "stop has no Responses counterpart"}
	}
	if c.budget() != nil {
		return nil, &NotSupported{Reason: "a reasoning budget has no Responses counterpart"}
	}
	out := appendQuoted(append([]byte(nil), `{"model":`...), t.Model)
	out, err := input(append(out, `,"input":[`...), c, t.CacheBreakpoints)
	if err != nil {
		return nil, err
	}
	out = append(out, `],"store":false`...)
	if t.EncryptedReasoning {
		out = append(out, `,"include":["`+encrypted+`"]`...)
	}
	for _, write := range []func([]byte, *canon) ([]byte, error){responsesTools, responsesChoice, responsesText} {
		if out, err = write(out, c); err != nil {
			return nil, err
		}
	}
	out = responsesSettings(out, c, t)
	for _, id := range []struct {
		name  string
		value *string
	}{{"user", c.User}, {"safety_identifier", c.SafetyIdentifier}, {"prompt_cache_key", c.PromptCacheKey}} {
		if id.value != nil {
			out = appendQuoted(append(append(append(out, ',', '"'), id.name...), `":`...), w.Identity(*id.value))
		}
	}
	return append(out, '}'), nil
}

// input writes the messages as input items; breakpoints says whether the model takes OpenAI's cache breakpoints.
func input(out []byte, c *canon, breakpoints bool) ([]byte, error) {
	var err error
	for i, msg := range c.Messages {
		if i > 0 {
			out = append(out, ',')
		}
		if out, err = inputItems(out, msg, breakpoints); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// responsesSettings writes the cap, sampling, reasoning, streaming and service tier. A request that sets no cap gets
// t.MaxOutput, if there is one.
func responsesSettings(out []byte, c *canon, t Target) []byte {
	if n := cmp.Or(c.maxOutput(), t.MaxOutput); n > 0 {
		out = strconv.AppendInt(append(out, `,"max_output_tokens":`...), n, 10)
	}
	for _, member := range []struct {
		name  string
		value []byte
	}{{"temperature", c.Temperature}, {"top_p", c.TopP}} {
		if !isNull(member.value) {
			out = append(append(append(append(out, ',', '"'), member.name...), `":`...), member.value...)
		}
	}
	if effort := c.effort(); effort != "" {
		out = appendQuoted(append(out, `,"reasoning":{"effort":`...), effort)
		if c.Reasoning == nil || c.Reasoning.Exclude == nil || !*c.Reasoning.Exclude {
			out = append(out, `,"summary":"auto"`...) // so that the client sees the reasoning, as Chat clients do
		}
		out = append(out, '}')
	}
	if t.Streams { // the gateway reads every answer as a stream, and routing sends a conversion only to an endpoint that streams
		out = append(out, `,"stream":true`...)
	}
	if c.ServiceTier != nil {
		out = appendQuoted(append(out, `,"service_tier":`...), *c.ServiceTier)
	}
	return out
}

// inputItems writes one message as input items: system and developer messages in place, the reasoning an assistant
// message hands back first, then its text and function calls.
func inputItems(out []byte, msg canonMessage, breakpoints bool) ([]byte, error) {
	switch msg.Role {
	case "system", "developer", "user":
		out = append(append(append(out, `{"type":"message","role":"`...), msg.Role...), `","content":[`...)
		out, err := inputParts(out, msg, breakpoints)
		if err != nil {
			return nil, err
		}
		return append(out, "]}"...), nil
	case "tool":
		out = appendQuoted(append(out, `{"type":"function_call_output","call_id":`...), *msg.ToolCallID)
		out = append(out, `,"output":`...)
		if msg.Content.Kind() == '"' {
			return append(append(out, msg.Content...), '}'), nil
		}
		parts, err := inputParts(nil, msg, breakpoints)
		if err != nil {
			return nil, err
		}
		return append(append(append(out, '['), parts...), "]}"...), nil
	case "assistant":
		return assistantItems(out, msg, breakpoints)
	}
	return nil, &NotSupported{Reason: "role " + msg.Role + " has no Responses counterpart"}
}

// inputParts writes a message's content parts as input content, comma-separated.
func inputParts(out []byte, msg canonMessage, breakpoints bool) ([]byte, error) {
	var err error
	for i, p := range msg.Parts {
		if i > 0 {
			out = append(out, ',')
		}
		if out, err = inputPart(out, p, breakpoints); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// appendBreakpoint appends the cache breakpoint a text part's cache_control becomes on a model that takes them. Its TTL
// and every other marker are left out, as OpenRouter does (docs/domains/gateway.md「转发规则」).
func appendBreakpoint(out []byte, p canonPart, breakpoints bool) []byte {
	if breakpoints && !isNull(p.CacheControl) {
		out = append(out, `,"prompt_cache_breakpoint":{"mode":"explicit"}`...)
	}
	return out
}

// inputPart writes one part.
func inputPart(out []byte, p canonPart, breakpoints bool) ([]byte, error) {
	switch {
	case p.Type == "text":
		out = append(append(out, `{"type":"input_text","text":`...), p.Text...)
		return append(appendBreakpoint(out, p, breakpoints), '}'), nil
	case p.Type == "image_url" && p.ImageURL != nil:
		out = appendQuoted(append(out, `{"type":"input_image","image_url":`...), p.ImageURL.URL)
		if p.ImageURL.Detail != "" {
			out = appendQuoted(append(out, `,"detail":`...), p.ImageURL.Detail)
		}
		return append(out, '}'), nil
	case p.Type == "file" && p.File != nil && p.File.FileData != nil:
		out = appendQuoted(append(out, `{"type":"input_file","file_data":`...), *p.File.FileData)
		if !isNull(p.File.Filename) {
			out = append(append(out, `,"filename":`...), p.File.Filename...)
		}
		return append(out, '}'), nil
	}
	return nil, &NotSupported{Reason: "a " + p.Type + " part has no Responses counterpart"}
}

// assistantItems writes an assistant message: each reasoning item it hands back with encrypted content, which is
// all the upstream can use when it stores nothing, then its text and function calls. An empty turn still takes its
// place in the conversation.
func assistantItems(out []byte, msg canonMessage, breakpoints bool) ([]byte, error) {
	start := len(out)
	for _, item := range reasoningItems(msg.ReasoningDetails) {
		out = append(commaAfter(out, start), item...)
	}
	text, err := outputText(msg, breakpoints)
	if err != nil {
		return nil, err
	}
	if len(text) == 0 && len(msg.ToolCalls) == 0 && len(out) == start {
		text = []byte(`{"type":"output_text","text":""}`)
	}
	if len(text) > 0 {
		out = append(commaAfter(out, start), `{"type":"message","role":"assistant","content":[`...)
		out = append(append(out, text...), "]}"...)
	}
	for _, call := range msg.ToolCalls {
		out = appendQuoted(append(commaAfter(out, start), `{"type":"function_call","call_id":`...), call.ID)
		out = append(append(append(append(out, `,"name":`...), call.Function.Name...), `,"arguments":`...), call.Function.Arguments...)
		out = append(out, '}')
	}
	return out, nil
}

// outputText is an assistant message's text as output_text parts, comma-separated; empty text writes nothing.
func outputText(msg canonMessage, breakpoints bool) ([]byte, error) {
	var text []byte
	for _, p := range msg.Parts {
		switch {
		case p.Type == "refusal":
			text = append(append(append(commaAfter(text, 0), `{"type":"refusal","refusal":`...), p.Text...), '}')
		case p.Type != "text":
			return nil, &NotSupported{Reason: "a " + p.Type + " part in an assistant message"}
		case string(p.Text) == `""`:
		default:
			text = append(append(commaAfter(text, 0), `{"type":"output_text","text":`...), p.Text...)
			text = append(appendBreakpoint(text, p, breakpoints), '}')
		}
	}
	return text, nil
}

// reasoningItem gathers an item's summaries and encrypted content.
type reasoningItem struct{ id, summary, encrypted []byte }

// reasoningItems are the reasoning items of OpenRouter's openai-responses-v1 entries, gathered by item ID in their
// order. An item without encrypted content is left out: the upstream stores nothing to look its ID up in.
func reasoningItems(details []canonReasoningDetail) [][]byte {
	var gathered []*reasoningItem
	at := map[string]int{} // by the ID decoded, so that two ways of escaping it are one item
	for _, d := range details {
		if d.Format != "openai-responses-v1" || isNull(d.ID) {
			continue
		}
		key, err := jsontext.AppendUnquote(nil, d.ID)
		if err != nil {
			key = d.ID // not a string: matched as written
		}
		i, seen := at[string(key)]
		if !seen {
			i = len(gathered)
			at[string(key)] = i
			gathered = append(gathered, &reasoningItem{id: d.ID})
		}
		gathered[i].add(d)
	}
	var items [][]byte
	for _, item := range gathered {
		if item.encrypted != nil {
			b := append(append(append([]byte(`{"type":"reasoning","id":`), item.id...), `,"summary":[`...), item.summary...)
			items = append(items, append(append(append(b, `],"encrypted_content":`...), item.encrypted...), '}'))
		}
	}
	return items
}

func (item *reasoningItem) add(d canonReasoningDetail) {
	switch {
	case d.Type == "reasoning.summary" && !isNull(d.Summary):
		item.summary = append(commaAfter(item.summary, 0), `{"type":"summary_text","text":`...)
		item.summary = append(append(item.summary, d.Summary...), '}')
	case d.Type == "reasoning.encrypted" && !isNull(d.Data):
		item.encrypted = d.Data
	}
}

// responsesTools writes the function tools. A tool's strict is written even when Chat leaves it out, as false, which
// Chat's omission means: Responses would take its omission as true and rewrite the schema (OpenAI《Function calling》
// "Strict mode", read 2026-10-14).
func responsesTools(out []byte, c *canon) ([]byte, error) {
	if len(c.Tools) == 0 {
		return out, nil
	}
	out = append(out, `,"tools":[`...)
	for i, tool := range c.Tools {
		if tool.Type != "function" {
			return nil, &NotSupported{Reason: "a " + tool.Type + " tool has no Responses counterpart"}
		}
		if i > 0 {
			out = append(out, ',')
		}
		f := tool.Function
		out = append(append(out, `{"type":"function","name":`...), f.Name...)
		if !isNull(f.Description) {
			out = append(append(out, `,"description":`...), f.Description...)
		}
		if !isNull(f.Parameters) {
			out = append(append(out, `,"parameters":`...), f.Parameters...)
		}
		out = strconv.AppendBool(append(out, `,"strict":`...), f.Strict != nil && *f.Strict)
		out = append(out, '}')
	}
	return append(out, ']'), nil
}

// responsesChoice writes tool_choice and parallel_tool_calls.
func responsesChoice(out []byte, c *canon) ([]byte, error) {
	switch v := c.ToolChoice; {
	case isNull(v):
	case v.Kind() == '"':
		out = append(append(out, `,"tool_choice":`...), v...)
	default:
		var named struct {
			Function struct {
				Name *string `json:"name"`
			} `json:"function"`
		}
		if err := jsonx.Unmarshal(v, &named); err != nil || named.Function.Name == nil {
			return nil, &NotSupported{Reason: "a tool_choice Responses cannot express"}
		}
		out = appendQuoted(append(out, `,"tool_choice":{"type":"function","name":`...), *named.Function.Name)
		out = append(out, '}')
	}
	if c.ParallelToolCalls != nil {
		out = strconv.AppendBool(append(out, `,"parallel_tool_calls":`...), *c.ParallelToolCalls)
	}
	return out, nil
}

// responsesText writes the reply's format and verbosity as text.
func responsesText(out []byte, c *canon) ([]byte, error) {
	var members []byte
	if f := c.ResponseFormat; f != nil && f.Type != "text" {
		switch {
		case f.Type == "json_object":
			members = append(members, `"format":{"type":"json_object"}`...)
		case f.Type == "json_schema" && f.JSONSchema != nil:
			s := f.JSONSchema
			members = append(append(members, `"format":{"type":"json_schema","name":`...), s.Name...)
			members = append(append(members, `,"schema":`...), s.Schema...)
			if !isNull(s.Description) {
				members = append(append(members, `,"description":`...), s.Description...)
			}
			if s.Strict != nil {
				members = strconv.AppendBool(append(members, `,"strict":`...), *s.Strict)
			}
			members = append(members, '}')
		default:
			return nil, &NotSupported{Reason: "response_format " + f.Type + " has no Responses counterpart"}
		}
	}
	if !isNull(c.Verbosity) {
		members = append(append(commaAfter(members, 0), `"verbosity":`...), c.Verbosity...)
	}
	if len(members) == 0 {
		return out, nil
	}
	return append(append(append(out, `,"text":{`...), members...), '}'), nil
}
