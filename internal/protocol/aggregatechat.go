package protocol

import (
	"bytes"
	"fmt"

	"axiom/internal/protocol/internal/sse"
)

// chatAggregate builds a Chat completion from the chunks of a Chat stream, whether the upstream spoke Chat, Messages
// or Responses (README.md「非流式聚合」, OpenAI《Create chat completion》, OpenRouter《Reasoning tokens》, read
// 2026-10-15). Each choice keeps its pieces apart, since choices interleave, and a tool call's by its index; the body
// is made of them at the end. A piece that is copied to make the body is charged twice, and the reasoning details,
// which are decoded, merged and written again, four times.
type chatAggregate struct {
	aggBase
	ev      chatAggEvent
	pending []chatSpan
	curAt   int // the choice being applied, by its place in choices
	tool    chatTool
	// id, created and model are the first chunk's; usage, errored and axiom are those of the chunks that carry them.
	id, created, model, usage, errored, axiom []byte
	choices                                   []chatChoice
	at                                        map[int64]int // a choice's place in choices, by its index
	hadError                                  bool
	legacy                                    bool
	healing                                   bool
	cited                                     bool // the reply gets url_citation annotations at the end
	citeCharged                               bool // its extra working set has been drawn
}

// The pieces of a choice that are strings.
const (
	pieceContent = iota
	pieceRefusal
	pieceReasoning
	pieceReasoningContent
	pieces
)

// chatChoice is one choice's pieces: strings as their contents, escapes and all, without the quotes.
type chatChoice struct {
	index   int64
	strings [pieces][]byte
	// details and annotations are the entries of reasoning_details and annotations as the chunks gave them,
	// comma-separated; logs are those of logprobs' content and refusal, and logSeen whether the chunks had the array.
	details, annotations []byte
	logs                 [2][]byte
	logSeen              [2]bool
	finish               []byte
	tools                []chatTool
	toolAt               map[int64]int // a tool call's place in tools, by its index
}

type chatTool struct {
	index                     int64
	id, kind, name, arguments []byte
}

// chatSpan is a choice of the chunk being read, applied once the chunk is read.
type chatSpan struct {
	index                   int64
	delta, finish, logprobs []byte
}

// chatAggEvent is a chunk's members that are not choices, as written.
type chatAggEvent struct {
	span                                      chatSpan
	id, created, model, usage, errored, axiom []byte
}

// mapSlot is what a choice or a tool call is charged for its place in the map that finds it, besides the struct in its
// slice, as choiceSize works out.
const mapSlot = 48

var (
	chatAggFields = map[string]func(*chatAggregate) error{ //nolint:dupl // legacy completions' aggregate mirrors these members
		"id":      func(a *chatAggregate) error { return a.capture(&a.ev.id) },
		"created": func(a *chatAggregate) error { return a.capture(&a.ev.created) },
		"model":   func(a *chatAggregate) error { return a.capture(&a.ev.model) },
		"choices": func(a *chatAggregate) error { return a.eachElement(a.readChoice) },
		"usage":   func(a *chatAggregate) error { return a.capture(&a.ev.usage) },
		"error":   func(a *chatAggregate) error { return a.capture(&a.ev.errored) },
		"axiom":   func(a *chatAggregate) error { return a.capture(&a.ev.axiom) },
	}
	chatAggChoiceFields = map[string]func(*chatAggregate) error{
		"index":         func(a *chatAggregate) error { return a.index(&a.ev.span.index) },
		"delta":         func(a *chatAggregate) error { return a.capture(&a.ev.span.delta) },
		"finish_reason": func(a *chatAggregate) error { return a.capture(&a.ev.span.finish) },
		"logprobs":      func(a *chatAggregate) error { return a.capture(&a.ev.span.logprobs) },
	}
	chatAggLogFields = map[string]func(*chatAggregate) error{
		"content": func(a *chatAggregate) error { return a.logEntries(0) },
		"refusal": func(a *chatAggregate) error { return a.logEntries(1) },
	}
	chatAggDeltaFields = map[string]func(*chatAggregate) error{
		"content":           func(a *chatAggregate) error { return a.piece(pieceContent) },
		"refusal":           func(a *chatAggregate) error { return a.piece(pieceRefusal) },
		"reasoning":         func(a *chatAggregate) error { return a.piece(pieceReasoning) },
		"reasoning_content": func(a *chatAggregate) error { return a.piece(pieceReasoningContent) },
		"reasoning_details": func(a *chatAggregate) error { return a.eachElement(a.detail) },
		"annotations":       func(a *chatAggregate) error { return a.eachElement(a.annotation) },
		"tool_calls":        func(a *chatAggregate) error { return a.eachElement(a.readTool) },
	}
	chatAggToolFields = map[string]func(*chatAggregate) error{
		"index":    func(a *chatAggregate) error { return a.index(&a.tool.index) },
		"id":       func(a *chatAggregate) error { return a.captureString(&a.tool.id) },
		"type":     func(a *chatAggregate) error { return a.captureString(&a.tool.kind) },
		"function": func(a *chatAggregate) error { return walk(&a.reader, a, chatAggFunctionFields) },
	}
	chatAggFunctionFields = map[string]func(*chatAggregate) error{
		"name":      func(a *chatAggregate) error { return a.captureString(&a.tool.name) },
		"arguments": func(a *chatAggregate) error { return a.captureString(&a.tool.arguments) },
	}
)

