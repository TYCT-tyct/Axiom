package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // stream replies are rewritten in place without decoding payloads
	"fmt"

	"axiom/internal/protocol/internal/sse"
)

// legacyReply rewrites only the Chat envelope. The reusable buffer is charged just
// like the relay's ordinary edit buffer, and is absent for modern clients.
func (r *Relay) legacyReply(data []byte) ([]byte, error) {
	if r.legacy == nil || len(data) == 0 || data[0] == '[' {
		return data, nil
	} // [DONE]
	p := r.legacy
	var err error
	p.out, err = r.grow(p.out[:0], len(data)+3*bytes.Count(data, []byte("tool_calls")))
	if err != nil {
		return nil, err
	}
	p.body, p.at = data, 0
	p.src = *bytes.NewBuffer(data)
	if p.dec == nil {
		p.dec = jsontext.NewDecoder(&p.src)
	} else {
		p.dec.Reset(&p.src)
	}
	if err := p.top(legacyReplyRules); err != nil {
		return nil, fmt.Errorf("%w: legacy reply: %w", ErrUpstream, err)
	}
	return p.out, nil
}

var legacyReplyRules = formatRules{members: map[string]rule{
	"choices": func(p *rewriter) error {
		return p.eachElement(func() error {
			if err := p.token(); err != nil {
				return err
			}
			if err := p.members(legacyReplyChoiceRules, (*rewriter).skip); err != nil {
				return err
			}
			return p.token()
		})
	},
}}

var legacyReplyChoiceRules = map[string]rule{
	"delta": (*rewriter).legacyReplyDelta,
	"finish_reason": func(p *rewriter) error {
		v, err := p.raw()
		if err != nil || v == nil {
			return err
		}
		name, err := p.unquote(v)
		if err == nil && string(name) == "tool_calls" {
			p.replace(len(v), `"function_call"`)
		}
		return err
	},
}

func (p *rewriter) legacyReplyDelta() error {
	d := p.legacyDelta
	d.name, d.arguments = d.name[:0], d.arguments[:0]
	if ok, err := p.open('{', "an object"); !ok {
		return err
	}
	if err := p.members(legacyReplyDeltaRules, (*rewriter).skip); err != nil {
		return err
	}
	if len(d.name) > 0 || len(d.arguments) > 0 {
		if p.out[len(p.out)-1] != '{' {
			p.out = append(p.out, ',')
		}
		p.out = append(p.out, `"function_call":{`...)
		if len(d.name) > 0 {
			p.out = append(append(p.out, `"name":`...), d.name...)
		}
		if len(d.arguments) > 0 {
			p.out = append(append(comma(p.out), `"arguments":`...), d.arguments...)
		}
		p.out = append(p.out, '}')
	}
	return p.token()
}

var legacyReplyDeltaRules = map[string]rule{
	"tool_calls":    (*rewriter).legacyReplyCalls,
	"function_call": (*rewriter).drop,
}

func (p *rewriter) legacyReplyCalls() error {
	err := p.eachElement(func() error { return p.eachMember(p.legacyReplyMember) })
	p.at = p.offset()
	return err
}

func (p *rewriter) legacyReplyMember() error {
	switch string(p.name) {
	case "index":
		n, err := p.count()
		if err != nil {
			return err
		}
		if n != 0 {
			return shapeError("parallel calls in a legacy reply")
		}
		return nil
	case "type":
		kind, err := p.str()
		if err != nil {
			return err
		}
		if len(kind) > 0 && string(kind) != "function" {
			return shapeError("a non-function call in a legacy reply")
		}
		return nil
	case "function":
		return p.eachMember(p.legacyReplyPart)
	default:
		return p.skip()
	}
}

func (p *rewriter) legacyReplyPart() error {
	var dst *[]byte
	switch string(p.name) {
	case "name":
		dst = &p.legacyDelta.name
	case "arguments":
		dst = &p.legacyDelta.arguments
	default:
		return p.skip()
	}
	v, err := p.raw()
	if err != nil || v == nil {
		return err
	}
	b, err := sse.Grow(*dst, len(v), p.charge)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		*dst = append(b, v...)
	} else {
		*dst = append(b[:len(b)-1], v[1:]...)
	}
	return nil
}

type legacyReplyDelta struct{ name, arguments []byte }
