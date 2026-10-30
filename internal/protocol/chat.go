package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // control values are compared after unquoting
	"fmt"
	"strconv"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// chatStream reads one OpenAI Chat stream (OpenAI《Chat Completions streaming events》, read 2026-10-14) and what the
// dialects we accept add on the response side: reasoning as reasoning (OpenRouter), reasoning_content (DeepSeek) or
// reasoning_details (OpenRouter), a provider member and a relay's cost in usage (OpenRouter), usage in a chunk that
// also carries choices, and no usage chunk at all.
//
// A chunk is split three ways. Its content goes on in place, so the content of every choice keeps its order. Each
// finish_reason in it becomes null there and is held, as a chunk of its own for that choice, until [DONE]: a
// finish_reason tells the client the choice is complete. Its usage becomes null there and goes to the meter; the
// client gets the usage chunk we write from the settlement. What is left goes on unless no choice in it has a member
// with a value but its index and its finish_reason, and no delta in it has a member with a value: the members, not
// the content counted, tell whether anything is left, so a role or logprobs next to a finish_reason still reach the
// client. [DONE] is the normal end once every choice that started has finished, and a chunk with a top-level error
// the upstream's failure (OpenRouter《Errors》, read 2026-10-14).
type chatStream struct {
	eventReader
	chatWriter // created is the upstream's first chunk's
	// choices are the choices started so far, each set once it gave a finish_reason.
	choices map[int64]bool
	// The chunk being read: the choice being read and its finish_reason, in the chunk's data; the finish signals split
	// from the chunk so far; whether anything but them is left; and what else it carries.
	index                             int64
	finish                            []byte
	held                              []byte
	more, failed, rewrites, noChoices bool
	report                            *openAIUsage
	reportErr                         error
	// The reasoning members of the delta being read, to be written under both names.
	reasoning, reasoningContent reasoningMember
}

func newChatStream(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	return newChatStreamReader(u, o, charge)
}

// newChatStreamReader is the reader behind newChatStream, for the converters that hold it directly: theirs is not the
// format the client reads, so it rewrites nothing and they set converting (README.md「跨格式的流」).
func newChatStreamReader(u UsageContext, o RelayOptions, charge func(int) error) *chatStream {
	s := &chatStream{choices: map[int64]bool{}}
	s.init(u, o, nil, charge)
	s.chatWriter = chatWriter{id: s.eventReader.id, model: s.eventReader.model, created: "0", legacy: o.legacyFunctions} // quoted by init already
	return s
}

var (
	chatFields = map[string]func(*chatStream) error{
		"id":      func(s *chatStream) error { return s.replace(s.eventReader.id) },
		"model":   func(s *chatStream) error { return s.replace(s.eventReader.model) },
		"created": (*chatStream).createdAt,
		"choices": func(s *chatStream) error {
			s.noChoices = true
			return s.eachElement(s.choice)
		},
		"usage":        (*chatStream).usage,
		"error":        (*chatStream).upstreamError,
		"service_tier": (*chatStream).serviceTier,
		"provider":     (*chatStream).rewritten,
		"axiom":        (*chatStream).rewritten,
	}
	choiceFields = map[string]func(*chatStream) error{
		"index":         (*chatStream).choiceIndex,
		"delta":         (*chatStream).delta,
		"finish_reason": (*chatStream).finishReason,
	}
	chatDeltaFields = map[string]func(*chatStream) error{
		"content": (*chatStream).visibleText, "refusal": (*chatStream).visibleText,
		"reasoning":         func(s *chatStream) error { return s.reasoningOf(&s.reasoning) },
		"reasoning_content": func(s *chatStream) error { return s.reasoningOf(&s.reasoningContent) },
		"reasoning_details": (*chatStream).rawContent,
		"tool_calls": func(s *chatStream) error {
			return s.eachElement(func() error { return walk(&s.reader, s, toolCallFields) })
		},
	}
	toolCallFields = map[string]func(*chatStream) error{
		"function": func(s *chatStream) error { return walk(&s.reader, s, functionFields) },
	}
	functionFields = map[string]func(*chatStream) error{"name": (*chatStream).visibleText, "arguments": (*chatStream).visibleText}
	// chatRewrites are for a chunk with members to drop: the others only get their edits in place. provider names the
	// upstream (docs/domains/gateway.md「转发规则」).
	chatRewrites = formatRules{members: map[string]rule{
		"id": (*rewriter).ourID, "model": (*rewriter).ourModel, "provider": (*rewriter).drop, "axiom": (*rewriter).drop,
		"choices": hiddenChatChoices,
	}}
)

