package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"errors"

	"axiom/internal/platform/jsonx"
)

// messagesRequestToChat inverts toMessages. The block splitting follows sub2api
// backend/internal/pkg/apicompat/chatcompletions_anthropic_bridge.go (8f2cbb5);
// text and image blocks keep their position rather than being lifted across turns.
func messagesRequestToChat(src, dst requestObject, maxOutput int64, b *budget) ([]requestObject, error) {
	if err := messagesSettings(src, dst, maxOutput, b); err != nil {
		return nil, err
	}
	var out []requestObject
	if system := src.take("system"); !isNull(system) {
		messages, err := messagesContent("system", system, b)
		if err != nil {
			return nil, err
		}
		out = append(out, messages...)
	}
	messages, err := arrayWithin(src.take("messages"), b)
	if err != nil {
		return nil, err
	}
	for _, raw := range messages {
		converted, err := messagesTurnToChat(raw, b)
		if err != nil {
			return nil, err
		}
		out = append(out, converted...)
	}
	return out, nil
}

func messagesTurnToChat(raw jsontext.Value, b *budget) ([]requestObject, error) {
	o, err := membersWithin(raw, b)
	if err != nil {
		return nil, err
	}
	role, err := requestString(o.take("role"), b)
	if err != nil {
		return nil, err
	}
	if role != "user" && role != "assistant" && role != "system" {
		return nil, &NotSupported{Reason: "message role has no Chat counterpart"}
	}
	out, err := messagesContent(role, o.take("content"), b)
	if err != nil {
		return nil, err
	}
	return out, o.remaining()
}

func messagesSettings(src, dst requestObject, maxOutput int64, b *budget) error {
	src.move(dst, "max_tokens", "max_tokens")
	src.move(dst, "stop_sequences", "stop")
	if tier := dst["service_tier"]; string(tier) == `"standard_only"` {
		dst["service_tier"] = jsonValueWithin("default", b)
	} else if !isNull(tier) && string(tier) != `"auto"` {
		return &NotSupported{Reason: "service_tier has no Chat counterpart"}
	}
	if v := src.take("metadata"); !isNull(v) {
		meta, err := membersWithin(v, b)
		if err != nil {
			return err
		}
		meta.move(dst, "user_id", "user")
		if err := meta.remaining(); err != nil {
			return err
		}
	}
	if err := messagesThinking(src, dst, maxOutput, b); err != nil {
		return err
	}
	return nil
}

func messagesThinking(src, dst requestObject, maxOutput int64, b *budget) error {
	if v := src.take("output_config"); !isNull(v) {
		o, err := membersWithin(v, b)
		if err != nil {
			return err
		}
		o.move(dst, "effort", "reasoning_effort")
		if err := messagesFormatToChat(o.take("format"), dst, b); err != nil {
			return err
		}
		if err := o.remaining(); err != nil {
			return err
		}
	}
	return thinkingToChat(src.take("thinking"), dst, maxOutput, b)
}

func thinkingToChat(v jsontext.Value, dst requestObject, maxOutput int64, b *budget) error {
	if isNull(v) {
		return nil
	}
	o, err := membersWithin(v, b)
	if err != nil {
		return err
	}
	switch string(o.take("type")) {
	case `"disabled"`:
		dst["reasoning_effort"] = jsonValueWithin("none", b)
	case `"adaptive"`:
		if effortAbsent(dst["reasoning_effort"]) {
			dst["reasoning_effort"] = jsonValueWithin("medium", b)
		}
	case `"enabled"`:
		if err := budgetToChat(o.take("budget_tokens"), dst, maxOutput, b); err != nil {
			return err
		}
	default:
		return &NotSupported{Reason: "thinking mode has no Chat counterpart"}
	}
	if display := o.take("display"); !isNull(display) {
		if !controlIs(display, "omitted") && !controlIs(display, "summarized") {
			return &NotSupported{Reason: "thinking.display has no Chat counterpart"}
		}
	}
	return o.remaining()
}

// effortAbsent reports whether a Chat effort is no setting: absent, null or the empty string. The empty string is
// kept in the body as the client wrote it, but it must not stop a thinking mode from writing the effort it needs.
func effortAbsent(v jsontext.Value) bool { return isNull(v) || string(v) == `""` }

func budgetToChat(v jsontext.Value, dst requestObject, maxOutput int64, b *budget) error {
	var n int64
	if err := unmarshalWithin(v, &n, b); err != nil {
		if errors.Is(err, ErrMemory) {
			return err
		}
		return shapeError("thinking budget must be at least 1024")
	}
	if n < 1024 {
		return shapeError("thinking budget must be at least 1024")
	}
	if !effortAbsent(dst["reasoning_effort"]) {
		return nil
	}
	if limit := dst["max_tokens"]; !isNull(limit) {
		if err := unmarshalWithin(limit, &maxOutput, b); err != nil {
			return err
		}
	}
	c := canon{Reasoning: &canonReasoning{MaxTokens: &n}}
	effort, err := c.targetEffort(maxOutput)
	if err != nil {
		return err
	}
	dst["reasoning_effort"] = jsonValueWithin(effort, b)
	return nil
}

