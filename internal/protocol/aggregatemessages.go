package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: events are read token by token, in place
	"fmt"
	"slices"
	"strings"

	"axiom/internal/platform/jsonx"
)

// messagesAggregate builds a Messages message from the events of a Messages stream (README.md「非流式聚合」,
// Anthropic《Messages API reference》, read 2026-10-15). Blocks come one after another, so the message is written in
// one buffer as the events arrive: the members of message_start, then the content, where a block's text goes in
// place into its string, which stays open until the block stops. What the endings add, the stop reason and the usage,
// is written after the content.
type messagesAggregate struct {
	aggBase
	ev messagesAggEvent
	// out is the message so far; blocks counts the blocks started and open says whether the last one has not stopped.
	out    []byte
	blocks int64
	open   bool
	cur    messagesBlock
	// tail and usage are the objects the message_start and message_delta events make: the stop reason and its kin,
	// and the usage, whose members later events take over. axiom is the ending's own member, if any.
	tail, usage, axiom []byte
}

// messagesBlock is the block being written, and the pieces that are written when it stops.
type messagesBlock struct {
	kind                 blockKind
	signature, citations []byte
	signed               bool
	// input is where a tool's input begins in the message, -1 before there is any; deltas says it is what the deltas
	// made, which replaced the block's own.
	input  int
	deltas bool
}

type messagesEventKind uint8

const (
	otherEvent messagesEventKind = iota
	messageStart
	blockStart
	blockDelta
	blockStop
	messageDelta
)

type messagesDeltaKind uint8

const (
	mdOther messagesDeltaKind = iota
	mdText
	mdThinking
	mdSign
	mdInput
	mdCitation
)

// The special members of a block, which wait for the block's type: only the kinds that use them write them
// themselves, and the others write them as they are.
const (
	specText = iota
	specThinking
	specSignature
	specInput
	specCitations
	specials
)

var (
	specialNames = [specials]string{"text", "thinking", "signature", "input", "citations"}
	// uses are the special members each kind of block writes itself.
	uses = map[blockKind][]int{
		textBlock: {specText, specCitations}, thinkingBlock: {specThinking, specSignature}, toolBlock: {specInput},
	}
	messagesEventKinds = map[string]messagesEventKind{
		"message_start": messageStart, "content_block_start": blockStart, "content_block_delta": blockDelta,
		"content_block_stop": blockStop, "message_delta": messageDelta,
	}
	messagesDeltaKinds = map[string]messagesDeltaKind{
		"text_delta": mdText, "thinking_delta": mdThinking, "signature_delta": mdSign,
		"input_json_delta": mdInput, "citations_delta": mdCitation,
	}
)

// messagesAggEvent is what an event says, its objects as written.
type messagesAggEvent struct {
	kind                                      messagesEventKind
	index                                     int64
	block, delta, message, usage, axiom       []byte
	deltaKind                                 messagesDeltaKind
	text, thinking, signature, partial, cited []byte
}

var (
	messagesAggFields = map[string]func(*messagesAggregate) error{
		"type": func(a *messagesAggregate) error {
			v, err := a.str()
			a.ev.kind = messagesEventKinds[string(v)]
			return err
		},
		"index":         func(a *messagesAggregate) error { return a.index(&a.ev.index) },
		"content_block": func(a *messagesAggregate) error { return a.capture(&a.ev.block) },
		"delta":         func(a *messagesAggregate) error { return a.capture(&a.ev.delta) },
		"message":       func(a *messagesAggregate) error { return a.capture(&a.ev.message) },
		"usage":         func(a *messagesAggregate) error { return a.capture(&a.ev.usage) },
		"axiom":         func(a *messagesAggregate) error { return a.capture(&a.ev.axiom) },
	}
	messagesAggDeltaFields = map[string]func(*messagesAggregate) error{
		"type": func(a *messagesAggregate) error {
			v, err := a.str()
			a.ev.deltaKind = messagesDeltaKinds[string(v)]
			return err
		},
		"text":         func(a *messagesAggregate) error { return a.captureString(&a.ev.text) },
		"thinking":     func(a *messagesAggregate) error { return a.captureString(&a.ev.thinking) },
		"signature":    func(a *messagesAggregate) error { return a.captureString(&a.ev.signature) },
		"partial_json": func(a *messagesAggregate) error { return a.captureString(&a.ev.partial) },
		"citation":     func(a *messagesAggregate) error { return a.capture(&a.ev.cited) },
	}
)