func (s *chatStream) event(data []byte, m *meter) (event, error) {
	s.start(data)
	if string(data) == sse.Done {
		return s.done()
	}
	s.held, s.more, s.failed, s.rewrites, s.noChoices = s.held[:0], false, false, s.ctx.HidesReasoning, false
	if err := walk(&s.reader, s, chatFields); err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if s.report != nil {
		s.meter(m, s.failed)
	}
	if s.failed {
		return event{}, s.errorEvent("an error chunk")
	}
	if len(s.held) > 0 {
		s.held = append(s.held, "]}"...)
		s.e.withheld = s.held
	}
	s.settle()
	s.e.final = s.e.usage
	s.report, s.reportErr = nil, nil
	if s.rewrites {
		return s.clientEvent()
	}
	return s.e, nil
}

func (s *chatStream) clientEvent() (event, error) {
	if s.ctx.HidesReasoning && !s.converting {
		// A signed detail without text needs an empty text member for native replay.
		// Its added bytes are smaller than the original detail, so twice the event bounds the rewrite.
		var err error
		s.buf, err = sse.Grow(s.buf[:0], 2*len(s.e.data)+len(s.eventReader.id)+len(s.eventReader.model), s.charge)
		if err != nil {
			return event{}, err
		}
	}
	e, err := s.rewrite(chatRewrites)
	if err != nil || !s.ctx.HidesReasoning || s.converting || e.role != passive {
		return e, err
	}
	switch {
	case s.rw.chatOpaque:
		e.role = content
	case !s.rw.chatPayload && !e.usage && e.withheld == nil:
		e.role = skipped
	}
	return e, nil
}

func (s *chatStream) settle() {
	switch {
	case s.report != nil && s.noChoices: // the usage chunk, which our own replaces
		s.e.role = ending
	case s.e.content > 0 && !s.ctx.HidesReasoning:
		s.e.role = content
	case len(s.held) > 0 && !s.more: // only finish signals, which are held
		s.e.role = skipped
	}
}

// done is [DONE]: the normal end, once every choice that started has finished.
func (s *chatStream) done() (event, error) {
	for i, finished := range s.choices {
		if !finished {
			return event{}, fmt.Errorf("%w: [DONE] before choice %d finished", ErrUpstream, i)
		}
	}
	s.e.role = end
	return s.e, nil
}

// choiceSize is what the choices map is charged for each choice: a slot of a key and a value, 16 bytes, and its
// control byte, in a table that doubles when 7/8 full, so it holds up to 16/7 slots per choice: 39 bytes, rounded up.
const choiceSize = 48

// choice reads one choice of a chunk, starting it, and splits off its finish signal.
func (s *chatStream) choice() error {
	s.noChoices, s.index, s.finish = false, 0, nil
	if err := s.walkNoting(choiceFields, false); err != nil {
		return err
	}
	if _, started := s.choices[s.index]; !started {
		if err := s.charge(choiceSize); err != nil {
			return err
		}
		s.choices[s.index] = false
	}
	if s.finish == nil {
		return nil
	}
	s.choices[s.index] = true
	if s.index == 0 {
		if err := s.charge(max(0, len(s.finish)-len(s.first))); err != nil {
			return err
		}
		s.first = string(s.finish)
	}
	return s.hold()
}

