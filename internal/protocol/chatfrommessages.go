package protocol

import (
	"fmt"
	"strconv"

	"axiom/internal/protocol/internal/sse"
)

// messagesToChat converts a Messages stream for a Chat client (README.md「跨格式的流」, Anthropic《Streaming
// messages》, read 2026-10-14). Blocks come one after another: text becomes content, a tool_use block a tool call
// numbered from 0, thinking reasoning with its reasoning_details. Only the thinking before the first block of
// another kind keeps its signature, and only it gives its redacted_thinking: a Chat message cannot say where among
// its text and tool calls a later one was, and a replayed block must keep its place (README.md「跨格式的流」).
type messagesToChat struct {
	converted
	ev messagesEvent
	// The open block: its index, -1 when none; its kind; the Chat index of its tool call or reasoning detail; whether
	// its thinking keeps its signature; a tool's input as content_block_start gave it, and whether deltas replaced it.
	open           int64
	kind           blockKind
	tool, detail   int64
	signed, deltas bool
	stash          []byte
	leading        bool // no block but thinking has come yet
}

// messagesEvent is what an event says, its strings as written in its data.
type messagesEvent struct {
	index                       int64
	block, delta                blockKind
	id, name, input, text, sign []byte
	url, title                  []byte
	data, partial               []byte
	stop                        string // the finish reason the stop reason maps to, "" for none
	badStop                     string // a stop reason with no finish reason
}

type blockKind uint8

const (
	otherBlock blockKind = iota
	textBlock
	thinkingBlock
	redactedBlock
	toolBlock
	inputDelta
	signatureDelta
	citationDelta
)

// blockKinds and deltaKinds are the kinds of the block and delta types a converted request can bring about; any other is the
// upstream's failure, such as a server tool's block, which the request has none of. A citation is not one: a
// url_citation becomes a Chat annotation.
var (
	blockKinds = map[string]blockKind{
		"text": textBlock, "thinking": thinkingBlock, "redacted_thinking": redactedBlock, "tool_use": toolBlock,
	}
	deltaKinds = map[string]blockKind{
		"text_delta": textBlock, "thinking_delta": thinkingBlock, "signature_delta": signatureDelta,
		"input_json_delta": inputDelta, "citations_delta": citationDelta,
	}
)

// messagesFinish are the finish reasons of the stop reasons; pause_turn, which comes only with server tools, and any
// other are the upstream's failure.
var messagesFinish = map[string]string{
	"end_turn": `"stop"`, "stop_sequence": `"stop"`, "tool_use": `"tool_calls"`, "max_tokens": `"length"`,
	"model_context_window_exceeded": `"length"`, "refusal": `"content_filter"`,
}

const anthropicFormat = `,"format":"anthropic-claude-v1"`

func newMessagesToChat(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	inner := &messagesStream{}
	inner.init(u, o, messagesUsageMembers, charge)
	inner.converting = true
	s := &messagesToChat{open: -1, leading: true}
	s.init(inner, o, charge)
	s.hidesReasoning = u.HidesReasoning
	return s
}

var (
	messagesChatCitation = map[string]func(*messagesToChat) error{
		"url":   func(s *messagesToChat) error { return s.read(&s.ev.url) },
		"title": func(s *messagesToChat) error { return s.read(&s.ev.title) },
	}
	messagesChatFields = map[string]func(*messagesToChat) error{
		"index": func(s *messagesToChat) error {
			n, err := s.walk.count()
			s.ev.index = n
			return err
		},
		"content_block": func(s *messagesToChat) error { return walk(&s.walk, s, messagesChatBlock) },
		"delta":         func(s *messagesToChat) error { return walk(&s.walk, s, messagesChatDelta) },
	}
	messagesChatBlock = map[string]func(*messagesToChat) error{
		"type":      func(s *messagesToChat) error { return s.kindOf(&s.ev.block, blockKinds) },
		"id":        func(s *messagesToChat) error { return s.read(&s.ev.id) },
		"name":      func(s *messagesToChat) error { return s.read(&s.ev.name) },
		"input":     func(s *messagesToChat) error { v, err := s.value(); s.ev.input = v; return err },
		"text":      func(s *messagesToChat) error { return s.read(&s.ev.text) },
		"thinking":  func(s *messagesToChat) error { return s.read(&s.ev.text) },
		"signature": func(s *messagesToChat) error { return s.read(&s.ev.sign) },
		"data":      func(s *messagesToChat) error { return s.read(&s.ev.data) },
	}
	messagesChatDelta = map[string]func(*messagesToChat) error{
		"type":         func(s *messagesToChat) error { return s.kindOf(&s.ev.delta, deltaKinds) },
		"text":         func(s *messagesToChat) error { return s.read(&s.ev.text) },
		"thinking":     func(s *messagesToChat) error { return s.read(&s.ev.text) },
		"signature":    func(s *messagesToChat) error { return s.read(&s.ev.sign) },
		"partial_json": func(s *messagesToChat) error { return s.read(&s.ev.partial) },
		"citation":     func(s *messagesToChat) error { return walk(&s.walk, s, messagesChatCitation) },
		"stop_reason":  (*messagesToChat).stopReason,
	}
)