func messagesFormatToChat(v jsontext.Value, dst requestObject, b *budget) error {
	if isNull(v) {
		return nil
	}
	o, err := membersWithin(v, b)
	if err != nil {
		return err
	}
	if string(o.take("type")) != `"json_schema"` {
		return &NotSupported{Reason: "output_config.format has no Chat counterpart"}
	}
	schema := o.take("schema")
	if schema.Kind() != '{' {
		return shapeError("output_config schema must be an object")
	}
	// Messages has no schema name; Chat requires one as an identifier, not a constraint.
	dst["response_format"] = jsonValueWithin(requestObject{
		"type":        jsonValueWithin("json_schema", b),
		"json_schema": jsonValueWithin(requestObject{"name": jsonValueWithin("response", b), "schema": schema, "strict": jsonValueWithin(true, b)}, b),
	}, b)
	return o.remaining()
}

type chatMessageBlocks struct {
	role              string
	out, parts, calls []requestObject
	reasoning         joined
}

func messagesContent(role string, v jsontext.Value, b *budget) ([]requestObject, error) {
	if v.Kind() == '"' {
		return []requestObject{{"role": jsonValueWithin(role, b), "content": v}}, nil
	}
	blocks, err := arrayWithin(v, b)
	if err != nil {
		return nil, err
	}
	state := chatMessageBlocks{role: role}
	for _, raw := range blocks {
		o, err := membersWithin(raw, b)
		if err != nil {
			return nil, err
		}
		if err := state.block(o, b); err != nil {
			return nil, err
		}
		if err := o.remaining(); err != nil {
			return nil, err
		}
	}
	state.flush(b)
	if len(state.out) == 0 {
		state.out = append(state.out, requestObject{"role": jsonValueWithin(role, b), "content": jsonValueWithin("", b)})
	}
	return state.out, nil
}

func (s *chatMessageBlocks) flush(b *budget) {
	if len(s.parts) == 0 && len(s.calls) == 0 && s.reasoning.pieces == 0 {
		return
	}
	msg := requestObject{"role": jsonValueWithin(s.role, b), "content": jsonValueWithin(s.parts, b)}
	if len(s.calls) > 0 {
		msg["tool_calls"] = jsonValueWithin(s.calls, b)
	}
	if s.reasoning.pieces > 0 {
		v, err := s.reasoning.valueWithin(nil, b)
		if err != nil {
			return
		}
		msg["reasoning_content"] = v
	}
	s.out = append(s.out, msg)
	s.parts, s.calls, s.reasoning = nil, nil, joined{}
}

func (s *chatMessageBlocks) block(o requestObject, b *budget) error {
	kind := string(o.take("type"))
	switch {
	case kind == `"tool_result"` && s.role == "user":
		return s.toolResult(o, b)
	case kind == `"tool_use"` && s.role == "assistant":
		return s.toolCall(o, b)
	case kind == `"thinking"` && s.role == "assistant":
		return s.thinking(o, b)
	case kind == `"redacted_thinking"` && s.role == "assistant":
		o.take("data") // follows the reverse converter's non-replayable reasoning drop policy
		return nil
	}
	part, err := nativePart(o, kind, Messages, s.role, b)
	if err != nil {
		return err
	}
	s.parts = append(s.parts, part)
	return nil
}

// toolResult closes the turn so far: a Chat tool turn must follow the assistant turn it answers.
func (s *chatMessageBlocks) toolResult(o requestObject, b *budget) error {
	s.flush(b)
	result, err := messagesToolResult(o, b)
	if err != nil {
		return err
	}
	s.out = append(s.out, result)
	return nil
}

func (s *chatMessageBlocks) toolCall(o requestObject, b *budget) error {
	input := o.take("input")
	if input.Kind() != '{' {
		return shapeError("tool input must be an object")
	}
	call := requestObject{"id": o.take("id"), "type": jsonValueWithin("function", b)}
	call["function"] = jsonValueWithin(requestObject{"name": o.take("name"), "arguments": jsonValueWithin(string(input), b)}, b)
	s.calls = append(s.calls, call)
	return nil
}

// thinking replays the plaintext of an Anthropic thinking block as Chat reasoning; only the provider that signed it can
// verify the signature, and the text alone is what a Chat endpoint reads.
func (s *chatMessageBlocks) thinking(o requestObject, b *budget) error {
	o.take("signature")
	return s.reasoning.addWithin(o.take("thinking"), b)
}

func messagesToolResult(o requestObject, b *budget) (requestObject, error) {
	id, v := o.take("tool_use_id"), o.take("content")
	if isNull(v) || string(v) == "[]" {
		v = jsonValueWithin("", b) // Anthropic permits omitted content; Chat requires text.
	}
	var failed bool
	if v := o.take("is_error"); !isNull(v) && jsonx.Unmarshal(v, &failed) != nil {
		return nil, shapeError("tool result is_error must be boolean")
	}
	content, err := nativeContent(v, Messages, "tool", b)
	if err != nil {
		return nil, err
	}
	if failed {
		// Chat has no failure bit; preserve the tool's error signal in its text.
		if content.Kind() == '"' {
			text, err := requestString(content, b)
			if err != nil {
				return nil, err
			}
			content = jsonValueWithin("Error: "+text, b)
		} else {
			parts, err := arrayWithin(content, b)
			if err != nil {
				return nil, err
			}
			marker := jsonValueWithin(requestObject{"type": jsonValueWithin("text", b), "text": jsonValueWithin("Error: ", b)}, b)
			content = jsonValueWithin(append([]jsontext.Value{marker}, parts...), b)
		}
	}
	return requestObject{"role": jsonValueWithin("tool", b), "tool_call_id": id, "content": content}, nil
}
