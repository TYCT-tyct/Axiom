package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in canon.go: preserve strings and schemas as written
	"fmt"

	"axiom/internal/platform/jsonx"
)

// requestObject consumes understood members. Top-level defaults follow
// canon.Unknown; unknown nested members are rejected instead of silently lost.
type requestObject map[string]jsontext.Value

func requestMembers(v []byte) (requestObject, error) {
	var o requestObject
	if err := jsonx.Unmarshal(v, &o); err != nil || o == nil {
		return nil, shapeError("expected a request object")
	}
	return o, nil
}

func (o requestObject) take(name string) jsontext.Value {
	v := o[name]
	delete(o, name)
	return v
}

func (o requestObject) remaining() error {
	for name := range o {
		return &NotSupported{Reason: name + " is not converted"}
	}
	return nil
}

func (o requestObject) remainingDefaults() error {
	for name, v := range o {
		if !isDefault(name, v) {
			return &NotSupported{Reason: name + " is not converted"}
		}
	}
	return nil
}

func (o requestObject) move(to requestObject, from, name string) {
	if v := o.take(from); !isNull(v) {
		to[name] = v
	}
}

func requestArray(v jsontext.Value) ([]jsontext.Value, error) {
	if isNull(v) {
		return nil, nil
	}
	var out []jsontext.Value
	if err := jsonx.Unmarshal(v, &out); err != nil {
		return nil, shapeError("expected an array")
	}
	return out, nil
}

func requestString(v jsontext.Value) (string, error) {
	s, err := jsontext.AppendUnquote(nil, v)
	if err != nil {
		return "", shapeError("expected a string")
	}
	return string(s), nil
}

// jsonValue only accepts structures built here from validated JSON values.
func jsonValue(v any) jsontext.Value {
	b, err := jsonx.MarshalCanonical(v)
	if err != nil {
		panic(fmt.Sprintf("protocol: invalid constructed JSON: %v", err))
	}
	return b
}

// requestToChat adapts new-api's claude_messages/to_oai_chat_req.go and
// oai_responses/to_oai_chat_req.go (relaykit/relayconvert/internal, 603a5ce).
// The canonical model and Prepare's Chat rewrite own validation and identity/cap
// policy; unlike the reference, unsupported members are never silently discarded.
func requestToChat(f Format, body []byte, t Target, w Rewrites) ([]byte, error) {
	// Dispatch on JSON values, independent of escaped enum spelling or whitespace
	// in empty arrays. Same-format rewrites still preserve the original bytes.
	body, err := jsonx.MarshalCanonical(jsontext.Value(body))
	if err != nil {
		return nil, shapeError("invalid request JSON")
	}
	src, err := requestMembers(body)
	if err != nil {
		return nil, err
	}
	dst := requestObject{}
	for _, name := range []string{"model", "temperature", "top_p", "stream", "service_tier"} {
		src.move(dst, name, name)
	}
	for _, name := range routingParameters {
		src.take(name)
	}
	var messages []requestObject
	if f == Messages {
		messages, err = messagesRequestToChat(src, dst, t.MaxOutput)
	} else {
		messages, err = responsesRequestToChat(src, dst, w)
	}
	if err != nil {
		return nil, err
	}
	if err := nativeTools(src.take("tools"), f, dst); err != nil {
		return nil, err
	}
	if err := nativeChoice(src.take("tool_choice"), f, dst); err != nil {
		return nil, err
	}
	if err := src.remainingDefaults(); err != nil {
		return nil, err
	}
	dst["messages"] = jsonValue(messages)
	chat := jsonValue(dst)
	if err := checkNativeChat(chat); err != nil {
		return nil, err
	}
	return rewrite(&Request{Format: Chat}, chat, t, w)
}

// checkNativeChat applies the shared Chat invariants to a constructed native request.
func checkNativeChat(chat []byte) error {
	c, err := decodeCanon(chat)
	if err != nil {
		return err
	}
	if err := c.checkShape(); err != nil {
		return err
	}
	return c.checkCalls()
}

