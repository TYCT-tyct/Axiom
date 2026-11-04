package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // the turns are spliced into the body as written
	"fmt"

	"axiom/internal/platform/jsonx"
)

// serverShape is how a chat format writes the gateway's web search calls, when no captured turn holds them, and the
// results that answer them: Chat's tool_calls and tool messages, Messages' tool_use and tool_result blocks, Responses'
// function_call and function_call_output items.
type serverShape struct {
	id                                func(string) string // a call's ID as the format accepts it
	open, close                       string              // around the calls of a calls-only assistant turn
	callHead, callArgs, callTail      string              // around one call's ID and its arguments
	quoteArgs                         bool                // the arguments are a JSON string; Messages' input is the object
	resultHead, resultMid, resultTail string              // around one result's call ID and its text
}

var (
	chatServerShape = serverShape{
		id: sameID, open: `{"role":"assistant","content":null,"tool_calls":[`, close: `]}`,
		callHead: `{"id":`, callArgs: `,"type":"function","function":{"name":"` + ServerToolName + `","arguments":`,
		callTail: `}}`, quoteArgs: true,
		resultHead: `{"role":"tool","tool_call_id":`, resultMid: `,"content":`, resultTail: `}`,
	}
	messagesServerShape = serverShape{
		id: toolUseID, open: `{"role":"assistant","content":[`, close: `]}`,
		callHead: `{"type":"tool_use","id":`, callArgs: `,"name":"` + ServerToolName + `","input":`, callTail: `}`,
		resultHead: `{"role":"user","content":[{"type":"tool_result","tool_use_id":`,
		resultMid:  `,"content":[{"type":"text","text":`, resultTail: `}]}]}`,
	}
	// Responses' conversation is an array of output items; its calls-only turn is the array AppendServerTurn flattens.
	responsesServerShape = serverShape{
		id: sameID, open: `[`, close: `]`,
		callHead: `{"type":"function_call","call_id":`, callArgs: `,"name":"` + ServerToolName + `","arguments":`,
		callTail: `}`, quoteArgs: true,
		resultHead: `{"type":"function_call_output","call_id":`, resultMid: `,"output":`, resultTail: `}`,
	}
)

func sameID(id string) string { return id }

// serverShapeOf is the shape of a format that carries tools.
func serverShapeOf(f Format) (*serverShape, error) {
	switch f { //nolint:exhaustive // only the three chat formats carry tools
	case Chat:
		return &chatServerShape, nil
	case Messages:
		return &messagesServerShape, nil
	case Responses:
		return &responsesServerShape, nil
	default:
		return nil, &NotSupported{Reason: fmt.Sprintf("format %d has no tools", f)}
	}
}

// assistant builds a calls-only assistant turn for a caller that has no captured turn.
func (s *serverShape) assistant(calls []ServerToolCall) ([]byte, error) {
	out := append([]byte(nil), s.open...)
	for i, call := range calls {
		if i > 0 {
			out = append(out, ',')
		}
		id, err := jsonx.MarshalCanonical(s.id(call.ID))
		if err != nil {
			return nil, err
		}
		out = append(out, s.callHead...)
		out = append(out, id...)
		out = append(out, s.callArgs...)
		args, err := s.arguments(call.Arguments)
		if err != nil {
			return nil, err
		}
		out = append(out, args...)
		out = append(out, s.callTail...)
	}
	return append(out, s.close...), nil
}

// arguments is a call's arguments as the format writes them: a JSON string, or Messages' object itself.
func (s *serverShape) arguments(args string) ([]byte, error) {
	args = serverArguments(args)
	if s.quoteArgs {
		return jsonx.MarshalCanonical(args)
	}
	return []byte(args), nil
}

// results writes one result per server call, comma-separated; results has one entry per call.
func (s *serverShape) results(calls []ServerToolCall, results []string) ([]byte, error) {
	var out []byte
	for i, call := range calls {
		id, err := jsonx.MarshalCanonical(s.id(call.ID))
		if err != nil {
			return nil, err
		}
		out = appendResult(out, i, s.resultHead, id, s.resultMid, results[i], s.resultTail)
	}
	return out, nil
}

// appendResult appends one tool result, quoted, with a comma before it when it is not the first.
func appendResult(dst []byte, i int, prefix string, id []byte, middle, text, suffix string) []byte {
	if i > 0 {
		dst = append(dst, ',')
	}
	dst = append(dst, prefix...)
	dst = append(dst, id...)
	dst = append(dst, middle...)
	quoted, err := jsonx.MarshalCanonical(text)
	if err != nil {
		panic(err) // a Go string always encodes
	}
	dst = append(dst, quoted...)
	return append(dst, suffix...)
}

