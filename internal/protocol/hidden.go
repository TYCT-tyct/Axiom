package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // part values remain slices of the validated event
)

// Hidden output is rewritten only after the upstream reader has counted the full
// content and usage. Opaque state stays replayable in every client format.
var hiddenChatChoices = hidden(func(p *rewriter) error {
	return p.replayArray(func(p *rewriter) (bool, error) {
		if ok, err := p.open('{', "an object"); !ok {
			return true, err
		}
		if err := p.members(hiddenChatChoiceRules, (*rewriter).hiddenChatValue); err != nil {
			return true, err
		}
		return true, p.token()
	})
})

var hiddenChatChoiceRules = map[string]rule{
	"index": (*rewriter).skip, "finish_reason": (*rewriter).skip, "delta": (*rewriter).hiddenChatDelta,
}

var hiddenChatDeltaRules = map[string]rule{
	"reasoning": (*rewriter).drop, "reasoning_content": (*rewriter).drop, "reasoning_details": (*rewriter).hiddenChatDetails,
}

var hiddenThinking = hidden(object(map[string]rule{"thinking": (*rewriter).emptyText}))

var hiddenResponseItem = hidden(object(map[string]rule{
	"summary": (*rewriter).emptyArray,
	"content": (*rewriter).hiddenReasoningParts,
}))

func hidden(do rule) rule {
	return func(p *rewriter) error {
		if !p.hidesReasoning {
			return p.skip()
		}
		return do(p)
	}
}

func arrayObjects(rules map[string]rule) rule {
	each := object(rules)
	return func(p *rewriter) error {
		return p.replayArray(func(p *rewriter) (bool, error) { return true, each(p) })
	}
}

func (p *rewriter) emptyText() error {
	v, err := p.dec.ReadValue()
	if err == nil {
		p.replace(len(v), `""`)
	}
	return err
}

func (p *rewriter) emptyArray() error {
	v, err := p.dec.ReadValue()
	if err == nil {
		p.replace(len(v), `[]`)
	}
	return err
}

func (p *rewriter) hiddenReasoningParts() error {
	return p.replayArray(func(p *rewriter) (bool, error) { return true, p.hiddenResponsePart() })
}

func (p *rewriter) hiddenResponsePart() error {
	v, err := p.dec.ReadValue()
	if err != nil {
		return err
	}
	replacement, err := p.hiddenPartValue(v)
	if err == nil && replacement != "" {
		p.replace(len(v), replacement)
	}
	return err
}

func (p *rewriter) hiddenPartValue(v jsontext.Value) (string, error) {
	if p.replayMeta == nil {
		p.replayMeta = &aggBase{}
	}
	m := p.replayMeta
	m.charge = p.charge
	m.begin(v)
	replacement := ""
	err := m.eachMember(func() error {
		if string(m.name) != "type" {
			return m.skip()
		}
		kind, err := m.str()
		switch string(kind) {
		case "reasoning_text":
			replacement = `{"type":"reasoning_text","text":""}`
		case "summary_text":
			replacement = `{"type":"summary_text","text":""}`
		}
		return err
	})
	return replacement, err
}

func (p *rewriter) hiddenChatDelta() error {
	if ok, err := p.open('{', "an object"); !ok {
		return err
	}
	if err := p.members(hiddenChatDeltaRules, (*rewriter).hiddenChatValue); err != nil {
		return err
	}
	return p.token()
}

func (p *rewriter) hiddenChatValue() error {
	v, err := p.dec.ReadValue()
	if err != nil {
		return err
	}
	switch byte(v.Kind()) {
	case 'n':
	case '"':
		p.chatPayload = p.chatPayload || len(v) > 2
	case '[':
		p.chatPayload = p.chatPayload || len(bytes.TrimSpace(v[1:len(v)-1])) > 0
	default:
		p.chatPayload = true
	}
	return nil
}

func (p *rewriter) hiddenChatDetails() error { return p.replayArray((*rewriter).hiddenChatDetail) }

func (p *rewriter) hiddenChatDetail() (bool, error) {
	v, err := p.dec.ReadValue()
	if err != nil {
		return false, err
	}
	detail, err := p.hiddenDetailValue(v)
	if err != nil {
		return false, err
	}
	keep := detail.encrypted || detail.text && detail.signed
	if !keep {
		return false, nil
	}
	p.chatPayload, p.chatOpaque = true, true
	if detail.encrypted {
		return true, nil
	}
	end := p.offset()
	p.copyTo(end - len(v))
	if detail.textTo > 0 {
		p.out = append(append(append(p.out, v[:detail.textFrom]...), `""`...), v[detail.textTo:]...)
	} else {
		p.out = append(append(p.out, v[:len(v)-1]...), `,"text":""}`...)
	}
	p.at = end
	return true, nil
}

type hiddenDetail struct {
	text, encrypted, signed bool
	textFrom, textTo        int
}

func (p *rewriter) hiddenDetailValue(v jsontext.Value) (hiddenDetail, error) {
	if p.replayMeta == nil {
		p.replayMeta = &aggBase{}
	}
	m := p.replayMeta
	m.charge = p.charge
	m.begin(v)
	var detail hiddenDetail
	err := m.eachMember(func() (err error) {
		switch string(m.name) {
		case "type":
			var kind []byte
			kind, err = m.str()
			detail.text, detail.encrypted = string(kind) == "reasoning.text", string(kind) == "reasoning.encrypted"
		case "signature":
			var signature jsontext.Value
			signature, err = m.raw()
			detail.signed = len(signature) > 2
		case "text":
			var text jsontext.Value
			text, err = m.dec.ReadValue()
			detail.textTo = int(m.dec.InputOffset())
			detail.textFrom = detail.textTo - len(text)
		default:
			err = m.skip()
		}
		return err
	})
	return detail, err
}
