package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"strconv"

	"axiom/internal/platform/jsonx"
)

func (s *chatToNative) itemID(i int) string {
	return s.items[i].responseID
}

func (s *chatToNative) responsePart(i int, whole bool) requestObject {
	it := &s.items[i]
	part := requestObject{}
	kind, member := "output_text", "text"
	switch it.kind {
	case "thinking":
		kind = "summary_text"
	case "refusal":
		kind, member = "refusal", "refusal"
	}
	part["type"], part[member] = jsonValue(kind), jsonValue("")
	if whole {
		part[member] = quotedContents(it.text)
	}
	if kind == "output_text" {
		part["annotations"] = jsontext.Value("[]")
	}
	return part
}

func (s *chatToNative) responseItem(i int, whole bool) requestObject {
	it := &s.items[i]
	item := requestObject{"id": jsonValue(s.itemID(i))}
	status := "in_progress"
	if whole {
		status = "completed"
	}
	switch it.kind {
	case "tool_use":
		item["type"], item["call_id"], item["name"] = jsonValue("function_call"), jsonValue(it.id), jsonValue(it.name)
		item["arguments"], item["status"] = jsonValue(""), jsonValue(status)
		if whole {
			item["arguments"] = quotedContents(it.text)
		}
		if it.custom {
			delete(item, "arguments")
			item["type"], item["input"] = jsonValue("custom_tool_call"), jsonValue("")
			if whole {
				item["input"] = customInput(it.text)
			}
		}
	case "thinking":
		item["type"], item["summary"] = jsonValue("reasoning"), jsontext.Value("[]")
		if whole {
			item["summary"] = jsonValue([]requestObject{s.responsePart(i, true)})
		}
	default:
		item["type"], item["role"], item["status"], item["content"] = jsonValue("message"), jsonValue("assistant"), jsonValue(status), jsontext.Value("[]")
		if whole {
			item["content"] = jsonValue([]requestObject{s.responsePart(i, true)})
		}
	}
	return item
}

func (s *chatToNative) responsePosition(i int) requestObject {
	return requestObject{"output_index": jsonValue(i), "item_id": jsonValue(s.itemID(i))}
}

func (s *chatToNative) responseDelta(i int) error {
	it := &s.items[i]
	if !it.started {
		if err := s.emit("response.output_item.added", requestObject{"output_index": jsonValue(i), "item": jsonValue(s.responseItem(i, false))}); err != nil {
			return err
		}
		if it.kind != "tool_use" {
			name, index := "response.content_part.added", "content_index"
			if it.kind == "thinking" {
				name, index = "response.reasoning_summary_part.added", "summary_index"
			}
			event := s.responsePosition(i)
			event[index], event["part"] = jsonValue(0), jsonValue(s.responsePart(i, false))
			if err := s.emit(name, event); err != nil {
				return err
			}
		}
		it.started = true
	}
	if it.sent == len(it.text) || it.custom { // a custom call's input is known only once its arguments are whole
		return nil
	}
	name, index := responseDeltaName(it.kind)
	return s.responseIncrement(i, name, index)
}

// customInput is a custom tool call's input, quoted: the input member of the arguments customFunction asked for, or the
// arguments as they came when the model wrote something else, which the client then checks as it would bad input.
func customInput(arguments []byte) jsontext.Value {
	args := quotedContents(arguments)
	var raw string
	var wrapped struct {
		Input *string `json:"input"`
	}
	if jsonx.Unmarshal(args, &raw) == nil && jsonx.Unmarshal([]byte(raw), &wrapped) == nil && wrapped.Input != nil {
		return jsonValue(*wrapped.Input)
	}
	return args
}

// closeCustomItem ends a custom tool call: its whole input as one delta, then the done events.
func (s *chatToNative) closeCustomItem(i int) error {
	input := customInput(s.items[i].text)
	event := s.responsePosition(i)
	event["delta"] = input
	if err := s.emit("response.custom_tool_call_input.delta", event); err != nil {
		return err
	}
	event = s.responsePosition(i)
	event["input"] = input
	if err := s.emit("response.custom_tool_call_input.done", event); err != nil {
		return err
	}
	return s.emit("response.output_item.done", requestObject{"output_index": jsonValue(i), "item": jsonValue(s.responseItem(i, true))})
}

func responseDeltaName(kind string) (name, index string) {
	switch kind {
	case "tool_use":
		return "response.function_call_arguments", ""
	case "thinking":
		return "response.reasoning_summary_text", "summary_index"
	case "refusal":
		return "response.refusal", "content_index"
	default:
		return "response.output_text", "content_index"
	}
}

func (s *chatToNative) closeResponseItem(i int) error {
	if err := s.responseDelta(i); err != nil {
		return err
	}
	it := &s.items[i]
	if it.custom {
		return s.closeCustomItem(i)
	}
	name, index := responseDeltaName(it.kind)
	event := s.responsePosition(i)
	if index != "" {
		event[index] = jsonValue(0)
	}
	member := "text"
	switch it.kind {
	case "tool_use":
		member = "arguments"
		event["name"] = jsonValue(it.name)
	case "refusal":
		member = "refusal"
	}
	event[member] = quotedContents(it.text)
	if err := s.emit(name+".done", event); err != nil {
		return err
	}
	if it.kind != "tool_use" {
		name = "response.content_part.done"
		if it.kind == "thinking" {
			name = "response.reasoning_summary_part.done"
		}
		event = s.responsePosition(i)
		event[index], event["part"] = jsonValue(0), jsonValue(s.responsePart(i, true))
		if err := s.emit(name, event); err != nil {
			return err
		}
	}
	return s.emit("response.output_item.done", requestObject{"output_index": jsonValue(i), "item": jsonValue(s.responseItem(i, true))})
}

func (s *chatToNative) response(status string, usage []byte) jsontext.Value {
	items := make([]requestObject, 0, len(s.items))
	for i := range s.items {
		items = append(items, s.responseItem(i, true))
	}
	response := requestObject{
		"id": jsonValue(s.o.ID), "object": jsonValue("response"), "created_at": jsonValue(s.o.Created),
		"model": jsonValue(s.o.modelName()), "status": jsonValue(status), "output": jsonValue(items),
		"error": jsontext.Value("null"), "incomplete_details": jsontext.Value("null"),
		"usage": jsontext.Value("null"),
	}
	if status == "incomplete" {
		response["incomplete_details"] = jsonValue(requestObject{"reason": jsonValue(inverseName(responsesIncomplete, strconv.Quote(s.reason)))})
	}
	if len(usage) > 0 {
		response["usage"] = usage
	}
	return jsonValue(response)
}