// hold adds the choice's finish signal to the held chunk, charging its room first, the closing "]}" included.
func (s *chatStream) hold() error {
	head := []byte(",")
	if len(s.held) == 0 {
		head = s.chunk("[")
	}
	const room = len(`{"index":,"delta":{},"finish_reason":}]}`) + 20 // 20 digits hold any int64
	var err error
	if s.held, err = sse.Grow(s.held, len(head)+room+len(s.finish), s.charge); err != nil {
		return err
	}
	s.held = strconv.AppendInt(append(append(s.held, head...), `{"index":`...), s.index, 10)
	s.held = append(append(append(s.held, `,"delta":{},"finish_reason":`...), s.finish...), '}')
	return nil
}

// walkNoting reads an object by fields as walk does, and sets more for a member with a value but null that the client
// still needs once the finish signals are out: any member when all is set, else one fields does not name.
func (s *chatStream) walkNoting(fields map[string]func(*chatStream) error, all bool) error {
	return s.eachMember(func() error {
		read := fields[string(s.name)]
		s.more = s.more || (all || read == nil) && s.dec.PeekKind() != 'n'
		if read != nil {
			return read(s)
		}
		return s.skip()
	})
}

func (s *chatStream) choiceIndex() error {
	n, err := s.count()
	s.index = n
	return err
}

func (s *chatStream) createdAt() error {
	v, err := s.dec.ReadValue()
	if err == nil && s.created == "0" && v.Kind() == '0' {
		s.created = string(v)
	}
	return err
}

// finishReason takes a choice's finish_reason out of the chunk, leaving null in its place.
func (s *chatStream) finishReason() error {
	v, err := s.raw()
	if v == nil || err != nil {
		return err
	}
	to := int(s.dec.InputOffset())
	s.finish = s.e.data[to-len(v) : to]
	return s.edit(len(v), "null")
}

func (s *chatStream) upstreamError() error {
	s.failed = s.dec.PeekKind() != 'n'
	return errorObject(&s.reader, &s.eventReader, errorFields)
}

func (s *chatStream) rewritten() error {
	s.rewrites = true
	return s.skip()
}

// usage takes the usage out of the chunk for the meter, leaving null in its place.
func (s *chatStream) usage() error {
	if s.dec.PeekKind() == 'n' {
		return s.skip()
	}
	v, err := s.dec.ReadValue()
	if err != nil {
		return err
	}
	if err := s.edit(len(v), "null"); err != nil {
		return err
	}
	s.report, s.e.usage = &openAIUsage{}, true
	s.reportErr = jsonx.Unmarshal(v, s.report)
	return nil
}

// meter maps the usage just read onto billing items. final is set for the usage of the chunk that ended the stream:
// what it states is charged as it is rather than estimated (meter.take).
func (s *chatStream) meter(m *meter, final bool) {
	if s.reportErr != nil {
		m.invalid = fmt.Errorf("usage: %w", s.reportErr)
		return
	}
	r, err := s.report.items(s.tier)
	if err != nil {
		m.invalid = err
		return
	}
	m.take(r, nil, final)
}

func (s *chatStream) dropBuffers() {
	s.eventReader.dropBuffers()
	s.choices, s.held, s.finish, s.first, s.report, s.reportErr = nil, nil, nil, "", nil, nil
	s.reasoning, s.reasoningContent = reasoningMember{}, reasoningMember{}
}

// chatWriter writes the chunks of ours a Chat client gets: the usage chunk and the error chunk, and for a stream
// converted from another format every chunk. id, model and created are JSON values; first is choice 0's
// finish_reason, as written, which the usage chunk repeats.
type chatWriter struct {
	id, model, created, first string
	legacy                    bool
}

func newChatWriter(o RelayOptions, created string) chatWriter {
	return chatWriter{id: string(appendQuoted(nil, o.ID)), model: string(appendQuoted(nil, o.Model)), created: created, legacy: o.legacyFunctions}
}

