package protocol

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"

	"axiom/internal/platform/jsonx"
)

// The body of a Chat completion is written once, at the end, from the pieces chatAggregate gathered.

func (a *chatAggregate) body() ([]byte, error) {
	a.heal()
	slices.SortFunc(a.choices, func(x, y chatChoice) int { return cmp.Compare(x.index, y.index) })
	size := 512 + len(a.id) + len(a.model) + len(a.usage) + len(a.errored) + len(a.axiom)
	for i := range a.choices {
		size += a.choices[i].size()
	}
	out := a.appendHead(make([]byte, 0, size), "chat.completion")
	for i := range a.choices {
		var err error
		if out, err = a.choices[i].render(comma(out), a.hadError, a.legacy); err != nil {
			return nil, err
		}
	}
	out = append(out, ']')
	return a.appendTail(out), nil
}

// size is about what the choice takes in a body, so that the body is allocated once: its pieces and a margin for the
// members around them.
func (c *chatChoice) size() int {
	n := 256 + len(c.details) + len(c.finish) + len(c.annotations)
	for _, s := range c.strings {
		n += len(s)
	}
	for _, t := range c.tools {
		n += 128 + len(t.id) + len(t.name) + len(t.arguments)
	}
	return n
}

func orElse(v []byte, def string) []byte {
	if len(v) == 0 {
		return []byte(def)
	}
	return v
}

// render appends the choice as a completion's choice: the message, with the members a chunk's delta has only when it
// has them. A tool call's message has no content, as OpenAI's has none. A choice that never finished, when the reply
// failed, finished with the error.
func (c *chatChoice) render(out []byte, failed, legacy bool) ([]byte, error) {
	out = append(strconv.AppendInt(append(out, `{"index":`...), c.index, 10), `,"message":{"role":"assistant","content":`...)
	out = appendContents(out, c.strings[pieceContent], len(c.strings[pieceContent]) > 0 || len(c.tools) == 0)
	out = appendContents(append(out, `,"refusal":`...), c.strings[pieceRefusal], len(c.strings[pieceRefusal]) > 0)
	for _, m := range [...]struct {
		name string
		i    int
	}{{"reasoning", pieceReasoning}, {"reasoning_content", pieceReasoningContent}} {
		if len(c.strings[m.i]) > 0 {
			out = appendContents(append(append(append(out, `,"`...), m.name...), `":`...), c.strings[m.i], true)
		}
	}
	var err error
	if len(c.details) > 0 {
		if out, err = appendDetails(append(out, `,"reasoning_details":`...), c.details); err != nil {
			return nil, err
		}
	}
	if len(c.annotations) > 0 {
		out = append(append(append(out, `,"annotations":[`...), c.annotations...), ']')
	}
	out = c.appendCalls(out, legacy)

	out = c.appendLogprobs(append(out, `},"logprobs":`...))
	out = c.appendFinish(out, failed, legacy)
	return append(out, '}'), nil
}

// appendLogprobs appends the choice's logprobs: null when no chunk had any, else the object with the entries of
// content and refusal, each null when no chunk had that array.
func (c *chatChoice) appendLogprobs(out []byte) []byte {
	if !c.logSeen[0] && !c.logSeen[1] {
		return append(out, "null"...)
	}
	out = append(out, '{')
	for i, name := range [...]string{"content", "refusal"} {
		out = append(append(comma(out), '"'), name...)
		out = append(out, `":`...)
		if c.logSeen[i] {
			out = append(append(append(out, '['), c.logs[i]...), ']')
		} else {
			out = append(out, "null"...)
		}
	}
	return append(out, '}')
}

// appendContents appends a string whose contents are given, or null when it is not set.
func appendContents(out, contents []byte, set bool) []byte {
	if !set {
		return append(out, "null"...)
	}
	return append(append(append(out, '"'), contents...), '"')
}

// appendTools appends the calls as the array of a message's tool_calls.
func appendTools(out []byte, tools []chatTool) []byte {
	out = append(out, '[')
	for _, t := range tools {
		out = append(comma(out), `{"id":`...)
		out = append(out, orElse(t.id, `""`)...)
		out = append(append(out, `,"type":`...), orElse(t.kind, `"function"`)...)
		out = appendContents(append(out, `,"function":{"name":`...), t.name, true)
		out = appendContents(append(out, `,"arguments":`...), t.arguments, true)
		out = append(out, "}}"...)
	}
	return append(out, ']')
}

// appendDetails appends the entries merged, as a client hands them back (mergeDetails), as an array.
func appendDetails(out, entries []byte) ([]byte, error) {
	var details []canonReasoningDetail
	if err := jsonx.Unmarshal(slices.Concat([]byte{'['}, entries, []byte{']'}), &details); err != nil {
		return nil, fmt.Errorf("%w: reasoning_details: %w", ErrUpstream, err)
	}
	details, err := mergeDetails(details)
	if err != nil {
		return nil, fmt.Errorf("%w: reasoning_details: %w", ErrUpstream, err)
	}
	out = append(out, '[')
	for _, d := range details {
		out = appendQuoted(append(comma(out), `{"type":`...), d.Type)
		for _, m := range [...]struct {
			name string
			v    []byte
		}{{"text", d.Text}, {"summary", d.Summary}, {"signature", d.Signature}, {"data", d.Data}, {"id", d.ID}} {
			if !isNull(m.v) {
				out = append(append(append(comma(out), '"'), m.name...), `":`...)
				out = append(out, m.v...)
			}
		}
		if d.Format != "" {
			out = appendQuoted(append(comma(out), `"format":`...), d.Format)
		}
		if !isNull(d.Index) {
			out = append(append(comma(out), `"index":`...), d.Index...)
		}
		out = append(out, '}')
	}
	return append(out, ']'), nil
}

func (c *chatChoice) appendCalls(out []byte, legacy bool) []byte {
	if len(c.tools) > 0 {
		if legacy {
			t := c.tools[0]
			out = appendContents(append(out, `,"function_call":{"name":`...), t.name, true)
			out = appendContents(append(out, `,"arguments":`...), t.arguments, true)
			out = append(out, '}')
		} else {
			out = appendTools(append(out, `,"tool_calls":`...), c.tools)
		}
	}
	return out
}

func (c *chatChoice) appendFinish(out []byte, failed, legacy bool) []byte {
	out = append(out, `,"finish_reason":`...)
	switch {
	case len(c.finish) > 0:
		if legacy && controlIs(c.finish, "tool_calls") {
			out = append(out, `"function_call"`...)
		} else {
			out = append(out, c.finish...)
		}
	case failed:
		out = append(out, `"error"`...)
	default:
		out = append(out, "null"...)
	}
	return out
}