func (a *messagesAggregate) event(data []byte) error {
	a.begin(data)
	a.ev = messagesAggEvent{index: -1}
	if err := walk(&a.reader, a, messagesAggFields); err != nil {
		return upstreamFailure(err)
	}
	switch a.ev.kind {
	case messageStart:
		return a.start()
	case blockStart:
		return a.startBlock()
	case blockDelta:
		return a.deltaBlock()
	case blockStop:
		return a.stopBlock()
	case messageDelta:
		return a.endMessage()
	case otherEvent:
	}
	return nil
}

// start writes the message's members that stay as the upstream gave them, and opens its content.
func (a *messagesAggregate) start() error {
	if a.ev.message == nil || a.out != nil {
		return fmt.Errorf("%w: message_start without a message, or twice", ErrUpstream)
	}
	var err error
	if a.out, err = grow(&a.aggBase, a.out, len(a.ev.message)+len(`,"content":[`), 1); err != nil {
		return err
	}
	a.out = append(a.out, '{')
	usage := a.ev.usage
	if err = a.each(a.ev.message, func() error {
		v, err := a.value()
		switch string(a.name) {
		case "content", "stop_reason", "stop_sequence":
		case "usage":
			usage = v
		default:
			a.out = append(a.quoted(comma(a.out)), v...)
		}
		return err
	}); err != nil {
		return upstreamFailure(err)
	}
	a.out = append(comma(a.out), `"content":[`...)
	if err := a.set(&a.tail, []byte(`{"stop_reason":null,"stop_sequence":null}`), 1); err != nil || usage == nil {
		return err
	}
	return a.merge(&a.usage, usage, 1)
}

// comma separates a member or element from the one before it in dst.
func comma(dst []byte) []byte {
	if n := len(dst); n > 0 && dst[n-1] != '{' && dst[n-1] != '[' {
		return append(dst, ',')
	}
	return dst
}

// startBlock writes the block's members as they are, but for the special ones, which wait for its type.
func (a *messagesAggregate) startBlock() error {
	if a.out == nil || a.open || a.ev.index != a.blocks || a.ev.block == nil {
		return fmt.Errorf("%w: a block starts out of turn", ErrUpstream)
	}
	var err error
	if a.out, err = grow(&a.aggBase, a.out, len(a.ev.block)+64, 1); err != nil {
		return err
	}
	a.out = append(comma(a.out), '{')
	a.cur = messagesBlock{signature: a.cur.signature[:0], citations: a.cur.citations[:0], input: -1}
	var special [specials][]byte
	if err = a.each(a.ev.block, func() error {
		for i, name := range specialNames {
			if string(a.name) == name {
				return a.capture(&special[i])
			}
		}
		if string(a.name) == "type" {
			return a.blockType()
		}
		v, err := a.value()
		a.out = append(a.quoted(comma(a.out)), v...)
		return err
	}); err != nil {
		return upstreamFailure(err)
	}
	a.blocks++
	a.open = true
	return a.openBlock(&special)
}

// blockType writes the block's type, and takes the kind of block from it.
func (a *messagesAggregate) blockType() error {
	v, err := a.value()
	if err != nil {
		return err
	}
	a.cur.kind = otherBlock
	switch name := string(v); {
	case name == `"text"`:
		a.cur.kind = textBlock
	case name == `"thinking"`:
		a.cur.kind = thinkingBlock
	case strings.HasSuffix(name, `tool_use"`):
		a.cur.kind = toolBlock
	}
	a.out = append(a.quoted(comma(a.out)), v...)
	return nil
}