// failure appends a chunk with a top-level error, finish_reason error and our error type in error.metadata
// (docs/domains/gateway.md「错误」).
func (s *chatWriter) failure(dst []byte, e Error, u catalog.Usage, cost string) []byte {
	data := s.chunk(`[{"index":0,"delta":{},"finish_reason":"error"}]`)
	data = append(appendOpenAIError(append(data, `,"error":`...), e), `,"usage":`...)
	data = append(appendOpenAIUsage(data, chatNames, u, cost), '}')
	return sse.AppendEvent(dst, "", data)
}

// usageEvent appends the usage chunk, written from the settlement whether or not the upstream sent one, so the client
// always sees the usage and cost. Like OpenRouter's, it has one choice with a delta without content that repeats the
// finish_reason, since many clients read choices[0] of every chunk (OpenRouter《Streaming》"The final usage chunk",
// read 2026-10-15).
func (s *chatWriter) usageEvent(dst []byte, u catalog.Usage, cost, top string) []byte {
	choices := "[]"
	if s.first != "" {
		reason := s.first
		if s.legacy && controlIs([]byte(reason), "tool_calls") {
			reason = `"function_call"`
		}
		choices = `[{"index":0,"delta":{"content":""},"finish_reason":` + reason + `}]`
	}
	data := append(appendOpenAIUsage(append(s.chunk(choices), `,"usage":`...), chatNames, u, cost), top+"}"...)
	return sse.AppendEvent(dst, "", data)
}

// chunk is the start of a chunk of ours with choices, missing its closing brace.
func (s *chatWriter) chunk(choices string) []byte {
	return []byte(`{"id":` + s.id + `,"object":"chat.completion.chunk","created":` + s.created + `,"model":` + s.model +
		`,"choices":` + choices)
}

// visibleText separates the commit point from the full content bound used for billing.
func (s *chatStream) visibleText() error {
	before := s.e.content
	err := s.text()
	if s.e.content > before {
		s.e.role = content
	}
	return err
}

// reasoningMember is a reasoning member of a delta: its value as written, or for a null where it ends, 0 for a member
// the delta does not have.
type reasoningMember struct {
	value   []byte
	nullEnd int
}

// reasoningOf counts a reasoning member as content and keeps where it is, to write its text under the other name.
func (s *chatStream) reasoningOf(m *reasoningMember) error {
	v, err := s.raw()
	s.addText(v)
	if err != nil {
		return err
	}
	to := int(s.dec.InputOffset())
	if v == nil {
		m.nullEnd = to
	} else {
		m.value = s.e.data[to-len(v) : to]
	}
	return nil
}

// delta reads a delta and writes its reasoning under both names. A relay or a vendor writes one of them (DeepSeek's
// reasoning_content, OpenRouter's reasoning); the client reads either (docs/domains/gateway.md「转发规则」). Both with a
// value are left as they are, and a name that is null takes the other's text.
func (s *chatStream) delta() error {
	s.reasoning, s.reasoningContent = reasoningMember{}, reasoningMember{}
	if err := s.walkNoting(chatDeltaFields, true); err != nil {
		return err
	}
	if s.ctx.HidesReasoning {
		return nil
	}
	if s.reasoning.value != nil && s.reasoningContent.value == nil {
		return s.mirror(s.reasoning.value, s.reasoningContent, `,"reasoning_content":`)
	}
	if s.reasoningContent.value != nil && s.reasoning.value == nil {
		return s.mirror(s.reasoningContent.value, s.reasoning, `,"reasoning":`)
	}
	return nil
}

// mirror writes value as the member into: in place of its null, or, when the delta has none, before the delta's
// closing brace, which the decoder has just read.
func (s *chatStream) mirror(value []byte, into reasoningMember, name string) error {
	if into.nullEnd > 0 {
		return s.editEcho(into.nullEnd-len("null"), into.nullEnd, "", value)
	}
	at := int(s.dec.InputOffset()) - 1
	return s.editEcho(at, at, name, value)
}

// controlIs compares a validated JSON string without normalizing payload bytes.
func controlIs(v []byte, want string) bool {
	var buf [32]byte
	name, err := jsontext.AppendUnquote(buf[:0], v)
	return err == nil && string(name) == want
}
