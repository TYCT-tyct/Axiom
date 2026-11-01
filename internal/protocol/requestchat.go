package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in canon.go: preserve strings and schemas as written
	"errors"

	"axiom/internal/platform/jsonx"
)

// requestObject consumes understood members. Top-level defaults follow
// canon.Unknown; unknown nested members are rejected instead of silently lost.
type requestObject map[string]jsontext.Value

func requestMembers(v []byte) (requestObject, error) { return membersWithin(v, nil) }

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

func requestArray(v jsontext.Value) ([]jsontext.Value, error) { return arrayWithin(v, nil) }

func requestString(v jsontext.Value, budgets ...*budget) (string, error) {
	if len(budgets) > 0 {
		// AppendUnquote builds a byte buffer and string(s) copies it; charge both before either is made.
		if err := budgets[0].draw(int64(2 * len(v))); err != nil {
			return "", err
		}
	}
	s, err := jsontext.AppendUnquote(nil, v)
	if err != nil {
		return "", shapeError("expected a string")
	}
	return string(s), nil
}

// jsonValue only accepts structures built here from validated JSON values.
func jsonValue(v any) jsontext.Value { return jsonValueWithin(v, nil) }

// requestToChat adapts new-api's claude_messages/to_oai_chat_req.go and
// oai_responses/to_oai_chat_req.go (relaykit/relayconvert/internal, 603a5ce).
// The canonical model and Prepare's Chat rewrite own validation and identity/cap
// policy; unlike the reference, unsupported members are never silently discarded.
func requestToChat(f Format, body []byte, t Target, w Rewrites, b *budget) ([]byte, error) {
	chat, err := nativeChatRequest(f, body, t, w, b)
	if err != nil {
		return nil, err
	}
	if err := checkNativeChat(chat, b); err != nil {
		return nil, err
	}
	return rewriteWithin(&Request{Format: Chat}, chat, t, w, b)
}

// nativeChatRequest writes a Messages or Responses request as the canonical Chat-shaped request, before anything the
// target adds: the model's name, the identifiers' derived values and the endpoint's settings. A conversion to the other
// of the two formats reads it (convertNative) instead of the endpoint's rewrite.
//
//nolint:cyclop,gocognit // one branch per source format and member, in the order the conversion reads them
func nativeChatRequest(f Format, body []byte, t Target, w Rewrites, b *budget) ([]byte, error) {
	// Dispatch on JSON values, independent of escaped enum spelling or whitespace
	// in empty arrays. Same-format rewrites still preserve the original bytes.
	source := len(body)
	if err := b.draw(rounded(int64(source))); err != nil {
		return nil, err
	}
	body, err := jsonx.MarshalCanonical(jsontext.Value(body))
	if err != nil {
		return nil, shapeError("invalid request JSON")
	}
	src, err := membersWithin(body, b)
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
		messages, err = messagesRequestToChat(src, dst, t.MaxOutput, b)
	} else {
		messages, err = responsesRequestToChat(src, dst, w, b)
	}
	if err != nil {
		return nil, err
	}
	if err := nativeTools(src.take("tools"), f, dst, b); err != nil {
		return nil, err
	}
	if err := nativeChoice(src.take("tool_choice"), f, dst, b); err != nil {
		return nil, err
	}
	if err := src.remainingDefaults(); err != nil {
		return nil, err
	}
	encoded := jsonValueWithin(messages, b)
	if b != nil && b.err != nil {
		return nil, b.err
	}
	dst["messages"] = encoded
	chat := jsonValueWithin(dst, b)
	if b != nil && b.err != nil {
		return nil, b.err
	}
	b.give(rounded(int64(source))) // the canonical source is copied into chat and no longer needed
	if !b.fast() {
		b.give(encodedSize(messages) + structureMemory(messages)) // and so is the messages array chat now holds a copy of
	}
	if b != nil && b.err != nil {
		return nil, b.err
	}
	return chat, nil
}

// checkNativeChat applies the shared Chat invariants to a constructed native request.
func checkNativeChat(chat []byte, b *budget) error {
	before := b.held
	c, err := decodeCanonWithin(chat, b)
	if err != nil {
		b.give(b.held - before)
		return err
	}
	if err := c.checkShape(); err != nil {
		b.give(b.held - before)
		return err
	}
	err = c.checkCalls()
	b.give(b.held - before) // the tree is read only to check the request
	return err
}

// nativeRequestCost is what a request converted to Chat may hold at once: the consumed input maps, the canonical Chat
// encoding they are written into, and the structures validating it, all alive together; history sums one per item
// (PrepareCost).
func nativeRequestCost(body []byte) int64 {
	return 12*int64(len(body)) + 3072*int64(bytes.Count(body, []byte("{"))+bytes.Count(body, []byte("null"))) +
		512*int64(bytes.Count(body, []byte(":")))
}

func nativeTools(v jsontext.Value, f Format, dst requestObject, b *budget) error {
	tools, err := arrayWithin(v, b)
	if err != nil || isNull(v) {
		return err
	}
	out := make([]requestObject, 0, len(tools))
	for _, raw := range tools {
		tool, err := nativeTool(raw, f, b)
		if err != nil {
			return err
		}
		out = append(out, tool)
	}
	dst["tools"] = jsonValueWithin(out, b)
	return nil
}

func nativeTool(raw jsontext.Value, f Format, b *budget) (requestObject, error) {
	o, err := membersWithin(raw, b)
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
	tool := requestObject{"type": jsontext.Value(`"function"`), "function": jsonValueWithin(fn, b)}
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

func nativeChoice(v jsontext.Value, f Format, dst requestObject, b *budget) error {
	if isNull(v) {
		return nil
	}
	if f == Responses && v.Kind() == '"' {
		dst["tool_choice"] = v
		return nil
	}
	o, err := membersWithin(v, b)
	if err != nil {
		return err
	}
	kind, err := requestString(o.take("type"), b)
	if err != nil {
		return err
	}
	if f == Messages {
		if err := messagesParallel(o, dst, b); err != nil {
			return err
		}
	}
	choice, err := nativeChoiceOf(o, f, kind, b)
	if err != nil {
		return err
	}
	dst["tool_choice"] = choice
	return o.remaining()
}

// messagesParallel turns Anthropic's disable_parallel_tool_use into Chat's opposite member.
func messagesParallel(o, dst requestObject, b *budget) error {
	parallel := o.take("disable_parallel_tool_use")
	if isNull(parallel) {
		return nil
	}
	var disabled bool
	if err := unmarshalWithin(parallel, &disabled, b); err != nil {
		if errors.Is(err, ErrMemory) {
			return err
		}
		return shapeError("disable_parallel_tool_use must be boolean")
	}
	dst["parallel_tool_calls"] = jsonValueWithin(!disabled, b)
	return nil
}

// nativeChoiceOf is a tool choice in Chat's terms: one of the three scalar choices, or the function it names.
func nativeChoiceOf(o requestObject, f Format, kind string, b *budget) (jsontext.Value, error) {
	if f == Messages && inverseName(choiceNames, kind) != "" {
		return jsonValueWithin(inverseName(choiceNames, kind), b), nil
	}
	if f == Messages && kind == "tool" || f == Responses && kind == "function" {
		name := o.take("name")
		if _, err := requestString(name, b); err != nil {
			return nil, err
		}
		named := requestObject{"name": name}
		return jsonValueWithin(requestObject{"type": jsonValueWithin("function", b), "function": jsonValueWithin(named, b)}, b), nil
	}
	return nil, &NotSupported{Reason: "tool_choice has no Chat counterpart"}
}