// nativeRequestCost is what a request converted to Chat may hold at once: the consumed input maps, the canonical Chat
// encoding they are written into, and the structures validating it, all alive together; history sums one per item
// (PrepareCost).
func nativeRequestCost(body []byte) int64 {
	return 12*int64(len(body)) + 3072*int64(bytes.Count(body, []byte("{"))+bytes.Count(body, []byte("null"))) +
		512*int64(bytes.Count(body, []byte(":")))
}

func nativeTools(v jsontext.Value, f Format, dst requestObject) error {
	tools, err := requestArray(v)
	if err != nil || isNull(v) {
		return err
	}
	out := make([]requestObject, 0, len(tools))
	for _, raw := range tools {
		tool, err := nativeTool(raw, f)
		if err != nil {
			return err
		}
		out = append(out, tool)
	}
	dst["tools"] = jsonValue(out)
	return nil
}

func nativeTool(raw jsontext.Value, f Format) (requestObject, error) {
	o, err := requestMembers(raw)
	if err != nil {
		return nil, err
	}
	kind := o.take("type")
	if !isNull(kind) && string(kind) != `"function"` {
		return nil, &NotSupported{Reason: "only function tools convert to Chat"}
	}
	fn := requestObject{}
	for _, name := range []string{"name", "description", "strict"} {
		o.move(fn, name, name)
	}
	parameters := "parameters"
	if f == Messages {
		parameters = "input_schema"
	}
	o.move(fn, parameters, "parameters")
	if f == Responses && isNull(fn["strict"]) {
		fn["strict"] = jsontext.Value("false") // Chat cannot promise Responses' implicit schema normalization.
	}
	tool := requestObject{"type": jsontext.Value(`"function"`), "function": jsonValue(fn)}
	o.move(tool, "cache_control", "cache_control")
	return tool, o.remaining()
}

// choiceNames is shared by both request directions. Named choices carry their
// name separately, so only the three scalar choices belong in this mapping.
var choiceNames = map[string]string{"auto": "auto", "none": "none", "required": "any"}

func inverseName(names map[string]string, value string) string {
	var found string
	for from, to := range names {
		if to == value && (found == "" || from < found) {
			found = from
		}
	}
	return found
}

func nativeChoice(v jsontext.Value, f Format, dst requestObject) error {
	if isNull(v) {
		return nil
	}
	if f == Responses && v.Kind() == '"' {
		dst["tool_choice"] = v
		return nil
	}
	o, err := requestMembers(v)
	if err != nil {
		return err
	}
	kind, err := requestString(o.take("type"))
	if err != nil {
		return err
	}
	if f == Messages {
		if err := messagesParallel(o, dst); err != nil {
			return err
		}
	}
	choice, err := nativeChoiceOf(o, f, kind)
	if err != nil {
		return err
	}
	dst["tool_choice"] = choice
	return o.remaining()
}

// messagesParallel turns Anthropic's disable_parallel_tool_use into Chat's opposite member.
func messagesParallel(o, dst requestObject) error {
	parallel := o.take("disable_parallel_tool_use")
	if isNull(parallel) {
		return nil
	}
	var disabled bool
	if err := jsonx.Unmarshal(parallel, &disabled); err != nil {
		return shapeError("disable_parallel_tool_use must be boolean")
	}
	dst["parallel_tool_calls"] = jsonValue(!disabled)
	return nil
}

// nativeChoiceOf is a tool choice in Chat's terms: one of the three scalar choices, or the function it names.
func nativeChoiceOf(o requestObject, f Format, kind string) (jsontext.Value, error) {
	if f == Messages && inverseName(choiceNames, kind) != "" {
		return jsonValue(inverseName(choiceNames, kind)), nil
	}
	if f == Messages && kind == "tool" || f == Responses && kind == "function" {
		name := o.take("name")
		if _, err := requestString(name); err != nil {
			return nil, err
		}
		named := requestObject{"name": name}
		return jsonValue(requestObject{"type": jsonValue("function"), "function": jsonValue(named)}), nil
	}
	return nil, &NotSupported{Reason: "tool_choice has no Chat counterpart"}
}
