package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"fmt"

	"axiom/internal/platform/jsonx"
)

func responsesRequestToChat(src, dst requestObject, w Rewrites, b *budget) ([]requestObject, error) {
	if err := responsesChatDefaults(src, b); err != nil {
		return nil, err
	}
	src.move(dst, "max_output_tokens", "max_completion_tokens")
	for _, name := range []string{"parallel_tool_calls", "user", "safety_identifier", "prompt_cache_key"} {
		src.move(dst, name, name)
	}
	src.take("store") // storage and continuation belong to Axiom, never the Chat endpoint
	previous := src.take("previous_response_id")
	if (!isNull(previous) && string(previous) != `""`) != (len(w.History) > 0) {
		return nil, errHistory
	}
	if err := responsesReasoning(src.take("reasoning"), dst, b); err != nil {
		return nil, err
	}
	if err := responsesTextToChat(src.take("text"), dst, b); err != nil {
		return nil, err
	}
	var out []requestObject
	if instructions := src.take("instructions"); !isNull(instructions) {
		if _, err := requestString(instructions); err != nil {
			return nil, err
		}
		out = append(out, requestObject{"role": jsonValueWithin("system", b), "content": instructions})
	}
	items, err := responsesInput(src.take("input"), w.History, b)
	if err != nil {
		return nil, err
	}
	return responsesItemsToChat(items, out, b)
}

func responsesChatDefaults(src requestObject, b *budget) error {
	for _, field := range [...]struct{ name, value string }{{"truncation", `"disabled"`}, {"background", "false"}} {
		if v := src.take(field.name); !isNull(v) && string(v) != field.value {
			return &NotSupported{Reason: field.name + " has no Chat counterpart"}
		}
	}
	include, err := arrayWithin(src.take("include"), b)
	if err != nil {
		return err
	}
	for _, v := range include {
		if string(v) != `"reasoning.encrypted_content"` {
			return &NotSupported{Reason: "include member has no Chat counterpart"}
		}
	}
	return nil // Chat supplies plaintext reasoning, so an encrypted-content hint adds no output.
}

// responsesInput combines resolved history with the current input before item conversion.
func responsesInput(v jsontext.Value, history [][]byte, b *budget) ([]jsontext.Value, error) {
	items := make([]jsontext.Value, 0, len(history)+1)
	for _, item := range history {
		if err := b.draw(rounded(int64(len(item)))); err != nil {
			return nil, err
		}
		canonical, err := jsonx.MarshalCanonical(jsontext.Value(item))
		if err != nil {
			return nil, fmt.Errorf("protocol: invalid history item: %w", err)
		}
		items = append(items, canonical)
	}
	if v.Kind() == '"' {
		items = append(items, jsonValueWithin(requestObject{"role": jsonValueWithin("user", b), "content": v}, b))
	} else {
		input, err := arrayWithin(v, b)
		if err != nil {
			return nil, err
		}
		items = append(items, input...)
	}
	return items, nil
}

func responsesReasoning(v jsontext.Value, dst requestObject, b *budget) error {
	if isNull(v) {
		return nil
	}
	o, err := membersWithin(v, b)
	if err != nil {
		return err
	}
	o.move(dst, "effort", "reasoning_effort")
	// Chat exposes plaintext reasoning, but cannot choose a Responses summary style.
	if summary := o.take("summary"); !isNull(summary) && string(summary) != `"auto"` {
		return &NotSupported{Reason: "reasoning.summary has no Chat counterpart"}
	}
	return o.remaining()
}

func responsesTextToChat(v jsontext.Value, dst requestObject, b *budget) error {
	if isNull(v) {
		return nil
	}
	o, err := membersWithin(v, b)
	if err != nil {
		return err
	}
	o.move(dst, "verbosity", "verbosity")
	if v := o.take("format"); !isNull(v) {
		format, err := membersWithin(v, b)
		if err != nil {
			return err
		}
		kind := format.take("type")
		switch string(kind) {
		case `"text"`, `"json_object"`:
			dst["response_format"] = jsonValueWithin(requestObject{"type": kind}, b)
		case `"json_schema"`:
			schema := requestObject{}
			for _, name := range []string{"name", "description", "schema", "strict"} {
				format.move(schema, name, name)
			}
			dst["response_format"] = jsonValueWithin(requestObject{"type": kind, "json_schema": jsonValueWithin(schema, b)}, b)
		default:
			return &NotSupported{Reason: "text.format has no Chat counterpart"}
		}
		if err := format.remaining(); err != nil {
			return err
		}
	}
	return o.remaining()
}

