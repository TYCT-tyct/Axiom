package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in compact.go: the members are copied as written, in place
	"fmt"
	"slices"
)

// countMembers are the members besides model that Anthropic's count_tokens takes (Anthropic《Count tokens in a
// message》, read 2026-11-26). Everything else of a Messages request — its output limit, stream, metadata, the routing
// parameters and the axiom object — stays here: the endpoint refuses members it does not take.
var countMembers = []string{"messages", "system", "tools", "tool_choice", "thinking"}

// CountBody is the body of Anthropic's count_tokens for a Messages request: the endpoint's name for the model, then the
// countMembers of body as they were written, in their order. Its buffer is drawn from m first, and held is what to
// give back once the count is sent; a refusal is ErrMemory. body is one Inspect has read.
func CountBody(m *Memory, body []byte, model string) (out []byte, held int64, err error) {
	// The kept members are a part of body; the model is written anew, at worst every byte escaped as \u00XX.
	size := len(body) + 6*len(model) + len(`{"model":""}`)
	if !m.Charge(int64(size)) {
		return nil, 0, ErrMemory
	}
	defer func() {
		if err != nil {
			m.Refund(int64(size))
		}
	}()
	dec := jsontext.NewDecoder(bytes.NewBuffer(body))
	if dec.PeekKind() != '{' {
		return nil, 0, fmt.Errorf("%w: the body is not an object", ErrInvalid)
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, 0, err
	}
	out, err = jsontext.AppendQuote(append(make([]byte, 0, size), `{"model":`...), model)
	if err != nil {
		return nil, 0, err
	}
	var name []byte
	for dec.PeekKind() != '}' {
		raw, err := dec.ReadValue()
		if err != nil {
			return nil, 0, err
		}
		if name, err = jsontext.AppendUnquote(name[:0], raw); err != nil {
			return nil, 0, err
		}
		value, err := dec.ReadValue()
		if err != nil {
			return nil, 0, err
		}
		if slices.Contains(countMembers, string(name)) {
			out = append(append(append(append(out, `,"`...), name...), `":`...), value...)
		}
	}
	return append(out, '}'), int64(size), nil
}
