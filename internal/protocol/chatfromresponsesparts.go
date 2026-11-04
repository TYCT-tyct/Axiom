package protocol

import (
	"crypto/sha256"
	"fmt"
	"hash"
)

// The parts of a Responses item, text, refusal, arguments and reasoning, reach a Chat client as delta pieces, whether
// the upstream streamed them or gave them whole.

type partKind uint8

const (
	otherPart partKind = iota
	textPart
	refusalPart
	argumentsPart
	summaryPart
	reasoningTextPart
)

var partKinds = map[string]partKind{
	"output_text": textPart, "refusal": refusalPart, "summary_text": summaryPart, "reasoning_text": reasoningTextPart,
}

// partOf are the kinds of part each kind of item holds.
var partOf = map[partKind]itemKind{
	textPart: messageItem, refusalPart: messageItem, argumentsPart: functionItem, summaryPart: reasoningItemKind,
	reasoningTextPart: reasoningItemKind,
}

// partKey names a part: its item's output_index, its index in the item's content or summary, and its kind.
type partKey struct {
	item, sub int64
	kind      partKind
}

// snapshot is what the client was given of a part: the length and SHA-256 of its decoded text, and the index of its
// reasoning detail.
type snapshot struct {
	n      int64
	sum    hash.Hash
	detail int64
}

// part is the snapshot of part k, which it starts when it is new; the part's item must have been added.
func (s *responsesToChat) part(k partKey) (*snapshot, *responsesItem, error) {
	it := s.items[k.item]
	if it == nil || partOf[k.kind] != it.kind {
		return nil, nil, fmt.Errorf("a part of item %d, which is not an item of its kind", k.item)
	}
	if p := s.parts[k]; p != nil {
		return p, it, nil
	}
	if err := s.walk.charge(partSize); err != nil {
		return nil, nil, err
	}
	p := &snapshot{sum: sha256.New(), detail: -1}
	if k.kind == summaryPart || k.kind == reasoningTextPart {
		p.detail = s.details
		s.details++
	}
	s.parts[k] = p
	return p, it, nil
}

// delta gives the client a piece of part k, piece a JSON string.
func (s *responsesToChat) delta(k partKey, piece []byte) error {
	p, it, err := s.part(k)
	if err != nil || nonEmpty(piece) == nil {
		return err
	}
	text, err := s.walk.unquote(piece)
	if err != nil {
		return err
	}
	p.n += int64(len(text))
	p.sum.Write(text)
	return s.emit(k, p, it, piece)
}

// snapshot compares part k given whole, whole a JSON string, with what the client was given of it: the same is
// nothing new; more, whose start is what was given, gives the rest; anything given when nothing was is new; any
// other is the upstream's failure.
func (s *responsesToChat) snapshot(k partKey, whole []byte) error {
	if whole == nil {
		return nil
	}
	p, it, err := s.part(k)
	if err != nil {
		return err
	}
	text, err := s.walk.unquote(whole)
	if err != nil {
		return err
	}
	var given [sha256.Size]byte
	p.sum.Sum(given[:0])
	switch n := p.n; {
	case n == 0 && len(text) > 0: // nothing given yet: the whole as written
		p.n = int64(len(text))
		p.sum.Write(text)
		return s.emit(k, p, it, whole)
	case int64(len(text)) == n && sha256.Sum256(text) == given:
		return nil
	case int64(len(text)) > n && sha256.Sum256(text[:n]) == given:
		rest := appendQuoted(nil, string(text[n:]))
		p.n = int64(len(text))
		p.sum.Write(text[n:])
		return s.emit(k, p, it, rest)
	}
	return fmt.Errorf("part %d of item %d given whole differs from its deltas", k.sub, k.item)
}

// emit adds a piece of part k to the delta, as the Chat member its kind maps to.
func (s *responsesToChat) emit(k partKey, p *snapshot, it *responsesItem, piece []byte) error {
	n := unquotedLen(piece)
	it.counted += n
	s.fresh += n
	switch k.kind {
	case textPart:
		return s.addText(&s.draft.content, piece)
	case refusalPart:
		return s.addText(&s.draft.refusal, piece)
	case argumentsPart:
		return s.addEntry(&s.draft.tools, toolIndex(it.tool), []byte(`,"function":{"arguments":`), piece, []byte("}}"))
	case summaryPart, reasoningTextPart, otherPart: // part never makes an otherPart
	}
	if s.hidesReasoning {
		return nil
	}
	if err := s.addText(&s.draft.reasoning, piece); err != nil {
		return err
	}
	head := `{"type":"reasoning.text","text":`
	if k.kind == summaryPart {
		head = `{"type":"reasoning.summary","summary":`
	}
	return s.addEntry(&s.draft.details, []byte(head), piece, []byte(`,"id":`), idOrNull(it.id), []byte(openAIFormat),
		detailIndex(p.detail), []byte("}"))
}