// responsesItemsToChat follows new-api relaykit/relayconvert/internal/oai_responses/
// to_oai_chat_req.go (603a5ce): calls attach to an assistant turn, results are tool
// turns, and reasoning precedes the assistant turn it belongs to.
type chatInputItems struct {
	out       []requestObject
	reasoning joined
	calls     map[int][]requestObject
}

func responsesItemsToChat(items []jsontext.Value, out []requestObject, b *budget) ([]requestObject, error) {
	s := chatInputItems{out: out, calls: map[int][]requestObject{}}
	for _, raw := range items {
		o, err := membersWithin(raw, b)
		if err != nil {
			return nil, err
		}
		if err := s.item(o, b); err != nil {
			return nil, err
		}
		if err := o.remaining(); err != nil {
			return nil, err
		}
	}
	s.flushReasoning(b)
	for index, calls := range s.calls {
		s.out[index]["tool_calls"] = jsonValueWithin(calls, b)
	}
	return s.out, nil
}

func (s *chatInputItems) item(o requestObject, b *budget) error {
	kind := o.take("type")
	o.take("id")
	if status := o.take("status"); !isNull(status) && string(status) != `"completed"` {
		return &NotSupported{Reason: "an unfinished input item cannot be replayed"}
	}
	switch string(kind) {
	case "", `"message"`:
		return s.message(o, b)
	case `"function_call"`:
		s.call(o, b)
	case `"function_call_output"`:
		s.reasoning = joined{} // A tool result must immediately follow the assistant call it answers.
		v, err := nativeContent(o.take("output"), Responses, "tool", b)
		if err != nil {
			return err
		}
		s.out = append(s.out, requestObject{"role": jsonValueWithin("tool", b), "tool_call_id": o.take("call_id"), "content": v})
	case `"reasoning"`:
		return reasoningSummary(o, &s.reasoning, b)
	default:
		return &NotSupported{Reason: "input item has no Chat counterpart"}
	}
	return nil
}

func (s *chatInputItems) message(o requestObject, b *budget) error {
	role, err := requestString(o.take("role"))
	if err != nil {
		return err
	}
	if role != "user" && role != "assistant" && !isSystem(role) {
		return &NotSupported{Reason: "input message role has no Chat counterpart"}
	}
	v, err := nativeContent(o.take("content"), Responses, role, b)
	if err != nil {
		return err
	}
	msg := requestObject{"role": jsonValueWithin(role, b), "content": v}
	if role == "assistant" {
		s.attachReasoning(msg, b)
	} else {
		s.flushReasoning(b)
	}
	s.out = append(s.out, msg)
	return nil
}

func (s *chatInputItems) call(o requestObject, b *budget) {
	call := requestObject{
		"id": o.take("call_id"), "type": jsonValueWithin("function", b),
		"function": jsonValueWithin(requestObject{"name": o.take("name"), "arguments": o.take("arguments")}, b),
	}
	if len(s.out) == 0 || string(s.out[len(s.out)-1]["role"]) != `"assistant"` {
		s.out = append(s.out, requestObject{"role": jsonValueWithin("assistant", b), "content": jsonValueWithin("", b)})
	}
	i := len(s.out) - 1
	s.calls[i] = append(s.calls[i], call)
	s.attachReasoning(s.out[i], b)
}

func (s *chatInputItems) attachReasoning(msg requestObject, b *budget) {
	if s.reasoning.pieces > 0 {
		v, err := s.reasoning.valueWithin(nil, b)
		if err != nil {
			return
		}
		msg["reasoning_content"] = v
		s.reasoning = joined{}
	}
}

func (s *chatInputItems) flushReasoning(b *budget) {
	if s.reasoning.pieces > 0 {
		msg := requestObject{"role": jsonValueWithin("assistant", b), "content": jsonValueWithin("", b)}
		s.attachReasoning(msg, b)
		s.out = append(s.out, msg)
	}
}

func reasoningSummary(o requestObject, text *joined, b *budget) error {
	o.take("encrypted_content") // Chat cannot replay another provider's opaque reasoning
	parts, err := arrayWithin(o.take("summary"), b)
	if err != nil {
		return err
	}
	for _, raw := range parts {
		p, err := membersWithin(raw, b)
		if err != nil {
			return err
		}
		if string(p.take("type")) != `"summary_text"` {
			return &NotSupported{Reason: "reasoning summary part has no Chat counterpart"}
		}
		if err := text.addWithin(p.take("text"), b); err != nil {
			return err
		}
		if err := p.remaining(); err != nil {
			return err
		}
	}
	return nil
}
