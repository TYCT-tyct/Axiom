package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go

	"axiom/internal/platform/jsonx"
)

// messagesRequestToChat inverts toMessages. The block splitting follows sub2api
// backend/internal/pkg/apicompat/chatcompletions_anthropic_bridge.go (8f2cbb5);
// text and image blocks keep their position rather than being lifted across turns.
func messagesRequestToChat(src, dst requestObject) ([]requestObject, error) {
	if err := messagesSettings(src, dst); err != nil {
		return nil, err
	}
	var out []requestObject
	if system := src.take("system"); !isNull(system) {
		messages, err := messagesContent("system", system)
		if err != nil {
			return nil, err
		}
		out = append(out, messages...)
	}
	messages, err := requestArray(src.take("messages"))
	if err != nil {
		return nil, err
	}
	for _, raw := range messages {
		converted, err := messagesTurnToChat(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, converted...)
	}
	return out, nil
}

func messagesTurnToChat(raw jsontext.Value) ([]requestObject, error) {
	o, err := requestMembers(raw)
	if err != nil {
		return nil, err
	}
	role, err := requestString(o.take("role"))
	if err != nil {
		return nil, err
	}
	if role != "user" && role != "assistant" && role != "system" {
		return nil, &NotSupported{Reason: "message role has no Chat counterpart"}
	}
	out, err := messagesContent(role, o.take("content"))
	if err != nil {
		return nil, err
	}
	return out, o.remaining()
}

func messagesSettings(src, dst requestObject) error {
	src.move(dst, "max_tokens", "max_tokens")
	src.move(dst, "stop_sequences", "stop")
	if tier := dst["service_tier"]; string(tier) == `"standard_only"` {
		dst["service_tier"] = jsonValue("default")
	} else if !isNull(tier) && string(tier) != `"auto"` {
		return &NotSupported{Reason: "service_tier has no Chat counterpart"}
	}
	if v := src.take("metadata"); !isNull(v) {
		meta, err := requestMembers(v)
		if err != nil {
			return err
		}
		meta.move(dst, "user_id", "user")
		if err := meta.remaining(); err != nil {
			return err
		}
	}
	if err := messagesThinking(src, dst); err != nil {
		return err
	}
	return nil
}

func messagesThinking(src, dst requestObject) error {
	if err := thinkingToChat(src.take("thinking"), dst); err != nil {
		return err
	}
	if v := src.take("output_config"); !isNull(v) {
		o, err := requestMembers(v)
		if err != nil {
			return err
		}
		o.move(dst, "effort", "reasoning_effort")
		if err := messagesFormatToChat(o.take("format"), dst); err != nil {
			return err
		}
		if err := o.remaining(); err != nil {
			return err
		}
	}
	return nil
}

func thinkingToChat(v jsontext.Value, dst requestObject) error {
	if !isNull(v) {
		o, err := requestMembers(v)
		if err != nil {
			return err
		}
		switch string(o.take("type")) {
		case `"disabled"`:
			dst["reasoning_effort"] = jsonValue("none")
		case `"adaptive"`:
			dst["reasoning_effort"] = jsonValue("high")
		case `"enabled"`:
			budget := o.take("budget_tokens")
			var n int64
			if err := jsonx.Unmarshal(budget, &n); err != nil || n < 1024 {
				return shapeError("thinking budget must be at least 1024")
			}
			dst["reasoning"] = jsonValue(requestObject{"max_tokens": budget, "enabled": jsonValue(true)})
		default:
			return &NotSupported{Reason: "thinking mode has no Chat counterpart"}
		}
		if err := o.remaining(); err != nil {
			return err
		}
	}
	return nil
}

func messagesFormatToChat(v jsontext.Value, dst requestObject) error {
	if isNull(v) {
		return nil
	}
	o, err := requestMembers(v)
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
	dst["response_format"] = jsonValue(requestObject{"type": jsonValue("json_schema"),
		"json_schema": jsonValue(requestObject{"name": jsonValue("response"), "schema": schema, "strict": jsonValue(true)})})
	return o.remaining()
}

type chatMessageBlocks struct {
	role              string
	out, parts, calls []requestObject
	reasoning         joined
}

func messagesContent(role string, v jsontext.Value) ([]requestObject, error) {
	if v.Kind() == '"' {
		return []requestObject{{"role": jsonValue(role), "content": v}}, nil
	}
	blocks, err := requestArray(v)
	if err != nil {
		return nil, err
	}
	state := chatMessageBlocks{role: role}
	for _, raw := range blocks {
		o, err := requestMembers(raw)
		if err != nil {
			return nil, err
		}
		if err := state.block(o); err != nil {
			return nil, err
		}
		if err := o.remaining(); err != nil {
			return nil, err
		}
	}
	state.flush()
	if len(state.out) == 0 {
		state.out = append(state.out, requestObject{"role": jsonValue(role), "content": jsonValue("")})
	}
	return state.out, nil
}

func (s *chatMessageBlocks) flush() {
	if len(s.parts) == 0 && len(s.calls) == 0 && s.reasoning.pieces == 0 {
		return
	}
	msg := requestObject{"role": jsonValue(s.role), "content": jsonValue(s.parts)}
	if len(s.calls) > 0 {
		msg["tool_calls"] = jsonValue(s.calls)
	}
	if s.reasoning.pieces > 0 {
		msg["reasoning_content"] = s.reasoning.value(nil)
	}
	s.out = append(s.out, msg)
	s.parts, s.calls, s.reasoning = nil, nil, joined{}
}

func (s *chatMessageBlocks) block(o requestObject) error {
	kind := string(o.take("type"))
	switch {
	case kind == `"tool_result"` && s.role == "user":
		s.flush()
		result, err := messagesToolResult(o)
		if err != nil {
			return err
		}
		s.out = append(s.out, result)
	case kind == `"tool_use"` && s.role == "assistant":
		input := o.take("input")
		if input.Kind() != '{' {
			return shapeError("tool input must be an object")
		}
		s.calls = append(s.calls, requestObject{"id": o.take("id"), "type": jsonValue("function"),
			"function": jsonValue(requestObject{"name": o.take("name"), "arguments": jsonValue(string(input))})})
	case kind == `"thinking"` && s.role == "assistant":
		o.take("signature") // only the originating provider can verify it; plaintext remains replayable
		return s.reasoning.add(o.take("thinking"))
	case kind == `"redacted_thinking"` && s.role == "assistant":
		o.take("data") // follows the reverse converter's non-replayable reasoning drop policy
	default:
		part, err := nativePart(o, kind, Messages, s.role)
		if err != nil {
			return err
		}
		s.parts = append(s.parts, part)
	}
	return nil
}

func messagesToolResult(o requestObject) (requestObject, error) {
	id, v := o.take("tool_use_id"), o.take("content")
	if failed := o.take("is_error"); !isNull(failed) && string(failed) != "false" {
		return nil, &NotSupported{Reason: "a tool result's is_error has no Chat counterpart"}
	}
	content, err := nativeContent(v, Messages, "tool")
	if err != nil {
		return nil, err
	}
	return requestObject{"role": jsonValue("tool"), "tool_call_id": id, "content": content}, nil
}