func (s *messagesToChat) read(dst *[]byte) error {
	v, err := s.text()
	*dst = v
	return err
}

func (s *messagesToChat) kindOf(dst *blockKind, kinds map[string]blockKind) error {
	v, err := s.walk.str()
	*dst = kinds[string(v)]
	return err
}

func (s *messagesToChat) stopReason() error {
	v, err := s.walk.str()
	if v != nil {
		if s.ev.stop = messagesFinish[string(v)]; s.ev.stop == "" {
			s.ev.badStop = string(v)
		}
	}
	return err
}

func (s *messagesToChat) event(data []byte, m *meter) (event, error) {
	e, err := s.begin(data, m)
	if err != nil {
		return event{}, err
	}
	s.ev = messagesEvent{index: -1}
	if err := walk(&s.walk, s, messagesChatFields); err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	r, finish, err := s.convert(e.name)
	if err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	return s.settle(e, r, finish)
}

// convert turns the event into the delta, or into the role it has.
func (s *messagesToChat) convert(name string) (role, []byte, error) {
	switch name {
	case "ping":
		return heartbeat, nil, nil
	case "message_start":
		s.draft.role, s.started = !s.started, true
	case "content_block_start":
		return passive, nil, s.blockStart()
	case "content_block_delta":
		return passive, nil, s.blockDelta()
	case "content_block_stop":
		return passive, nil, s.blockStop()
	case "message_delta":
		switch {
		case s.ev.badStop != "":
			return 0, nil, fmt.Errorf("stop reason %q", s.ev.badStop)
		case s.ev.stop != "" && !s.finished: // a later one, should one come, changes nothing the client was told
			return passive, s.hold(s.ev.stop), nil
		}
	case "message_stop":
		if !s.finished {
			return 0, nil, fmt.Errorf("message_stop without a stop reason")
		}
		return end, nil, nil
	}
	return passive, nil, nil
}

func (s *messagesToChat) blockStart() error {
	if s.open >= 0 {
		return fmt.Errorf("block %d started before block %d stopped", s.ev.index, s.open)
	}
	s.open, s.kind, s.deltas, s.stash = s.ev.index, s.ev.block, false, s.stash[:0]
	switch s.kind { //nolint:exhaustive // a citation is a delta, not a block that starts
	case textBlock:
		s.leading = false
		return s.addText(&s.draft.content, nonEmpty(s.ev.text))
	case thinkingBlock:
		s.detail, s.signed = s.details, s.leading
		s.details++
		if err := s.thinking(s.ev.text); err != nil {
			return err
		}
		return s.signature(s.ev.sign)
	case redactedBlock:
		if !s.leading || s.ev.data == nil {
			return nil
		}
		s.details++
		return s.addEntry(&s.draft.details, []byte(`{"type":"reasoning.encrypted","data":`), s.ev.data,
			[]byte(anthropicFormat), detailIndex(s.details-1), []byte("}"))
	case toolBlock:
		return s.toolStart()
	case otherBlock, inputDelta, signatureDelta:
	}
	return fmt.Errorf("block %d of a type a converted request does not ask for", s.ev.index)
}

