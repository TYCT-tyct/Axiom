package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"fmt"

	"axiom/internal/platform/jsonx"
)

func responsesRequestToChat(src, dst requestObject, w Rewrites) ([]requestObject, error) {
	src.move(dst, "max_output_tokens", "max_completion_tokens")
	for _, name := range []string{"parallel_tool_calls", "user", "safety_identifier", "prompt_cache_key"} {
		src.move(dst, name, name)
	}
	src.take("store") // storage and continuation belong to Axiom, never the Chat endpoint
	previous := src.take("previous_response_id")
	if (!isNull(previous) && string(previous) != `""`) != (len(w.History) > 0) {
		return nil, errHistory
	}
	if err := responsesReasoning(src.take("reasoning"), dst); err != nil {
		return nil, err
	}
	if err := responsesTextToChat(src.take("text"), dst); err != nil {
		return nil, err
	}
	var out []requestObject
	if instructions := src.take("instructions"); !isNull(instructions) {
		if _, err := requestString(instructions); err != nil {
			return nil, err
		}
		out = append(out, requestObject{"role": jsonValue("system"), "content": instructions})
	}
	items := make([]jsontext.Value, 0, len(w.History)+1)
	for _, item := range w.History {
		canonical, err := jsonx.MarshalCanonical(jsontext.Value(item))
		if err != nil {
			return nil, fmt.Errorf("protocol: invalid history item: %w", err)
		}
		items = append(items, canonical)
	}
	v := src.take("input")
	if v.Kind() == '"' {
		items = append(items, jsonValue(requestObject{"role": jsonValue("user"), "content": v}))
	} else {
		input, err := requestArray(v)
		if err != nil {
			return nil, err
		}
		items = append(items, input...)
	}
	return responsesItemsToChat(items, out)
}

func responsesReasoning(v jsontext.Value, dst requestObject) error {
	if isNull(v) {
		return nil
	}
	o, err := requestMembers(v)
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

func responsesTextToChat(v jsontext.Value, dst requestObject) error {
	if isNull(v) {
		return nil
	}
	o, err := requestMembers(v)
	if err != nil {
		return err
	}
	o.move(dst, "verbosity", "verbosity")
	if v := o.take("format"); !isNull(v) {
		format, err := requestMembers(v)
		if err != nil {
			return err
		}
		kind := format.take("type")
		switch string(kind) {
		case `"text"`, `"json_object"`:
			dst["response_format"] = jsonValue(requestObject{"type": kind})
		case `"json_schema"`:
			schema := requestObject{}
			for _, name := range []string{"name", "description", "schema", "strict"} {
				format.move(schema, name, name)
			}
			dst["response_format"] = jsonValue(requestObject{"type": kind, "json_schema": jsonValue(schema)})
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

func responsesItemsToChat(items []jsontext.Value, out []requestObject) ([]requestObject, error) {
	s := chatInputItems{out: out, calls: map[int][]requestObject{}}
	for _, raw := range items {
		o, err := requestMembers(raw)
		if err != nil {
			return nil, err
		}
		if err := s.item(o); err != nil {
			return nil, err
		}
		if err := o.remaining(); err != nil {
			return nil, err
		}
	}
	if s.reasoning.pieces > 0 {
		s.out = append(s.out, requestObject{"role": jsonValue("assistant"), "content": jsonValue(""), "reasoning_content": s.reasoning.value(nil)})
	}
	for index, calls := range s.calls {
		s.out[index]["tool_calls"] = jsonValue(calls)
	}
	return s.out, nil
}

func (s *chatInputItems) item(o requestObject) error {
	kind := o.take("type")
	o.take("id")
	if status := o.take("status"); !isNull(status) && string(status) != `"completed"` {
		return &NotSupported{Reason: "an unfinished input item cannot be replayed"}
	}
	switch string(kind) {
	case "", `"message"`:
		return s.message(o)
	case `"function_call"`:
		s.call(o)
	case `"function_call_output"`:
		v, err := nativeContent(o.take("output"), Responses, "tool")
		if err != nil {
			return err
		}
		s.out = append(s.out, requestObject{"role": jsonValue("tool"), "tool_call_id": o.take("call_id"), "content": v})
	case `"reasoning"`:
		return reasoningSummary(o, &s.reasoning)
	default:
		return &NotSupported{Reason: "input item has no Chat counterpart"}
	}
	return nil
}

func (s *chatInputItems) message(o requestObject) error {
	role, err := requestString(o.take("role"))
	if err != nil {
		return err
	}
	if role != "user" && role != "assistant" && !isSystem(role) {
		return &NotSupported{Reason: "input message role has no Chat counterpart"}
	}
	v, err := nativeContent(o.take("content"), Responses, role)
	if err != nil {
		return err
	}
	msg := requestObject{"role": jsonValue(role), "content": v}
	if role == "assistant" {
		s.attachReasoning(msg)
	}
	s.out = append(s.out, msg)
	return nil
}

func (s *chatInputItems) call(o requestObject) {
	call := requestObject{
		"id": o.take("call_id"), "type": jsonValue("function"),
		"function": jsonValue(requestObject{"name": o.take("name"), "arguments": o.take("arguments")}),
	}
	if len(s.out) == 0 || string(s.out[len(s.out)-1]["role"]) != `"assistant"` {
		s.out = append(s.out, requestObject{"role": jsonValue("assistant"), "content": jsonValue("")})
	}
	i := len(s.out) - 1
	s.calls[i] = append(s.calls[i], call)
	s.attachReasoning(s.out[i])
}

func (s *chatInputItems) attachReasoning(msg requestObject) {
	if s.reasoning.pieces > 0 {
		msg["reasoning_content"], s.reasoning = s.reasoning.value(nil), joined{}
	}
}

func reasoningSummary(o requestObject, text *joined) error {
	o.take("encrypted_content") // Chat cannot replay another provider's opaque reasoning
	parts, err := requestArray(o.take("summary"))
	if err != nil {
		return err
	}
	for _, raw := range parts {
		p, err := requestMembers(raw)
		if err != nil {
			return err
		}
		if string(p.take("type")) != `"summary_text"` {
			return &NotSupported{Reason: "reasoning summary part has no Chat counterpart"}
		}
		if err := text.add(p.take("text")); err != nil {
			return err
		}
		if err := p.remaining(); err != nil {
			return err
		}
	}
	return nil
}
