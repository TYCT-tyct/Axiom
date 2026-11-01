package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // the turns are spliced into the body as written
	"fmt"

	"axiom/internal/platform/jsonx"
)

// serverAssistant builds a calls-only assistant turn for a caller that has no captured turn.
//
//nolint:cyclop,funlen,gocognit // one branch per chat format
func serverAssistant(f Format, calls []ServerToolCall) ([]byte, error) {
	var assistant []byte
	//nolint:exhaustive // only the three chat formats carry tools
	switch f {
	case Chat:
		assistant = append(assistant, `{"role":"assistant","content":null,"tool_calls":[`...)
		for i, call := range calls {
			if i > 0 {
				assistant = append(assistant, ',')
			}
			args, err := jsonx.MarshalCanonical(serverArguments(call.Arguments))
			if err != nil {
				return nil, err
			}
			id, err := jsonx.MarshalCanonical(call.ID)
			if err != nil {
				return nil, err
			}
			assistant = append(assistant, `{"id":`...)
			assistant = append(assistant, id...)
			assistant = append(assistant, `,"type":"function","function":{"name":"`+ServerToolName+`","arguments":`...)
			assistant = append(assistant, args...)
			assistant = append(assistant, `}}`...)
		}
		assistant = append(assistant, `]}`...)
	case Messages:
		assistant = append(assistant, `{"role":"assistant","content":[`...)
		for i, call := range calls {
			if i > 0 {
				assistant = append(assistant, ',')
			}
			id, err := jsonx.MarshalCanonical(toolUseID(call.ID))
			if err != nil {
				return nil, err
			}
			input := jsontext.Value(serverArguments(call.Arguments))
			assistant = append(assistant, `{"type":"tool_use","id":`...)
			assistant = append(assistant, id...)
			assistant = append(assistant, `,"name":"`+ServerToolName+`","input":`...)
			assistant = append(assistant, input...)
			assistant = append(assistant, '}')
		}
		assistant = append(assistant, `]}`...)
	case Responses:
		for i, call := range calls {
			if i > 0 {
				assistant = append(assistant, ',')
			}
			id, err := jsonx.MarshalCanonical(call.ID)
			if err != nil {
				return nil, err
			}
			args, err := jsonx.MarshalCanonical(serverArguments(call.Arguments))
			if err != nil {
				return nil, err
			}
			assistant = append(assistant, `{"type":"function_call","call_id":`...)
			assistant = append(assistant, id...)
			assistant = append(assistant, `,"name":"`+ServerToolName+`","arguments":`...)
			assistant = append(assistant, args...)
			assistant = append(assistant, '}')
		}
	default:
		return nil, &NotSupported{Reason: fmt.Sprintf("format %d has no tools", f)}
	}
	if f == Responses {
		// Responses' conversation is an array of output items; the fallback is the array AppendServerTurn flattens.
		assistant = append(append([]byte{'['}, assistant...), ']')
	}
	return assistant, nil
}

// removeTopMember removes a top-level object member and the comma that separated it, keeping the rest of the body as
// written.
//
//nolint:cyclop,gocognit // walking back over a JSON member is branchy by nature
func removeTopMember(body []byte, member string) ([]byte, error) {
	_, start, end, ok, err := topMember(body, member)
	if err != nil || !ok {
		return body, err
	}
	// Walk back over the whitespace around the key (never the value's), so a space before the key or colon cannot
	// hide the separator.
	i := start
	for i > 0 && isSpaceByte(body[i-1]) {
		i--
	}
	if i == 0 || body[i-1] != ':' {
		return nil, fmt.Errorf("%w: %s has no value", ErrInvalid, member)
	}
	i--
	for i > 0 && isSpaceByte(body[i-1]) {
		i--
	}
	if i == 0 || body[i-1] != '"' {
		return nil, fmt.Errorf("%w: %s has no key", ErrInvalid, member)
	}
	i--
	for i > 0 {
		i--
		if body[i] == '"' {
			break
		}
	}
	removeStart, removeEnd := i, end
	before := i
	for before > 0 && isSpaceByte(body[before-1]) {
		before--
	}
	if before == 0 {
		return nil, fmt.Errorf("%w: %s is outside an object", ErrInvalid, member)
	}
	if body[before-1] == ',' {
		// A member after the first: remove the comma before it; the next member's whitespace stays.
		removeStart = before - 1
	} else {
		// The first member: remove the comma after it, which is the one that would otherwise lead the object.
		for removeEnd < len(body) && isSpaceByte(body[removeEnd]) {
			removeEnd++
		}
		if removeEnd < len(body) && body[removeEnd] == ',' {
			removeEnd++
		}
	}
	out := make([]byte, 0, len(body)-(removeEnd-removeStart))
	out = append(out, body[:removeStart]...)
	out = append(out, body[removeEnd:]...)
	return out, nil
}
