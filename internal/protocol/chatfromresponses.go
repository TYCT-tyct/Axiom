package protocol

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"hash"

	"axiom/internal/protocol/internal/sse"
)

// responsesToChat converts a Responses stream for a Chat client (README.md「跨格式的流」, OpenAI《Responses
// streaming events》, read 2026-10-14). A message item's text and refusal become content and refusal, a function
// call a tool call numbered from 0 whose id is the call_id, a reasoning item's summaries and text reasoning with
// its reasoning details, and its encrypted content one more detail with the item's ID. Responses gives each part
// whole again when it is done, and some upstreams give it only then: each snapshot is compared with what the deltas
// said, by length and SHA-256 of the decoded text, so the client gets every part once (README.md「跨格式的流」).
type responsesToChat struct {
	converted
	raw   *responsesStream // the reader of the upstream's events, which converted holds as inner
	ev    responsesEvent
	items map[int64]*responsesItem // by output_index
	parts map[partKey]*snapshot
	// fresh is the content the event gave the client: every piece counts when it is given, whether as a delta or in a
	// snapshot, so the meter covers what the client has read before it is written (docs/domains/billing.md).
	fresh int64
}

// responsesEvent is what an event says, its strings as written in its data.
type responsesEvent struct {
	index, content, summary                int64
	item                                   itemKind
	id, callID, name, arguments, encrypted []byte
	delta, text, refusal, partText         []byte
	partKind                               partKind
	parts                                  []donePart // of the item, as output_item.done gives it
	next                                   *int64     // the index the next part of the array being read takes
	contents, summaries                    int64
	incomplete                             string
}

type itemKind uint8

const (
	otherItem itemKind = iota
	messageItem
	functionItem
	reasoningItemKind
)

var itemKinds = map[string]itemKind{"message": messageItem, "function_call": functionItem, "reasoning": reasoningItemKind}

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

// donePart is one part of an item given whole: its index in the item's content or summary, and its text.
type donePart struct {
	sub  int64
	kind partKind
	text []byte
}

