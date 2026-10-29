package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // preserve caller-owned payload bytes while upgrading the envelope

	"axiom/internal/platform/jsonx"
)

// legacyRequest keeps the raw declarations alongside the shared call pairing. A full
// canonical re-encoding would also change signatures, schemas and vendor extensions.
type legacyRequest struct {
	canon                *canon
	Tools                []jsontext.Value `json:"tools"`
	Functions            []jsontext.Value `json:"functions"`
	message              int
	toolSeen, choiceSeen bool
}

func rewriteLegacy(body []byte) ([]byte, error) {
	c, err := decodeCanon(body)
	if err != nil {
		return nil, err
	}
	if err := c.checkShape(); err != nil {
		return nil, err
	}
	if err := c.checkCalls(); err != nil {
		return nil, err
	}
	raw := &legacyRequest{canon: c}
	if err := jsonx.Unmarshal(body, raw); err != nil {
		return nil, shapeError("invalid legacy tools")
	}
	p := &rewriter{body: body, out: make([]byte, 0, 4*len(body)+256), legacy: raw}
	p.dec = jsontext.NewDecoder(bytes.NewBuffer(body))
	if err := p.top(legacyRequestRules); err != nil {
		return nil, err
	}
	return p.out, nil
}

var legacyRequestRules = formatRules{
	members: map[string]rule{
		"functions": (*rewriter).drop, "function_call": (*rewriter).drop,
		"tools": (*rewriter).legacyTools, "tool_choice": (*rewriter).legacyChoice,
		"messages": (*rewriter).legacyMessages, "parallel_tool_calls": (*rewriter).legacyParallel,
	},
	add: func(p *rewriter) error {
		if !p.legacy.toolSeen && len(p.legacy.Functions) > 0 {
			p.out = append(p.out, `,"tools":`...)
			p.appendLegacyTools()
		}
		if !p.legacy.choiceSeen && !isNull(p.legacy.canon.ToolChoice) {
			p.out = append(append(p.out, `,"tool_choice":`...), p.legacy.canon.ToolChoice...)
		}
		// Both directions use the same single-call constraint for legacy clients.
		if p.legacy.canon.legacy {
			p.out = append(p.out, `,"parallel_tool_calls":false`...)
		}
		return nil
	},
}

func (p *rewriter) appendLegacyTools() {
	p.out = append(p.out, '[')
	for _, raw := range p.legacy.Tools {
		p.out = append(comma(p.out), raw...)
	}
	for _, raw := range p.legacy.Functions {
		p.out = append(append(append(comma(p.out), `{"type":"function","function":`...), raw...), '}')
	}
	p.out = append(p.out, ']')
}

func (p *rewriter) legacyTools() error {
	p.legacy.toolSeen = true
	if len(p.legacy.Functions) == 0 {
		return p.skip()
	}
	v, err := p.value()
	if err != nil {
		return err
	}
	p.copyTo(p.offset() - len(v))
	p.appendLegacyTools()
	p.at = p.offset()
	return nil
}

func (p *rewriter) legacyChoice() error {
	p.legacy.choiceSeen = true
	v, err := p.value()
	if err != nil {
		return err
	}
	if !isNull(p.legacy.canon.ToolChoice) {
		p.copyTo(p.offset() - len(v))
		p.out = append(p.out, p.legacy.canon.ToolChoice...)
		p.at = p.offset()
	}
	return nil
}

func (p *rewriter) legacyParallel() error {
	if p.legacy.canon.legacy {
		return p.drop()
	}
	return p.skip()
}

func (p *rewriter) legacyMessages() error {
	return p.eachElement(func() error {
		m := &p.legacy.canon.Messages[p.legacy.message]
		p.legacy.message++
		if isNull(m.FunctionCall) && !m.legacyResult {
			return p.skip()
		}
		if err := p.token(); err != nil {
			return err
		}
		if err := p.members(legacyMessageRules, (*rewriter).skip); err != nil {
			return err
		}
		if !isNull(m.FunctionCall) {
			call := m.ToolCalls[0]
			p.out = appendQuoted(append(comma(p.out), `"tool_calls":[{"id":`...), call.ID)
			p.out = append(p.out, `,"type":"function","function":`...)
			p.out = append(append(p.out, m.FunctionCall...), "}]"...)
		} else {
			p.out = appendQuoted(append(comma(p.out), `"role":"tool","tool_call_id":`...), *m.ToolCallID)
		}
		return p.token()
	})
}

var legacyMessageRules = map[string]rule{
	"function_call": (*rewriter).drop,
	"role":          (*rewriter).legacyMessageIdentity,
	"name":          (*rewriter).legacyMessageIdentity,
	"tool_call_id":  (*rewriter).legacyMessageIdentity,
}

// The generated ID replaces the legacy result's role and name; assistant identities stay.
func (p *rewriter) legacyMessageIdentity() error {
	if !isNull(p.legacy.canon.Messages[p.legacy.message-1].FunctionCall) {
		return p.skip()
	}
	return p.drop()
}
