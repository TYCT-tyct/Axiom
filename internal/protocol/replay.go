package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // native replay preserves every member except converted artifacts
	"slices"
	"strconv"
	"uuid"
)

// replayBody removes artifacts a Chat conversion cannot make valid for a native provider.
// The ordinary native path keeps its bytes and allocations; only candidate bodies need a filtering pass.
func replayBody(f Format, body []byte, item bool) ([]byte, error) {
	if !replayCandidate(f, body) {
		return body, nil
	}
	p := &rewriter{body: body, out: make([]byte, 0, len(body))}
	p.dec = jsontext.NewDecoder(bytes.NewReader(body))
	rules := replayRules[f]
	if item {
		rules = replayItemRules
	}
	if err := p.top(formatRules{members: rules}); err != nil {
		return nil, shapeError("invalid native replay: " + err.Error())
	}
	return p.out, nil
}

func replayHistory(history [][]byte) ([][]byte, error) {
	out, copied := history, false
	for i, item := range history {
		if !replayCandidate(Responses, item) {
			continue
		}
		if !copied {
			out, copied = slices.Clone(history), true
		}
		var err error
		out[i], err = replayBody(Responses, item, true)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func replayCost(f Format, body []byte) int64 {
	if replayCandidate(f, body) {
		return 4*int64(len(body)) + 4096 // filtering output, two reused decoders and unquoting scratch
	}
	return 0
}

func replayHistoryCost(history [][]byte) int64 {
	var cost int64
	for _, item := range history {
		cost += replayCost(Responses, item)
	}
	if cost > 0 {
		cost += 32 * int64(len(history)) // the copied slice of history entries
	}
	return cost
}

var (
	replayRules = map[Format]map[string]rule{
		Messages:  {"messages": (*rewriter).replayMessages},
		Responses: {"input": (*rewriter).replayInput},
	}
	replayItemRules    = map[string]rule{"id": (*rewriter).replayID}
	replayMessageRules = map[string]rule{
		"role": (*rewriter).replayRole, "content": (*rewriter).replayThinking,
	}
)

func replayCandidate(f Format, body []byte) bool {
	if f != Messages && f != Responses {
		return false
	}
	if bytes.Contains(body, []byte(`\u`)) {
		return true
	}
	if f == Messages {
		return bytes.Contains(body, []byte("thinking"))
	}
	return containsGeneratedID(body)
}

func containsGeneratedID(body []byte) bool {
	for from := 0; from < len(body); {
		next := bytes.IndexByte(body[from:], '_')
		if next < 0 {
			return false
		}
		at := from + next
		if at >= 36 {
			end := at + 1
			for end < len(body) && body[end] >= '0' && body[end] <= '9' {
				end++
			}
			if end < len(body) && body[end] == '"' && generatedOutputID(body[at-36:end]) {
				return true
			}
		}
		from = at + 1
	}
	return false
}

func generatedOutputID(id []byte) bool {
	if len(id) < 38 || id[36] != '_' || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	if _, err := uuid.Parse(string(id[:36])); err != nil {
		return false
	}
	_, err := strconv.ParseUint(string(id[37:]), 10, 64)
	return err == nil
}

func (p *rewriter) replayID() error {
	v, err := p.raw()
	if err != nil || v == nil {
		return err
	}
	id, err := p.unquote(v)
	if err == nil && generatedOutputID(id) {
		p.at = p.offset()
	}
	return err
}

func (p *rewriter) replayInput() error {
	if p.dec.PeekKind() != '[' {
		return p.skip()
	}
	return p.replayArray(func(p *rewriter) (bool, error) { return true, object(replayItemRules)(p) })
}

func (p *rewriter) replayMessages() error {
	return p.replayArray(func(p *rewriter) (bool, error) {
		p.replayAssistant, p.replayEmpty = false, false
		err := object(replayMessageRules)(p)
		return !p.replayAssistant || !p.replayEmpty, err
	})
}

func (p *rewriter) replayRole() error {
	role, err := p.str()
	p.replayAssistant = string(role) == "assistant"
	return err
}

func (p *rewriter) replayThinking() error {
	if p.dec.PeekKind() != '[' {
		return p.skip()
	}
	kept, removed := 0, false
	err := p.replayArray(func(p *rewriter) (bool, error) {
		v, err := p.dec.ReadValue()
		if err != nil {
			return false, err
		}
		unsigned, err := p.unsignedThinking(v)
		if err != nil {
			return false, err
		}
		if unsigned {
			removed = true
			return false, nil
		}
		kept++
		return true, nil
	})
	p.replayEmpty = removed && kept == 0
	return err
}

func (p *rewriter) unsignedThinking(v []byte) (bool, error) {
	if p.replayMeta == nil {
		p.replayMeta = &aggBase{}
	}
	m := p.replayMeta
	m.begin(v)
	thinking, signed := false, false
	err := m.eachMember(func() (err error) {
		switch string(m.name) {
		case "type":
			thinking, err = m.is("thinking")
		case "signature":
			var signature []byte
			signature, err = m.str()
			signed = len(signature) > 0
		default:
			err = m.skip()
		}
		return err
	})
	return thinking && !signed, err
}

func (p *rewriter) replayArray(each func(*rewriter) (bool, error)) error {
	if ok, err := p.open('[', "an array"); !ok {
		return err
	}
	first, kept := p.offset(), false
	p.copyTo(first)
	for p.dec.PeekKind() != ']' {
		if from := p.offset(); !kept && from != first {
			p.at = from + bytes.IndexByte(p.body[from:], ',') + 1
		}
		n := len(p.out)
		keep, err := each(p)
		if err != nil {
			return err
		}
		if keep {
			p.copyTo(p.offset())
			kept = true
		} else {
			p.out, p.at = p.out[:n], p.offset()
		}
	}
	return p.token()
}