// read reads a chunk into the event and its pending choices, unless it is [DONE] or has no data. Anything else is
// the JSON of a chunk, whatever whitespace leads it.
func (a *chatAggregate) read(data []byte) (bool, error) {
	if t := bytes.TrimSpace(data); len(t) == 0 || string(t) == sse.Done {
		return false, nil
	}
	a.begin(data)
	a.ev, a.pending = chatAggEvent{}, a.pending[:0]
	if err := walk(&a.reader, a, chatAggFields); err != nil {
		return false, upstreamFailure(err)
	}
	return true, nil
}

// hold makes room, as the relay withholds a chunk until the normal end, for the choices it starts and their finish
// reasons: Finish feeds the chunk when the charging has ended, so the cap and the node's budget are asked now.
func (a *chatAggregate) hold(data []byte) error {
	if ok, err := a.read(data); !ok || err != nil {
		return err
	}
	for _, s := range a.pending {
		i, err := a.choice(s.index)
		if err != nil {
			return err
		}
		c := &a.choices[i]
		if c.finish, err = grow(&a.aggBase, c.finish, max(0, len(s.finish)-len(c.finish)), 1); err != nil {
			return err
		}
	}
	return nil
}

func (a *chatAggregate) event(data []byte) error {
	if ok, err := a.read(data); !ok || err != nil {
		return err
	}
	if err := a.keep(); err != nil {
		return err
	}
	for _, s := range a.pending {
		if err := a.apply(s); err != nil {
			return err
		}
	}
	return nil
}

// keep takes the members of the chunk that are not choices: the first chunk's identity, and the last usage, error
// and axiom.
//
//nolint:dupl // legacy completions' aggregate mirrors this member list
func (a *chatAggregate) keep() error {
	e := &a.ev
	for _, m := range [...]struct {
		dst   *[]byte
		v     []byte
		first bool
	}{
		{&a.id, e.id, true},
		{&a.created, e.created, true},
		{&a.model, e.model, true},
		{&a.usage, e.usage, false},
		{&a.errored, e.errored, false},
		{&a.axiom, e.axiom, false},
	} {
		if m.v != nil && (!m.first || len(*m.dst) == 0) {
			if err := a.set(m.dst, m.v, 1); err != nil {
				return err
			}
		}
	}
	a.hadError = a.hadError || e.errored != nil
	return nil
}

// readChoice reads a choice of the chunk and queues it.
func (a *chatAggregate) readChoice() error {
	a.ev.span = chatSpan{}
	if err := walk(&a.reader, a, chatAggChoiceFields); err != nil {
		return err
	}
	var err error
	if a.pending, err = grow(&a.aggBase, a.pending, 1, 1); err != nil {
		return err
	}
	a.pending = append(a.pending, a.ev.span)
	return nil
}

// choice returns the place in choices of the choice with index, starting it. Nothing else points into choices, so
// that what a choice holds has one owner to be counted by.
func (a *chatAggregate) choice(index int64) (int, error) {
	if i, ok := a.at[index]; ok {
		return i, nil
	}
	choices, err := grow(&a.aggBase, a.choices, 1, 1)
	if err != nil {
		return 0, err
	}
	if err := a.take(mapSlot, 1); err != nil {
		return 0, err
	}
	a.at[index] = len(choices)
	a.choices = append(choices, chatChoice{index: index})
	return len(a.choices) - 1, nil
}

func (a *chatAggregate) cur() *chatChoice { return &a.choices[a.curAt] }