// citation writes a citation delta's url_citation as the Chat annotation of the same name. A citation without a URL
// (a character or page location) has no Chat counterpart and is left out.
func (s *messagesToChat) citation() error {
	if len(s.ev.url) == 0 {
		return nil
	}
	b := make([]byte, 0, len(s.ev.url)+len(s.ev.title)+48)
	b = append(b, `{"type":"url_citation","url_citation":{"url":`...)
	b = append(b, s.ev.url...)
	b = append(b, `,"title":`...)
	if len(s.ev.title) == 0 {
		b = append(b, `null`...)
	} else {
		b = append(b, s.ev.title...)
	}
	b = append(b, `}}`...)
	return s.addCitation(b)
}

// toolStart starts a tool call, which needs the id and the name it is written with.
func (s *messagesToChat) toolStart() error {
	if nonEmpty(s.ev.id) == nil || nonEmpty(s.ev.name) == nil {
		return fmt.Errorf("block %d is a tool call without an id or a name", s.ev.index)
	}
	s.leading, s.tool = false, s.tools
	s.tools++
	var err error
	if s.stash, err = sse.Grow(s.stash, len(s.ev.input), s.walk.charge); err != nil {
		return err
	}
	s.stash = append(s.stash, s.ev.input...)
	return s.addEntry(&s.draft.tools, toolIndex(s.tool), []byte(`,"id":`), s.ev.id,
		[]byte(`,"type":"function","function":{"name":`), s.ev.name, []byte(`,"arguments":""}}`))
}

//nolint:cyclop // one delta kind per block kind is the grammar
func (s *messagesToChat) blockDelta() error {
	if s.open < 0 || s.ev.index != s.open {
		return fmt.Errorf("a delta for block %d, which is not open", s.ev.index)
	}
	switch {
	case s.ev.delta == textBlock && s.kind == textBlock:
		return s.addText(&s.draft.content, nonEmpty(s.ev.text))
	case s.ev.delta == thinkingBlock && s.kind == thinkingBlock:
		return s.thinking(s.ev.text)
	case s.ev.delta == signatureDelta && s.kind == thinkingBlock:
		return s.signature(s.ev.sign)
	case s.ev.delta == inputDelta && s.kind == toolBlock:
		s.deltas = true
		return s.arguments(nonEmpty(s.ev.partial))
	case s.ev.delta == citationDelta && s.kind == textBlock:
		return s.citation()
	}
	return fmt.Errorf("block %d gets a delta of a type it does not take", s.open)
}

func (s *messagesToChat) blockStop() error {
	if s.open < 0 || s.ev.index != s.open {
		return fmt.Errorf("block %d stops, which is not open", s.ev.index)
	}
	s.open = -1
	if s.kind != toolBlock || s.deltas || len(s.stash) == 0 {
		return nil
	}
	return s.arguments(appendQuoted(nil, string(s.stash))) // no deltas: the input as content_block_start gave it
}

// thinking adds a piece of the open thinking block's text, as reasoning and as its reasoning detail.
func (s *messagesToChat) thinking(piece []byte) error {
	if piece = nonEmpty(piece); piece == nil || s.hidesReasoning {
		return nil
	}
	if err := s.addText(&s.draft.reasoning, piece); err != nil {
		return err
	}
	return s.addEntry(&s.draft.details, []byte(`{"type":"reasoning.text","text":`), piece, []byte(anthropicFormat),
		detailIndex(s.detail), []byte("}"))
}

// signature gives the open thinking block's signature, in the entry of its reasoning detail, when it keeps it.
func (s *messagesToChat) signature(sign []byte) error {
	if sign = nonEmpty(sign); sign == nil || !s.signed {
		return nil
	}
	return s.addEntry(&s.draft.details, []byte(`{"type":"reasoning.text","text":"","signature":`), sign,
		[]byte(anthropicFormat), detailIndex(s.detail), []byte("}"))
}

func (s *messagesToChat) arguments(piece []byte) error {
	if piece == nil {
		return nil
	}
	return s.addEntry(&s.draft.tools, toolIndex(s.tool), []byte(`,"function":{"arguments":`), piece, []byte("}}"))
}

func (s *messagesToChat) dropBuffers() {
	s.converted.dropBuffers()
	s.ev, s.stash = messagesEvent{}, nil
}

// nonEmpty is the JSON string piece, or nil when it is null or empty.
func nonEmpty(piece []byte) []byte {
	if len(piece) <= 2 {
		return nil
	}
	return piece
}

// toolIndex starts a tool call's entry.
func toolIndex(n int64) []byte { return strconv.AppendInt([]byte(`{"index":`), n, 10) }
