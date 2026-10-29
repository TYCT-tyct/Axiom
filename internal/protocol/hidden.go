package protocol

import "encoding/json/jsontext" //nolint:depguard // part values remain slices of the validated event

// Hidden output is rewritten only after the upstream reader has counted the full
// content and usage. Native opaque state stays replayable; Chat exposes no details.
var hiddenChatChoices = hidden(arrayObjects(map[string]rule{
	"delta": object(map[string]rule{"reasoning": (*rewriter).drop, "reasoning_content": (*rewriter).drop, "reasoning_details": (*rewriter).drop}),
}))

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
	return p.replayArray(func(p *rewriter) (bool, error) {
		v, err := p.dec.ReadValue()
		if err != nil {
			return false, err
		}
		replacement, err := p.hiddenPartValue(v)
		return replacement == "", err
	})
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