// openBlock writes the special members as the kind of block wants them: the text and thinking strings are left open
// for the deltas, a tool's own input is written, as it stays if no delta brings another.
func (a *messagesAggregate) openBlock(special *[specials][]byte) error {
	for i, v := range special {
		if v != nil && !slices.Contains(uses[a.cur.kind], i) {
			a.out = append(append(appendQuoted(comma(a.out), specialNames[i]), ':'), v...)
		}
	}
	switch a.cur.kind {
	case textBlock:
		a.openString("text", special[specText])
	case thinkingBlock:
		a.openString("thinking", special[specThinking])
		if v := special[specSignature]; v != nil {
			a.cur.signed = true
			return a.add(&a.cur.signature, v, 2)
		}
	case toolBlock:
		if v := special[specInput]; v != nil {
			a.out = append(append(comma(a.out), `"input":`...), v...)
			a.cur.input = len(a.out) - len(v)
		}
	case otherBlock, redactedBlock, signatureDelta, inputDelta:
	}
	return nil
}

// openString starts the block's string member name, with the contents of initial, a string or nil.
func (a *messagesAggregate) openString(name string, initial []byte) {
	a.out = append(append(append(comma(a.out), '"'), name...), `":"`...)
	if len(initial) >= 2 {
		a.out = append(a.out, initial[1:len(initial)-1]...)
	}
}

// deltaBlock adds a delta to the block being written.
func (a *messagesAggregate) deltaBlock() error {
	if !a.open || a.ev.index != a.blocks-1 || a.ev.delta == nil {
		return fmt.Errorf("%w: a delta out of turn", ErrUpstream)
	}
	a.begin(a.ev.delta)
	if err := walk(&a.reader, a, messagesAggDeltaFields); err != nil {
		return upstreamFailure(err)
	}
	if a.ev.deltaKind == mdOther {
		return nil
	}
	if kind, known := deltaBlocks[a.ev.deltaKind]; !known || kind != a.cur.kind {
		return fmt.Errorf("%w: a delta for a block of another kind", ErrUpstream)
	}
	return a.applyDelta()
}

// deltaBlocks are the kind of block each delta is for.
var deltaBlocks = map[messagesDeltaKind]blockKind{
	mdText: textBlock, mdThinking: thinkingBlock, mdSign: thinkingBlock, mdInput: toolBlock, mdCitation: textBlock,
}

// applyDelta adds the delta, which is for the block being written, to it.
func (a *messagesAggregate) applyDelta() error {
	e := &a.ev
	switch e.deltaKind {
	case mdText:
		return a.add(&a.out, e.text, 1)
	case mdThinking:
		return a.add(&a.out, e.thinking, 1)
	case mdSign:
		a.cur.signed = true
		return a.add(&a.cur.signature, e.signature, 2)
	case mdInput:
		return a.input(e.partial)
	case mdCitation:
		return a.cite(e.cited)
	case mdOther:
	}
	return nil
}

// cite keeps a citation for the end of the text block.
func (a *messagesAggregate) cite(citation []byte) error {
	if citation == nil {
		return nil
	}
	b, err := grow(&a.aggBase, a.cur.citations, len(citation)+1, 2)
	if err != nil {
		return err
	}
	if len(b) > 0 {
		b = append(b, ',')
	}
	a.cur.citations = append(b, citation...)
	return nil
}

