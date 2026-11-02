package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in canon.go: preserve strings and schemas as written
	"fmt"
	"slices"

	"axiom/internal/platform/jsonx"
)

// requestObject consumes understood members. Top-level defaults follow
// canon.Unknown; unknown nested members are rejected instead of silently lost.
type requestObject map[string]jsontext.Value

// requestMembers reads an object of the canonical request into its members. The values are slices of v, clipped so
// that an append to one never writes into v: a reader that copied them would copy every value once more at each level
// of nesting, as deep as the request goes.
func requestMembers(v []byte) (requestObject, error) {
	dec := newDecoder(v)
	if dec.PeekKind() != '{' {
		return nil, shapeError("expected a request object")
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, shapeError("expected a request object")
	}
	o := requestObject{}
	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return nil, shapeError("expected a request object")
		}
		key := name.String() // before ReadValue, which voids the token
		value, err := dec.ReadValue()
		if err != nil {
			return nil, shapeError("expected a request object")
		}
		o[key] = slices.Clip(value)
	}
	if _, err := dec.ReadToken(); err != nil || dec.PeekKind() != 0 {
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
		return &NotSupported{Reason: notConverted(name)}
	}
	return nil
}

func (o requestObject) remainingDefaults() error {
	for name, v := range o {
		if !isDefault(name, v) {
			return &NotSupported{Reason: notConverted(name)}
		}
	}
	return nil
}

func (o requestObject) move(to requestObject, from, name string) {
	if v := o.take(from); !isNull(v) {
		to[name] = v
	}
}

// requestArray reads an array of the canonical request into its elements, slices of v as requestMembers's values are.
func requestArray(v jsontext.Value) ([]jsontext.Value, error) {
	if isNull(v) {
		return nil, nil
	}
	dec := newDecoder(v)
	if dec.PeekKind() != '[' {
		return nil, shapeError("expected an array")
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, shapeError("expected an array")
	}
	var out []jsontext.Value
	for dec.PeekKind() != ']' {
		value, err := dec.ReadValue()
		if err != nil {
			return nil, shapeError("expected an array")
		}
		out = append(out, slices.Clip(value))
	}
	if _, err := dec.ReadToken(); err != nil || dec.PeekKind() != 0 {
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
func requestToChat(f Format, body []byte, t Target, w Rewrites, b *budget) ([]byte, error) {
	chat, err := nativeChatWithin(f, body, t, w, true, b)
	if err != nil {
		return nil, err
	}
	out, err := rewriteWithin(&Request{Format: Chat}, chat, t, w, b)
	b.give(rounded(int64(cap(chat)))) // the canonical request is rewritten into out and no longer needed
	return out, err
}

// nativeChatWithin is nativeChatRequest, checked by checkNativeChat when check is set, under a reservation of
// nativeRequestCost drawn before it starts. The readers build maps and re-encode them through encoding/json, whose
// allocations cannot be drawn one by one, so the stage reserves the bound TestPrepareAllocatesOnlyWhatItDrew holds it
// to; of the reservation only the canonical request it returns stays drawn.
func nativeChatWithin(f Format, body []byte, t Target, w Rewrites, check bool, b *budget) ([]byte, error) {
	reserve := nativeRequestCost(body)
	for _, item := range w.History {
		reserve += nativeRequestCost(item)
	}
	if err := b.draw(reserve); err != nil {
		return nil, err
	}
	chat, err := nativeChatRequest(f, body, t, w)
	if err == nil && check {
		err = checkNativeChat(chat)
	}
	if err != nil {
		b.give(reserve)
		return nil, err
	}
	b.give(reserve - min(reserve, rounded(int64(cap(chat)))))
	return chat, nil
}

// nativeChatRequest writes a Messages or Responses request as the canonical Chat-shaped request, before anything the
// target adds: the model's name, the identifiers' derived values and the endpoint's settings. A conversion to the other
// of the two formats reads it (convertNative) instead of the endpoint's rewrite.
func nativeChatRequest(f Format, body []byte, t Target, w Rewrites) ([]byte, error) {
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
	return jsonValue(dst), nil
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
	return c.checkCalls(nil) // under nativeChatWithin's reservation
}

// nativeRequestCost is what a request converted to Chat allocates, garbage included: the consumed input maps, the
// canonical Chat encoding they are written into, and the structures validating it; history sums one per item
// (nativeChatWithin). A member of an object the readers do not know costs about 610 bytes beside its bytes, measured
// 2026-11-01 for 20,000 of them; TestPrepareAllocatesOnlyWhatItDrew holds every constant to the worst shapes.
func nativeRequestCost(body []byte) int64 {
	return 12*int64(len(body)) + 3072*int64(bytes.Count(body, []byte("{"))+bytes.Count(body, []byte("null"))) +
		768*int64(bytes.Count(body, []byte(":")))
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
