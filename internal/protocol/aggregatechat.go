package protocol

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"strconv"

	"axiom/internal/platform/jsonx"
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
	chatAggFields = map[string]func(*chatAggregate) error{
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

func (a *chatAggregate) body() ([]byte, error) {
	a.heal()
	slices.SortFunc(a.choices, func(x, y chatChoice) int { return cmp.Compare(x.index, y.index) })
	size := 512 + len(a.id) + len(a.model) + len(a.usage) + len(a.errored) + len(a.axiom)
	for i := range a.choices {
		size += a.choices[i].size()
	}
	out := append(make([]byte, 0, size), `{"id":`...)
	out = append(append(out, orNull(a.id)...), `,"object":"chat.completion","created":`...)
	out = append(append(out, orZero(a.created)...), `,"model":`...)
	out = append(append(out, orNull(a.model)...), `,"choices":[`...)
	for i := range a.choices {
		var err error
		if out, err = a.choices[i].render(comma(out), a.hadError, a.legacy); err != nil {
			return nil, err
		}
	}
	out = append(out, ']')
	for _, m := range [...]struct {
		name string
		v    []byte
	}{{"usage", a.usage}, {"error", a.errored}, {"axiom", a.axiom}} {
		if len(m.v) > 0 {
			out = append(append(append(append(out, `,"`...), m.name...), `":`...), m.v...)
		}
	}
	return append(out, '}'), nil
}

// size is about what the choice takes in a body, so that the body is allocated once: its pieces and a margin for the
// members around them.
func (c *chatChoice) size() int {
	n := 256 + len(c.details) + len(c.finish) + len(c.annotations)
	for _, s := range c.strings {
		n += len(s)
	}
	for _, t := range c.tools {
		n += 128 + len(t.id) + len(t.name) + len(t.arguments)
	}
	return n
}

func orNull(v []byte) []byte {
	if len(v) == 0 {
		return []byte("null")
	}
	return v
}

func orZero(v []byte) []byte {
	if len(v) == 0 {
		return []byte("0")
	}
	return v
}

// render appends the choice as a completion's choice: the message, with the members a chunk's delta has only when it
// has them. A tool call's message has no content, as OpenAI's has none. A choice that never finished, when the reply
// failed, finished with the error.
func (c *chatChoice) render(out []byte, failed, legacy bool) ([]byte, error) {
	out = append(strconv.AppendInt(append(out, `{"index":`...), c.index, 10), `,"message":{"role":"assistant","content":`...)
	out = appendContents(out, c.strings[pieceContent], len(c.strings[pieceContent]) > 0 || len(c.tools) == 0)
	out = appendContents(append(out, `,"refusal":`...), c.strings[pieceRefusal], len(c.strings[pieceRefusal]) > 0)
	for _, m := range [...]struct {
		name string
		i    int
	}{{"reasoning", pieceReasoning}, {"reasoning_content", pieceReasoningContent}} {
		if len(c.strings[m.i]) > 0 {
			out = appendContents(append(append(append(out, `,"`...), m.name...), `":`...), c.strings[m.i], true)
		}
	}
	var err error
	if len(c.details) > 0 {
		if out, err = appendDetails(append(out, `,"reasoning_details":`...), c.details); err != nil {
			return nil, err
		}
	}
	if len(c.annotations) > 0 {
		out = append(append(append(out, `,"annotations":[`...), c.annotations...), ']')
	}
	if out, err = c.appendCalls(out, legacy); err != nil {
		return nil, err
	}

	out = c.appendLogprobs(append(out, `},"logprobs":`...))
	out = c.appendFinish(out, failed, legacy)
	return append(out, '}'), nil
}

// appendLogprobs appends the choice's logprobs: null when no chunk had any, else the object with the entries of
// content and refusal, each null when no chunk had that array.
func (c *chatChoice) appendLogprobs(out []byte) []byte {
	if !c.logSeen[0] && !c.logSeen[1] {
		return append(out, "null"...)
	}
	out = append(out, '{')
	for i, name := range [...]string{"content", "refusal"} {
		out = append(append(comma(out), '"'), name...)
		out = append(out, `":`...)
		if c.logSeen[i] {
			out = append(append(append(out, '['), c.logs[i]...), ']')
		} else {
			out = append(out, "null"...)
		}
	}
	return append(out, '}')
}

// appendContents appends a string whose contents are given, or null when it is not set.
func appendContents(out, contents []byte, set bool) []byte {
	if !set {
		return append(out, "null"...)
	}
	return append(append(append(out, '"'), contents...), '"')
}

// appendTools appends the calls as the array of a message's tool_calls.
func appendTools(out []byte, tools []chatTool) []byte {
	out = append(out, '[')
	for _, t := range tools {
		out = append(comma(out), `{"id":`...)
		out = append(out, orEmptyString(t.id)...)
		out = append(append(out, `,"type":`...), orFunction(t.kind)...)
		out = appendContents(append(out, `,"function":{"name":`...), t.name, true)
		out = appendContents(append(out, `,"arguments":`...), t.arguments, true)
		out = append(out, "}}"...)
	}
	return append(out, ']')
}

func orEmptyString(v []byte) []byte {
	if len(v) == 0 {
		return []byte(`""`)
	}
	return v
}

func orFunction(v []byte) []byte {
	if len(v) == 0 {
		return []byte(`"function"`)
	}
	return v
}

// appendDetails appends the entries merged, as a client hands them back (mergeDetails), as an array.
func appendDetails(out, entries []byte) ([]byte, error) {
	var details []canonReasoningDetail
	if err := jsonx.Unmarshal(slices.Concat([]byte{'['}, entries, []byte{']'}), &details); err != nil {
		return nil, fmt.Errorf("%w: reasoning_details: %w", ErrUpstream, err)
	}
	details, err := mergeDetails(details)
	if err != nil {
		return nil, fmt.Errorf("%w: reasoning_details: %w", ErrUpstream, err)
	}
	out = append(out, '[')
	for _, d := range details {
		out = appendQuoted(append(comma(out), `{"type":`...), d.Type)
		for _, m := range [...]struct {
			name string
			v    []byte
		}{{"text", d.Text}, {"summary", d.Summary}, {"signature", d.Signature}, {"data", d.Data}, {"id", d.ID}} {
			if !isNull(m.v) {
				out = append(append(append(comma(out), '"'), m.name...), `":`...)
				out = append(out, m.v...)
			}
		}
		if d.Format != "" {
			out = appendQuoted(append(comma(out), `"format":`...), d.Format)
		}
		if !isNull(d.Index) {
			out = append(append(comma(out), `"index":`...), d.Index...)
		}
		out = append(out, '}')
	}
	return append(out, ']'), nil
}

func (*chatAggregate) failed() {}

func (c *chatChoice) appendCalls(out []byte, legacy bool) ([]byte, error) {
	if len(c.tools) > 0 {
		if legacy {
			if len(c.tools) != 1 {
				return nil, fmt.Errorf("%w: parallel calls in a legacy reply", ErrUpstream)
			}
			t := c.tools[0]
			out = appendContents(append(out, `,"function_call":{"name":`...), t.name, true)
			out = appendContents(append(out, `,"arguments":`...), t.arguments, true)
			out = append(out, '}')
		} else {
			out = appendTools(append(out, `,"tool_calls":`...), c.tools)
		}
	}
	return out, nil
}

func (c *chatChoice) appendFinish(out []byte, failed, legacy bool) []byte {
	out = append(out, `,"finish_reason":`...)
	switch {
	case len(c.finish) > 0:
		if legacy && controlIs(c.finish, "tool_calls") {
			out = append(out, `"function_call"`...)
		} else {
			out = append(out, c.finish...)
		}
	case failed:
		out = append(out, `"error"`...)
	default:
		out = append(out, "null"...)
	}
	return out
}
