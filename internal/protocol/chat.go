package protocol

import (
	"fmt"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// chatStream reads one OpenAI Chat stream (OpenAI《Chat Completions streaming events》, read 2026-10-14) and what the
// dialects we accept add on the response side: reasoning as reasoning (OpenRouter), reasoning_content (DeepSeek) or
// reasoning_details (OpenRouter), a provider member and a relay's cost in usage (OpenRouter), usage in the chunk
// that finishes a choice, and no usage chunk at all. A chunk that finishes a choice is held until [DONE], since a
// finish_reason tells the client the reply is complete; the usage chunk is the ending, which the client gets as we
// write it from the settlement; [DONE] is the normal end, and a chunk with a top-level error the upstream's failure
// (OpenRouter《Errors》, read 2026-10-14).
type chatStream struct {
	eventReader
	created string // the created of the upstream's first chunk, for the chunks we write
	finish  string // the last finish_reason, as written, which our usage chunk repeats
	// The chunk being read.
	finished, failed, rewrites bool
	report                     *openAIUsage
	reportErr                  error
	tier                       *string
}

func newChatStream(u UsageContext, o RelayOptions) *chatStream {
	s := &chatStream{created: "0"}
	s.init(u, o, nil)
	return s
}

var (
	chatFields = map[string]func(*chatStream) error{
		"id":      func(s *chatStream) error { return s.replace(s.id) },
		"model":   func(s *chatStream) error { return s.replace(s.model) },
		"created": (*chatStream).createdAt,
		"choices": func(s *chatStream) error {
			return s.eachElement(func() error { return walk(&s.reader, s, choiceFields) })
		},
		"usage":        (*chatStream).usage,
		"error":        (*chatStream).upstreamError,
		"service_tier": (*chatStream).serviceTier,
		"provider":     (*chatStream).rewritten,
		"axiom":        (*chatStream).rewritten,
	}
	choiceFields = map[string]func(*chatStream) error{
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
	// chatRewrites are for a chunk with members to drop: the others only get their id and model edited in place.
	// provider names the upstream (docs/domains/gateway.md「转发规则」); a usage chunk never reaches the client.
	chatRewrites = formatRules{members: map[string]rule{
		"id": (*rewriter).identity, "model": (*rewriter).model, "provider": (*rewriter).drop, "axiom": (*rewriter).drop,
	}}
)

func (s *chatStream) event(data []byte, m *meter) (event, error) {
	s.start(data)
	if string(data) == sse.Done {
		s.e.role = end
		return s.e, nil
	}
	s.finished, s.failed, s.rewrites, s.report, s.reportErr = false, false, false, nil, nil
	if err := walk(&s.reader, s, chatFields); err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	switch {
	case s.failed:
		return event{}, fmt.Errorf("%w: an error chunk", ErrUpstream)
	case s.report != nil:
		s.e.role, s.e.final = ending, true
		s.meter(m)
	case s.finished:
		s.e.role = withheld
	case s.e.content > 0:
		s.e.role = content
	}
	if s.rewrites {
		return s.rewrite(chatRewrites)
	}
	return s.e, nil
}

func (s *chatStream) createdAt() error {
	v, err := s.dec.ReadValue()
	if err == nil && s.created == "0" && v.Kind() == '0' {
		s.created = string(v)
	}
	return err
}

func (s *chatStream) finishReason() error {
	v, err := s.raw()
	if v != nil {
		s.finished, s.finish = true, string(v)
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

func (s *chatStream) usage() error {
	if s.dec.PeekKind() == 'n' {
		return s.skip()
	}
	v, err := s.dec.ReadValue()
	if err != nil {
		return err
	}
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
	r, err := s.report.items(s.ctx, s.tier)
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
	if s.finish != "" {
		choices = `[{"index":0,"delta":{"content":""},"finish_reason":` + s.finish + `}]`
	}
	data := append(appendOpenAIUsage(append(s.chunk(choices), `,"usage":`...), chatNames, u, cost), top+"}"...)
	return sse.AppendEvent(dst, "", data)
}

// chunk is the start of a chunk of ours with choices, missing its closing brace.
func (s *chatStream) chunk(choices string) []byte {
	return []byte(`{"id":` + s.id + `,"object":"chat.completion.chunk","created":` + s.created + `,"model":` + s.model +
		`,"choices":` + choices)
}
