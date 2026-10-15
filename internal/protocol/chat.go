package protocol

import (
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
// client gets the usage chunk we write from the settlement. [DONE] is the normal end once every choice that started
// has finished, and a chunk with a top-level error the upstream's failure (OpenRouter《Errors》, read 2026-10-14).
type chatStream struct {
	eventReader
	created string // the created of the upstream's first chunk, for the chunks we write
	// choices are the choices started so far, each with its finish_reason as written once it gave one.
	choices map[int64]string
	// The chunk being read: the choice being read, its finish_reason, the finish signals split from the chunk so
	// far, and what else it carries.
	index                       int64
	finish                      string
	held                        []byte
	failed, rewrites, noChoices bool
	report                      *openAIUsage
	reportErr                   error
	tier                        *string
}

func newChatStream(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	s := &chatStream{created: "0", choices: map[int64]string{}}
	s.init(u, o, nil, charge)
	return s
}

var (
	chatFields = map[string]func(*chatStream) error{
		"id":      func(s *chatStream) error { return s.replace(s.id) },
		"model":   func(s *chatStream) error { return s.replace(s.model) },
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
		"delta":         func(s *chatStream) error { return walk(&s.reader, s, chatDeltaFields) },
		"finish_reason": (*chatStream).finishReason,
	}
	chatDeltaFields = map[string]func(*chatStream) error{
		"content": (*chatStream).text, "refusal": (*chatStream).text, "reasoning": (*chatStream).text,
		"reasoning_content": (*chatStream).text, "reasoning_details": (*chatStream).rawContent,
		"tool_calls": func(s *chatStream) error {
			return s.eachElement(func() error { return walk(&s.reader, s, toolCallFields) })
		},
	}
	toolCallFields = map[string]func(*chatStream) error{
		"function": func(s *chatStream) error { return walk(&s.reader, s, functionFields) },
	}
	functionFields = map[string]func(*chatStream) error{"name": (*chatStream).text, "arguments": (*chatStream).text}
	// chatRewrites are for a chunk with members to drop: the others only get their edits in place. provider names the
	// upstream (docs/domains/gateway.md「转发规则」).
	chatRewrites = formatRules{members: map[string]rule{
		"id": (*rewriter).ourID, "model": (*rewriter).ourModel, "provider": (*rewriter).drop, "axiom": (*rewriter).drop,
	}}
)

func (s *chatStream) event(data []byte, m *meter) (event, error) {
	s.start(data)
	if string(data) == sse.Done {
		return s.done()
	}
	s.held, s.failed, s.rewrites, s.noChoices, s.report, s.reportErr = s.held[:0], false, false, false, nil, nil
	if err := walk(&s.reader, s, chatFields); err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if s.failed {
		return event{}, fmt.Errorf("%w: an error chunk", ErrUpstream)
	}
	if len(s.held) > 0 {
		s.held = append(s.held, "]}"...)
		s.e.withheld = s.held
	}
	switch {
	case s.report != nil && s.noChoices: // the usage chunk, which our own replaces
		s.e.role = ending
	case s.e.content > 0:
		s.e.role = content
	case len(s.held) > 0: // only finish signals, which are held
		s.e.role = skipped
	}
	if s.report != nil {
		s.e.final = true
		s.meter(m)
	}
	if s.rewrites {
		return s.rewrite(chatRewrites)
	}
	return s.e, nil
}

// done is [DONE]: the normal end, once every choice that started has finished.
func (s *chatStream) done() (event, error) {
	for i, finish := range s.choices {
		if finish == "" {
			return event{}, fmt.Errorf("%w: [DONE] before choice %d finished", ErrUpstream, i)
		}
	}
	s.e.role = end
	return s.e, nil
}

// choice reads one choice of a chunk, starting it, and splits off its finish signal.
func (s *chatStream) choice() error {
	s.noChoices, s.index, s.finish = false, 0, ""
	if err := walk(&s.reader, s, choiceFields); err != nil {
		return err
	}
	if _, started := s.choices[s.index]; !started {
		s.choices[s.index] = ""
	}
	if s.finish == "" {
		return nil
	}
	s.choices[s.index] = s.finish
	if len(s.held) == 0 {
		s.held = append(s.held, s.chunk("[")...)
	} else {
		s.held = append(s.held, ',')
	}
	s.held = strconv.AppendInt(append(s.held, `{"index":`...), s.index, 10)
	s.held = append(append(append(s.held, `,"delta":{},"finish_reason":`...), s.finish...), '}')
	return nil
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
	if v != nil && err == nil {
		s.finish = string(v)
		s.edit(len(v), "null")
	}
	return err
}

func (s *chatStream) upstreamError() error {
	s.failed = s.dec.PeekKind() != 'n'
	return s.skip()
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
	s.edit(len(v), "null")
	s.report, s.e.usage = &openAIUsage{}, true
	s.reportErr = jsonx.Unmarshal(v, s.report)
	return nil
}

func (s *chatStream) serviceTier() error {
	v, err := s.str()
	if v != nil {
		t := string(v)
		s.tier = &t
	}
	return err
}

func (s *chatStream) meter(m *meter) {
	if s.reportErr != nil {
		m.invalid = fmt.Errorf("usage: %w", s.reportErr)
		return
	}
	r, err := s.report.items(s.tier)
	if err != nil {
		m.invalid = err
		return
	}
	m.report(r, nil)
}

// chatErrorCodes are the HTTP statuses the error chunk's code gives for ours, as OpenRouter's error chunk gives one,
// matching the statuses of docs/domains/gateway.md「错误」.
var chatErrorCodes = [...]int{402, 503, 502, 408}

// failure appends a chunk with a top-level error, finish_reason error and our error type in error.metadata
// (docs/domains/gateway.md「错误」).
func (s *chatStream) failure(dst []byte, e Error, u catalog.Usage, cost string) []byte {
	data := s.chunk(`[{"index":0,"delta":{},"finish_reason":"error"}]`)
	data = fmt.Appendf(data, `,"error":{"code":%d,"message":`, chatErrorCodes[e.Type])
	data = append(appendQuoted(data, e.Message), `,"metadata":{"error_type":"`+errorTypeNames[e.Type]+`"}},"usage":`...)
	data = append(appendOpenAIUsage(data, chatNames, u, cost), '}')
	return sse.AppendEvent(dst, "", data)
}

// usageEvent appends the usage chunk, written from the settlement whether or not the upstream sent one, so the client
// always sees the usage and cost. Like OpenRouter's, it has one choice with a delta without content that repeats the
// finish_reason, since many clients read choices[0] of every chunk (OpenRouter《Streaming》"The final usage chunk",
// read 2026-10-15).
func (s *chatStream) usageEvent(dst []byte, u catalog.Usage, cost, top string) []byte {
	choices := "[]"
	if finish := s.choices[0]; finish != "" {
		choices = `[{"index":0,"delta":{"content":""},"finish_reason":` + finish + `}]`
	}
	data := append(appendOpenAIUsage(append(s.chunk(choices), `,"usage":`...), chatNames, u, cost), top+"}"...)
	return sse.AppendEvent(dst, "", data)
}

// chunk is the start of a chunk of ours with choices, missing its closing brace.
func (s *chatStream) chunk(choices string) []byte {
	return []byte(`{"id":` + s.id + `,"object":"chat.completion.chunk","created":` + s.created + `,"model":` + s.model +
		`,"choices":` + choices)
}