// appendConversation appends the turn's assistant message and the results following it to the body's conversation:
// messages, or Responses' input.
func appendConversation(f Format, body, assistant, following []byte) ([]byte, error) {
	member := "messages"
	if f == Responses {
		member = "input"
	}
	value, start, end, ok, err := topMember(body, member)
	if err != nil {
		return nil, err
	}
	if f == Responses && ok && jsontext.Value(value).Kind() == '"' {
		body, value, end = inputMessage(body, start, end)
	}
	parts, err := serverTurnParts(f, assistant, following)
	if err != nil {
		return nil, err
	}
	if !ok {
		added := append(append([]byte{'['}, parts...), ']')
		return insertMember(body, member, added)
	}
	if jsontext.Value(value).Kind() != '[' {
		return nil, fmt.Errorf("%w: %s is not an array", ErrInvalid, member)
	}
	return appendElements(body, value, end, parts), nil
}

// inputMessage keeps a string Responses input, body[start:end], as the one user message it stands for (OpenAI《Create a
// model response》, input). It returns the body with that message's array in its place, the array, and where it ends.
func inputMessage(body []byte, start, end int) (out, array []byte, arrayEnd int) {
	out = make([]byte, 0, len(body)+32)
	out = append(out, body[:start]...)
	out = append(out, `[{"role":"user","content":`...)
	out = append(out, body[start:end]...)
	out = append(out, `}]`...)
	arrayEnd = len(out)
	out = append(out, body[end:]...)
	return out, out[start:arrayEnd], arrayEnd
}

// appendElements appends elems to the array value, which ends at end in body, after a comma when it holds an element.
func appendElements(body, value []byte, end int, elems []byte) []byte {
	at := end - 1 // the closing ']'
	out := make([]byte, 0, len(body)+len(elems)+1)
	out = append(out, body[:at]...)
	if !arrayEmpty(value) {
		out = append(out, ',')
	}
	out = append(out, elems...)
	return append(out, body[at:]...)
}

// removeTopMember removes a top-level object member and the comma that separated it, keeping the rest of the body as
// written.
func removeTopMember(body []byte, member string) ([]byte, error) {
	_, start, end, ok, err := topMember(body, member)
	if err != nil || !ok {
		return body, err
	}
	key, err := keyStart(body, start, member)
	if err != nil {
		return nil, err
	}
	removeStart, removeEnd, err := withSeparator(body, key, end, member)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(body)-(removeEnd-removeStart))
	out = append(out, body[:removeStart]...)
	return append(out, body[removeEnd:]...), nil
}

// keyStart walks back from a member's value at start to its key's opening quote. It skips the whitespace around the
// key and the colon (never the value's), so a space before the key or colon cannot hide the separator.
func keyStart(body []byte, start int, member string) (int, error) {
	i := spaceBefore(body, start)
	if i == 0 || body[i-1] != ':' {
		return 0, fmt.Errorf("%w: %s has no value", ErrInvalid, member)
	}
	i = spaceBefore(body, i-1)
	if i == 0 || body[i-1] != '"' {
		return 0, fmt.Errorf("%w: %s has no key", ErrInvalid, member)
	}
	i--
	for i > 0 {
		i--
		if body[i] == '"' {
			break
		}
	}
	return i, nil
}

// withSeparator widens a member's span, from its key to its value's end, by the comma that separated it.
func withSeparator(body []byte, key, end int, member string) (start, stop int, err error) {
	before := spaceBefore(body, key)
	if before == 0 {
		return 0, 0, fmt.Errorf("%w: %s is outside an object", ErrInvalid, member)
	}
	if body[before-1] == ',' {
		// A member after the first: remove the comma before it; the next member's whitespace stays.
		return before - 1, end, nil
	}
	// The first member: remove the comma after it, which is the one that would otherwise lead the object.
	for end < len(body) && space(body[end]) {
		end++
	}
	if end < len(body) && body[end] == ',' {
		end++
	}
	return key, end, nil
}

// spaceBefore is i moved back over the whitespace before it.
func spaceBefore(body []byte, i int) int {
	for i > 0 && space(body[i-1]) {
		i--
	}
	return i
}
