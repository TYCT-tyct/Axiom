package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // offsets and raw values share the strict tokenizer used by Inspect
)

// Only the finite set of top-level rewrite members needs offsets, never the messages inside them.
// If new rules outgrow the table, rewriting falls back to the tokenizer until its capacity is updated.
type requestPositions struct {
	members              [18]memberPosition
	n, close, tail, size int
	ready                bool
}

type memberPosition struct {
	from, value, end, ordinal int
	nameFrom, nameEnd         int
	generation, compact       rule
}

func (s *scanner) topMembers(body []byte) error {
	if err := s.token(); err != nil {
		return err
	}
	positions := &s.r.positions
	positions.ready = true
	positions.size = len(body)
	for ordinal := 0; s.dec.PeekKind() != '}'; ordinal++ {
		if err := s.topMember(body, ordinal); err != nil {
			return err
		}
	}
	positions.tail = int(s.dec.InputOffset())
	if err := s.token(); err != nil {
		return err
	}
	positions.close = int(s.dec.InputOffset()) - 1
	return nil
}

func (s *scanner) topMember(body []byte, ordinal int) error {
	positions := &s.r.positions
	from := int(s.dec.InputOffset())
	if err := s.member(); err != nil {
		return err
	}
	generation := rewriteRules[s.r.Format].members[string(s.name)]
	compact := compactRules[s.r.Format].members[string(s.name)]
	nameEnd := int(s.dec.InputOffset())
	nameFrom := from + bytes.IndexByte(body[from:nameEnd], '"')
	value := nameEnd
	read := s.g.top[string(s.name)]
	if read == nil {
		read = (*scanner).skip
	}
	if err := read(s); err != nil {
		return err
	}
	if generation == nil && compact == nil {
		return nil
	}
	if positions.n == len(positions.members) {
		positions.ready = false
		return nil
	}
	value += bytes.IndexByte(body[value:], ':') + 1
	for body[value] == ' ' || body[value] == '\t' || body[value] == '\r' || body[value] == '\n' {
		value++
	}
	positions.members[positions.n] = memberPosition{from, value, int(s.dec.InputOffset()), ordinal, nameFrom, nameEnd, generation, compact}
	positions.n++
	return nil
}

func (p *rewriter) indexedTop(positions *requestPositions, rules formatRules) error {
	if err := p.indexedMembers(positions); err != nil {
		return err
	}
	p.base, p.end, p.consumed = 0, 0, false
	p.copyTo(positions.tail)
	if rules.add != nil {
		if err := rules.add(p); err != nil {
			return err
		}
	}
	p.copyTo(len(p.body))
	return nil
}

func (p *rewriter) indexedMembers(positions *requestPositions) error {
	removed := 0
	for _, member := range positions.members[:positions.n] {
		do := member.generation
		if p.t.Compact {
			do = member.compact
		}
		if do == nil {
			continue
		}
		dropped, err := p.indexedMember(member, do)
		if err != nil {
			return err
		}
		// Dropping the first remaining member also removes the following comma, including
		// when an unrecorded member follows. Every other drop removes its preceding comma.
		if dropped && member.ordinal == removed {
			removed++
			if comma := bytes.IndexByte(p.body[p.at:positions.close], ','); comma >= 0 {
				p.at += comma + 1
			}
		}
		p.copyTo(max(p.at, member.end))
	}
	return nil
}

func (p *rewriter) indexedMember(member memberPosition, do rule) (bool, error) {
	p.copyTo(max(p.at, member.from))
	p.base, p.end, p.consumed = member.value, member.end, false
	p.src = *bytes.NewBuffer(p.body[member.value:member.end])
	p.dec.Reset(&p.src)
	var err error
	p.name, err = p.unquote(p.body[member.nameFrom:member.nameEnd])
	if err != nil {
		return false, err
	}
	n := len(p.out)
	if err := do(p); err != nil {
		return false, err
	}
	return len(p.out) == n && p.at == member.end, nil
}

func (p *rewriter) offset() int {
	if p.consumed {
		return p.end
	}
	return p.base + int(p.dec.InputOffset())
}

func (p *rewriter) value() (jsontext.Value, error) {
	if p.end > 0 && p.dec.InputOffset() == 0 {
		p.consumed = true
		return p.body[p.base:p.end], nil
	}
	return p.dec.ReadValue()
}

func (p *rewriter) skip() error {
	if p.end > 0 && p.dec.InputOffset() == 0 {
		p.consumed = true
		return nil
	}
	return p.reader.skip()
}