// apply adds a choice of a chunk to the reply.
func (a *chatAggregate) apply(s chatSpan) error {
	var err error
	if a.curAt, err = a.choice(s.index); err != nil {
		return err
	}
	if s.finish != nil {
		if err := a.set(&a.cur().finish, s.finish, 1); err != nil {
			return err
		}
	}
	for _, m := range [...]struct {
		v      []byte
		fields map[string]func(*chatAggregate) error
	}{{s.delta, chatAggDeltaFields}, {s.logprobs, chatAggLogFields}} {
		if m.v == nil {
			continue
		}
		a.begin(m.v)
		if err := walk(&a.reader, a, m.fields); err != nil {
			return upstreamFailure(err)
		}
	}
	return nil
}

// logEntries keeps the entries of logprobs' content (i is 0) or refusal (1).
func (a *chatAggregate) logEntries(i int) error {
	a.cur().logSeen[i] = a.cur().logSeen[i] || a.dec.PeekKind() == '['
	return a.eachElement(func() error { return a.entry(&a.cur().logs[i], 2) })
}

// detail and annotation keep an entry of reasoning_details and of annotations. The details are decoded, merged and
// written again, which holds them four times over.
func (a *chatAggregate) detail() error     { return a.entry(&a.cur().details, 4) }
func (a *chatAggregate) annotation() error { return a.entry(&a.cur().annotations, 2) }

// entry appends the array element just read to the comma-separated entries in dst.
func (a *chatAggregate) entry(dst *[]byte, factor int) error {
	v, err := a.value()
	if err != nil {
		return err
	}
	b, err := grow(&a.aggBase, *dst, len(v)+1, factor)
	if err != nil {
		return err
	}
	if len(b) > 0 {
		b = append(b, ',')
	}
	*dst = append(b, v...)
	return nil
}

// piece adds the string the delta has to the choice's piece i.
func (a *chatAggregate) piece(i int) error {
	var v []byte
	if err := a.captureString(&v); err != nil || v == nil {
		return err
	}
	factor := 2
	if a.healing && i == pieceContent {
		factor = healingMemoryFactor
	}
	if a.cited && i == pieceContent {
		if !a.citeCharged {
			if err := a.take(citationMemory, 1); err != nil {
				return err
			}
			a.citeCharged = true
		}
		factor = max(factor, citationFactor)
	}
	return a.add(&a.cur().strings[i], v, factor)
}

// readTool reads an entry of tool_calls, and adds it to the call with its index.
func (a *chatAggregate) readTool() error {
	a.tool = chatTool{}
	if err := walk(&a.reader, a, chatAggToolFields); err != nil {
		return err
	}
	t, err := a.cur().toolCall(a, a.tool.index)
	if err != nil {
		return err
	}
	for _, m := range [...]struct {
		dst *[]byte
		v   []byte
	}{{&t.id, a.tool.id}, {&t.kind, a.tool.kind}} {
		// An empty id or type is a vendor's filler on the fragments after the first (DashScope, DeepSeek), not a new value that
		// would empty the call's: sub2api drops them the same way (backend/internal/service/openai_gateway_cc_tool_call_identity.go,
		// stripEmptyChatToolCallIdentity, 8f2cbb5).
		if len(m.v) > len(`""`) {
			if err := a.set(m.dst, m.v, 1); err != nil {
				return err
			}
		}
	}
	for _, m := range [...]struct {
		dst *[]byte
		v   []byte
	}{{&t.name, a.tool.name}, {&t.arguments, a.tool.arguments}} {
		if m.v != nil {
			if err := a.add(m.dst, m.v, 2); err != nil {
				return err
			}
		}
	}
	return nil
}

// toolCall returns the call of the choice with index, starting it.
func (c *chatChoice) toolCall(a *chatAggregate, index int64) (*chatTool, error) {
	if a.legacy {
		if index != 0 {
			return nil, fmt.Errorf("%w: parallel calls in a legacy reply", ErrUpstream)
		}
		if len(a.tool.kind) > len(`""`) && !controlIs(a.tool.kind, "function") {
			return nil, fmt.Errorf("%w: a non-function call in a legacy reply", ErrUpstream)
		}
	}
	if i, ok := c.toolAt[index]; ok {
		return &c.tools[i], nil
	}
	tools, err := grow(&a.aggBase, c.tools, 1, 1)
	if err != nil {
		return nil, err
	}
	if err := a.take(mapSlot, 1); err != nil {
		return nil, err
	}
	if c.toolAt == nil {
		c.toolAt = map[int64]int{}
	}
	c.toolAt[index] = len(tools)
	c.tools = append(tools, chatTool{index: index})
	return &c.tools[len(c.tools)-1], nil
}

func (*chatAggregate) failed() {}