// input adds a piece of a tool's input, a JSON string whose contents are the next piece of the input's text. The
// first that is not empty replaces the input the block started with, if it had one.
func (a *messagesAggregate) input(piece []byte) error {
	if len(piece) < 2 || piece[0] != '"' {
		return fmt.Errorf("%w: partial_json is not a string", ErrUpstream)
	}
	if len(piece) == 2 {
		return nil
	}
	var err error
	if a.out, err = grow(&a.aggBase, a.out, len(piece)+len(`,"input":`), 1); err != nil {
		return err
	}
	if !a.cur.deltas {
		if a.cur.input >= 0 {
			a.out = a.out[:a.cur.input]
		} else {
			a.out = append(comma(a.out), `"input":`...)
			a.cur.input = len(a.out)
		}
		a.cur.deltas = true
	}
	if a.out, err = jsontext.AppendUnquote(a.out, piece); err != nil { // the piece is valid: it never grows
		return upstreamFailure(err)
	}
	return nil
}

func (a *messagesAggregate) stopBlock() error {
	if !a.open || a.ev.index != a.blocks-1 {
		return fmt.Errorf("%w: a block stops out of turn", ErrUpstream)
	}
	return a.closeBlock()
}

// closeBlock writes what waited for the block's end, and closes it: the string members, the signature and citations
// after them, a tool's input, which must be one JSON value by now, or `{}` when it had none.
func (a *messagesAggregate) closeBlock() error {
	b := &a.cur
	var err error
	if a.out, err = grow(&a.aggBase, a.out, len(b.signature)+len(b.citations)+64, 1); err != nil {
		return err
	}
	switch b.kind {
	case textBlock:
		a.out = append(a.out, '"')
		if len(b.citations) > 0 {
			a.out = append(append(append(a.out, `,"citations":[`...), b.citations...), ']')
		}
	case thinkingBlock:
		a.out = append(a.out, '"')
		if b.signed {
			a.out = append(append(append(a.out, `,"signature":"`...), b.signature...), '"')
		}
	case toolBlock:
		if b.input < 0 {
			a.out = append(comma(a.out), `"input":{}`...)
		} else if b.deltas {
			if err := jsonx.Validate(a.out[b.input:]); err != nil {
				return fmt.Errorf("%w: a tool's input is not JSON: %w", ErrUpstream, err)
			}
		}
	case otherBlock, redactedBlock, signatureDelta, inputDelta:
	}
	a.out, a.open = append(a.out, '}'), false
	return nil
}

// endMessage takes what a message_delta says of the message: the stop reason and the usage, which take over the
// members they name.
func (a *messagesAggregate) endMessage() error {
	if a.out == nil {
		return fmt.Errorf("%w: message_delta before message_start", ErrUpstream)
	}
	if a.open {
		if err := a.closeBlock(); err != nil {
			return err
		}
	}
	for _, part := range [...]struct {
		dst *[]byte
		add []byte
	}{{&a.tail, a.ev.delta}, {&a.usage, a.ev.usage}} {
		if part.add != nil {
			if err := a.merge(part.dst, part.add, 1); err != nil {
				return err
			}
		}
	}
	if a.ev.axiom != nil {
		return a.set(&a.axiom, a.ev.axiom, 1)
	}
	return nil
}

func (a *messagesAggregate) body() ([]byte, error) {
	if a.out == nil {
		return nil, fmt.Errorf("%w: the stream had no message", ErrUpstream)
	}
	if a.open {
		if err := a.closeBlock(); err != nil {
			return nil, err
		}
	}
	out := append(a.out, ']')
	if len(a.tail) > 2 {
		out = append(append(out, ','), a.tail[1:len(a.tail)-1]...)
	}
	out = append(append(out, `,"usage":`...), orEmptyObject(a.usage)...)
	if a.axiom != nil {
		out = append(append(out, `,"axiom":`...), a.axiom...)
	}
	return append(out, '}'), nil
}

func orEmptyObject(v []byte) []byte {
	if len(v) == 0 {
		return []byte("{}")
	}
	return v
}

// failed lets go of everything: a Messages reply that failed is an error without content.
func (a *messagesAggregate) failed() {
	a.out, a.cur, a.tail, a.usage, a.axiom, a.ev = nil, messagesBlock{}, nil, nil, nil, messagesAggEvent{}
	a.held, a.drawn = 0, 0
}