// responsesItem is an output item: its kind, the Chat index of its tool call, the ID its reasoning details carry,
// the bytes of its content counted so far, and whether it is done.
type responsesItem struct {
	kind    itemKind
	tool    int64
	id      []byte
	counted int64
	done    bool
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

// itemSize and partSize are what an entry of items and of parts is charged: a map slot with its key, the struct and,
// for a part, the SHA-256 state, about 110 bytes, rounded up with the map's spare slots.
const (
	itemSize = 96
	partSize = 320
)

const openAIFormat = `,"format":"openai-responses-v1"`

func newResponsesToChat(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	inner := &responsesStream{seq: -1}
	inner.init(u, o, nil, charge)
	inner.converting = true
	s := &responsesToChat{raw: inner, items: map[int64]*responsesItem{}, parts: map[partKey]*snapshot{}}
	s.init(inner, o, charge)
	return s
}

var (
	responsesChatFields = map[string]func(*responsesToChat) error{
		"output_index":  func(s *responsesToChat) error { return s.count(&s.ev.index) },
		"content_index": func(s *responsesToChat) error { return s.count(&s.ev.content) },
		"summary_index": func(s *responsesToChat) error { return s.count(&s.ev.summary) },
		"delta":         func(s *responsesToChat) error { return s.read(&s.ev.delta) },
		"text":          func(s *responsesToChat) error { return s.read(&s.ev.text) },
		"refusal":       func(s *responsesToChat) error { return s.read(&s.ev.refusal) },
		"arguments":     func(s *responsesToChat) error { return s.read(&s.ev.arguments) },
		"item":          func(s *responsesToChat) error { return walk(&s.walk, s, responsesChatItem) },
		"part":          func(s *responsesToChat) error { return walk(&s.walk, s, responsesChatPart) },
		"response":      func(s *responsesToChat) error { return walk(&s.walk, s, responsesChatResponse) },
	}
	responsesChatItem = map[string]func(*responsesToChat) error{
		"type": func(s *responsesToChat) error {
			v, err := s.walk.str()
			s.ev.item = itemKinds[string(v)]
			return err
		},
		"id":                func(s *responsesToChat) error { return s.read(&s.ev.id) },
		"call_id":           func(s *responsesToChat) error { return s.read(&s.ev.callID) },
		"name":              func(s *responsesToChat) error { return s.read(&s.ev.name) },
		"arguments":         func(s *responsesToChat) error { return s.read(&s.ev.arguments) },
		"encrypted_content": func(s *responsesToChat) error { return s.read(&s.ev.encrypted) },
		"content": func(s *responsesToChat) error {
			s.ev.next = &s.ev.contents
			return s.walk.eachElement(s.itemPart)
		},
		"summary": func(s *responsesToChat) error {
			s.ev.next = &s.ev.summaries
			return s.walk.eachElement(s.itemPart)
		},
	}
	responsesChatPart = map[string]func(*responsesToChat) error{
		"type": func(s *responsesToChat) error {
			v, err := s.walk.str()
			s.ev.partKind = partKinds[string(v)]
			return err
		},
		"text":    func(s *responsesToChat) error { return s.read(&s.ev.partText) },
		"refusal": func(s *responsesToChat) error { return s.read(&s.ev.partText) },
	}
	responsesChatResponse = map[string]func(*responsesToChat) error{
		"incomplete_details": func(s *responsesToChat) error { return walk(&s.walk, s, responsesChatIncomplete) },
	}
	responsesChatIncomplete = map[string]func(*responsesToChat) error{
		"reason": func(s *responsesToChat) error {
			v, err := s.walk.str()
			s.ev.incomplete = string(v)
			return err
		},
	}
)

func (s *responsesToChat) read(dst *[]byte) error {
	v, err := s.text()
	*dst = v
	return err
}

func (s *responsesToChat) count(dst *int64) error {
	n, err := s.walk.count()
	*dst = n
	return err
}

// itemPart reads one part of an item's content or summary into ev.parts, with its index there.
func (s *responsesToChat) itemPart() error {
	s.ev.partKind, s.ev.partText = otherPart, nil
	if err := walk(&s.walk, s, responsesChatPart); err != nil {
		return err
	}
	var err error
	if s.ev.parts, err = sse.Grow(s.ev.parts, 1, s.walk.charge); err != nil {
		return err
	}
	s.ev.parts = append(s.ev.parts, donePart{*s.ev.next, s.ev.partKind, s.ev.partText})
	*s.ev.next++
	return nil
}

func (s *responsesToChat) event(data []byte, m *meter) (event, error) {
	e, err := s.begin(data, m)
	if err != nil {
		return event{}, err
	}
	s.ev, s.fresh = responsesEvent{parts: s.ev.parts[:0]}, 0
	if err := walk(&s.walk, s, responsesChatFields); err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	r, finish, err := s.convert(e.name)
	if err != nil {
		return event{}, fmt.Errorf("%w: %s: %w", ErrUpstream, e.name, err)
	}
	e.content = s.fresh + s.residual(e.name)
	return s.settle(e, r, finish)
}

// residual is the content an item that is done holds beyond the text counted as it was given, such as encrypted
// reasoning: the item's size bounds it from above, and counts only what the pieces did not.
func (s *responsesToChat) residual(name string) int64 {
	if name != "response.output_item.done" {
		return 0
	}
	it := s.items[s.ev.index]
	n := max(0, s.raw.itemBytes-it.counted)
	it.counted += n
	return n
}

// convert turns the event into the delta, or into the role it has.
func (s *responsesToChat) convert(name string) (role, []byte, error) {
	ev := &s.ev
	if p, ok := responsesParts[name]; ok {
		k := partKey{ev.index, p.sub(ev), p.kind}
		if p.kind == otherPart {
			k.kind = ev.partKind
		}
		if p.whole {
			return passive, nil, s.snapshot(k, p.text(ev))
		}
		return passive, nil, s.delta(k, p.text(ev))
	}
	switch name {
	case "response.created":
		s.draft.role, s.started = !s.started, true
	case "response.output_item.added":
		return passive, nil, s.itemAdded()
	case "response.output_item.done":
		return passive, nil, s.itemDone()
	case "response.output_text.annotation.added":
		return 0, nil, fmt.Errorf("an annotation, which Chat has no place for")
	case "response.completed":
		if s.tools > 0 {
			return end, s.hold(`"tool_calls"`), nil
		}
		return end, s.hold(`"stop"`), nil
	case "response.incomplete":
		if reason := responsesIncomplete[ev.incomplete]; reason != "" {
			return end, s.hold(reason), nil
		}
		return 0, nil, fmt.Errorf("incomplete for %q", ev.incomplete)
	}
	return passive, nil, nil
}

// responsesParts are the events that give a piece of a part, or the part whole: its kind, otherPart for the type
// the event's part names; where its index is; and where its text is.
var responsesParts = map[string]struct {
	kind  partKind
	sub   func(*responsesEvent) int64
	text  func(*responsesEvent) []byte
	whole bool
}{
	"response.output_text.delta":             {textPart, contentIndex, deltaText, false},
	"response.refusal.delta":                 {refusalPart, contentIndex, deltaText, false},
	"response.function_call_arguments.delta": {argumentsPart, noIndex, deltaText, false},
	"response.reasoning_summary_text.delta":  {summaryPart, summaryIndex, deltaText, false},
	"response.reasoning_text.delta":          {reasoningTextPart, contentIndex, deltaText, false},
	"response.output_text.done":              {textPart, contentIndex, func(ev *responsesEvent) []byte { return ev.text }, true},
	"response.reasoning_text.done":           {reasoningTextPart, contentIndex, func(ev *responsesEvent) []byte { return ev.text }, true},
	"response.refusal.done":                  {refusalPart, contentIndex, func(ev *responsesEvent) []byte { return ev.refusal }, true},
	"response.function_call_arguments.done":  {argumentsPart, noIndex, func(ev *responsesEvent) []byte { return ev.arguments }, true},
	"response.reasoning_summary_text.done":   {summaryPart, summaryIndex, func(ev *responsesEvent) []byte { return ev.text }, true},
	"response.content_part.done":             {otherPart, contentIndex, partText, true},
	"response.reasoning_summary_part.done":   {summaryPart, summaryIndex, partText, true},
}

func contentIndex(ev *responsesEvent) int64 { return ev.content }
func summaryIndex(ev *responsesEvent) int64 { return ev.summary }
func noIndex(*responsesEvent) int64         { return 0 }
func deltaText(ev *responsesEvent) []byte   { return ev.delta }
func partText(ev *responsesEvent) []byte    { return ev.partText }

// responsesIncomplete are the finish reasons of an incomplete response's reasons; any other is the upstream's
// failure.
var responsesIncomplete = map[string]string{"max_output_tokens": `"length"`, "content_filter": `"content_filter"`}

func (s *responsesToChat) itemAdded() error {
	if s.items[s.ev.index] != nil {
		return fmt.Errorf("item %d added twice", s.ev.index)
	}
	_, err := s.item(true)
	return err
}

func (s *responsesToChat) itemDone() error {
	it, err := s.item(false)
	if err != nil {
		return err
	}
	if it.done {
		return fmt.Errorf("item %d done twice", s.ev.index)
	}
	it.done = true
	for _, p := range s.ev.parts {
		if partOf[p.kind] != it.kind {
			return fmt.Errorf("item %d holds a part of a type it does not take", s.ev.index)
		}
		if err := s.snapshot(partKey{s.ev.index, p.sub, p.kind}, p.text); err != nil {
			return err
		}
	}
	switch {
	case it.kind == functionItem:
		return s.snapshot(partKey{s.ev.index, 0, argumentsPart}, s.ev.arguments)
	case it.kind == reasoningItemKind && s.ev.encrypted != nil:
		s.details++
		return s.addEntry(&s.draft.details, []byte(`{"type":"reasoning.encrypted","data":`), s.ev.encrypted,
			[]byte(`,"id":`), it.id, []byte(openAIFormat), detailIndex(s.details-1), []byte("}"))
	}
	return nil
}

// item is the item at the event's output_index, which it adds when it is new: a function call starts its tool call.
func (s *responsesToChat) item(added bool) (*responsesItem, error) {
	it := s.items[s.ev.index]
	if it != nil {
		if it.kind != s.ev.item {
			return nil, fmt.Errorf("item %d changes its type", s.ev.index)
		}
		return it, nil
	}
	if s.ev.item == otherItem {
		return nil, fmt.Errorf("item %d of a type a converted request does not ask for", s.ev.index)
	}
	if s.ev.item == functionItem && (nonEmpty(s.ev.callID) == nil || nonEmpty(s.ev.name) == nil) {
		return nil, fmt.Errorf("function call %d without a call_id or a name", s.ev.index)
	}
	if err := s.walk.charge(itemSize + len(s.ev.id)); err != nil {
		return nil, err
	}
	it = &responsesItem{kind: s.ev.item, tool: -1, id: bytes.Clone(s.ev.id)}
	s.items[s.ev.index] = it
	if it.kind != functionItem {
		return it, nil
	}
	it.tool = s.tools
	s.tools++
	err := s.addEntry(&s.draft.tools, toolIndex(it.tool), []byte(`,"id":`), s.ev.callID,
		[]byte(`,"type":"function","function":{"name":`), s.ev.name, []byte(`,"arguments":""}}`))
	if err == nil && added {
		err = s.snapshot(partKey{s.ev.index, 0, argumentsPart}, s.ev.arguments)
	}
	return it, err
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

func (s *responsesToChat) dropBuffers() {
	s.converted.dropBuffers()
	s.ev, s.items, s.parts = responsesEvent{}, nil, nil
}

func idOrNull(id []byte) []byte {
	if id == nil {
		return []byte("null")
	}
	return id
}
