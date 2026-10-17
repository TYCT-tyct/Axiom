package protocol

import (
	"fmt"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// responsesStream reads one OpenAI Responses stream (OpenAI《Responses streaming events》, read 2026-10-14). Every
// delta event is content; an output item that completes is content too, since some carry everything at once, such
// as a reasoning item with only encrypted content. response.completed and response.incomplete carry the final usage
// and end the stream; response.failed and error end it as the upstream's failure.
type responsesStream struct {
	eventReader
	seq     int64 // the last sequence_number, which the failure we write follows
	created bool  // response.created came, so the failure is response.failed
	// The event being read.
	index                  int64 // its output_index, or -1
	itemBytes              int64 // the raw bytes of its item
	webSearch, hasResponse bool
	report                 *openAIUsage
	reportErr              error
	// items is, for each output item added so far, the content its deltas carried: an item that completes counts only
	// what it adds to them, so the bound never counts the same text twice.
	items []int64
}

func newResponsesStream(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	s := &responsesStream{seq: -1}
	s.init(u, o, keep("input_tokens", "input_tokens_details", "output_tokens", "output_tokens_details", "total_tokens"),
		charge)
	return s
}

var (
	responsesFields = map[string]func(*responsesStream) error{
		"type":            (*responsesStream).eventType,
		"sequence_number": (*responsesStream).sequence,
		"output_index":    (*responsesStream).outputIndex,
		"delta":           (*responsesStream).delta,
		"item":            (*responsesStream).item,
		"response": func(s *responsesStream) error {
			s.hasResponse = true
			return walk(&s.reader, s, responseFields)
		},
	}
	responseFields = map[string]func(*responsesStream) error{
		"usage":        (*responsesStream).usage,
		"service_tier": (*responsesStream).serviceTier,
	}
	responsesItemFields = map[string]func(*responsesStream) error{"type": (*responsesStream).itemType}
	// responsesRewrites put the call ID and the client's model name in the response, keep its usage to the official
	// members, and drop an upstream's axiom member, which our ending may write.
	responsesRewrites = formatRules{members: map[string]rule{
		"response": object(map[string]rule{"id": (*rewriter).ourID, "model": (*rewriter).ourModel, "usage": (*rewriter).usage}),
		"axiom":    (*rewriter).drop,
	}}
)

func (s *responsesStream) event(data []byte, m *meter) (event, error) {
	s.start(data)
	s.index, s.itemBytes, s.webSearch, s.hasResponse, s.report, s.reportErr = -1, 0, false, false, nil, nil
	if err := walk(&s.reader, s, responsesFields); err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	switch s.e.name {
	case "response.failed", "error":
		return event{}, fmt.Errorf("%w: %s", ErrUpstream, s.e.name)
	case "response.created":
		s.created = true
	case "response.completed", "response.incomplete":
		s.e.role, s.e.ends, s.e.final = ending, true, true
	}
	if err := s.settle(); err != nil {
		return event{}, err
	}
	if s.report != nil {
		s.meter(m)
		s.report = nil
	}
	if s.hasResponse {
		return s.rewrite(responsesRewrites)
	}
	return s.e, nil
}

// settle counts the content once all members are read, since they come in any order.
func (s *responsesStream) settle() error {
	switch {
	case strings.HasSuffix(s.e.name, ".delta"):
		s.e.role = content
		if 0 <= s.index && s.index < int64(len(s.items)) {
			s.items[s.index] += s.e.content
		}
	case s.e.name == "response.output_item.added":
		if s.index == int64(len(s.items)) { // an index out of turn is not tracked: its item counts in full when done
			var err error
			if s.items, err = sse.Grow(s.items, 1, s.charge); err != nil {
				return err
			}
			s.items = append(s.items, 0)
		}
		if s.webSearch {
			s.e.searches = 1
		}
	case s.e.name == "response.output_item.done":
		s.e.role, s.e.content = content, s.itemBytes
		if 0 <= s.index && s.index < int64(len(s.items)) {
			s.e.content = max(0, s.itemBytes-s.items[s.index])
		}
	}
	return nil
}

func (s *responsesStream) dropBuffers() {
	s.eventReader.dropBuffers()
	s.items, s.report, s.reportErr = nil, nil, nil
}

func (s *responsesStream) eventType() error {
	v, err := s.str()
	name, known := responsesEventNames[string(v)] // the map's own string, so a known name costs no allocation
	if !known {
		name = string(v)
	}
	s.e.name = name
	return err
}

// responsesEventNames are the event types of OpenAI《Responses streaming events》 (read 2026-10-14) a stream of text,
// reasoning and function calls sends.
var responsesEventNames = func() map[string]string {
	m := map[string]string{}
	for _, name := range []string{
		"response.created", "response.in_progress", "response.queued", "response.completed", "response.incomplete",
		"response.failed", "error", "response.output_item.added", "response.output_item.done",
		"response.content_part.added", "response.content_part.done", "response.output_text.delta",
		"response.output_text.done", "response.output_text.annotation.added", "response.refusal.delta",
		"response.refusal.done", "response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_text.delta",
		"response.reasoning_text.done",
	} {
		m[name] = name
	}
	return m
}()

func (s *responsesStream) sequence() error {
	n, err := s.count()
	s.seq = max(s.seq, n)
	return err
}

func (s *responsesStream) outputIndex() error {
	n, err := s.count()
	s.index = n
	return err
}

// delta counts a delta: text for the text, reasoning and argument deltas, and its bytes as written for any other.
func (s *responsesStream) delta() error {
	if s.dec.PeekKind() == '"' {
		return s.text()
	}
	return s.rawContent()
}

// item reads an output item's type, and how many bytes it takes with the colon before it.
func (s *responsesStream) item() error {
	from := s.dec.InputOffset()
	err := walk(&s.reader, s, responsesItemFields)
	s.itemBytes = s.dec.InputOffset() - from
	return err
}

func (s *responsesStream) itemType() error {
	is, err := s.is("web_search_call")
	s.webSearch = is
	return err
}

func (s *responsesStream) usage() error {
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

func (s *responsesStream) meter(m *meter) {
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

// failure appends response.failed, with our error type at its top level (docs/domains/gateway.md「错误」), or, before
// response.created, the error event, which is the only failure a stream may start with.
func (s *responsesStream) failure(dst []byte, e Error, u catalog.Usage, cost string) []byte {
	name := "response.failed"
	data := fmt.Appendf(nil, `{"type":%q,"sequence_number":%d,`, name, s.seq+1)
	if s.created {
		data = append(data, `"response":{"id":`+s.id+`,"object":"response","status":"failed","model":`+s.model+
			`,"output":[],"error":{"code":"`+errorTypes[e.Type].name+`","message":`...)
		data = append(appendQuoted(data, e.message()), `},"usage":`...)
		data = appendOpenAIUsage(data, responsesNames, u, cost)
		data = append(data, '}')
	} else {
		name = "error"
		data = fmt.Appendf(nil, `{"type":"error","sequence_number":%d,"code":"%s","param":null,"message":`, s.seq+1,
			errorTypes[e.Type].name)
		data = append(appendQuoted(data, e.message()), `,"usage":`...)
		data = appendOpenAIUsage(data, responsesNames, u, cost)
	}
	data = append(appendErrorType(append(data, ','), e), '}')
	return sse.AppendEvent(dst, name, data)
}

// usageEvent is never needed: response.completed is the ending and the end at once.
func (*responsesStream) usageEvent(dst []byte, _ catalog.Usage, _, _ string) []byte { return dst }

// UsageOf reads the usage of a complete Responses reply, for the calls the gateway passes through whole: the compact
// endpoint does not stream, so its body is not relayed (docs/domains/gateway.md「转发规则」, OpenAI《Compaction》,
// read 2026-10-14). A reply that names no usage reports nothing of it, which the call estimates rather than waives; one
// protocol cannot read or map is invalid, which billing estimates too (docs/domains/billing.md「免单规则」).
func UsageOf(body []byte) Usage {
	m := meter{final: true}
	members, err := membersOf(body)
	if err != nil {
		m.invalid = fmt.Errorf("usage: %w", err)
		return m.result()
	}
	var report *openAIUsage
	var tier *string
	for _, mem := range members {
		switch mem.name {
		case "usage":
			report = &openAIUsage{}
			if err := jsonx.Unmarshal(mem.value, report); err != nil {
				m.invalid = fmt.Errorf("usage: %w", err)
				return m.result()
			}
		case "service_tier":
			var s string
			if jsonx.Unmarshal(mem.value, &s) == nil {
				tier = &s
			}
		}
	}
	if report != nil {
		if r, err := report.items(tier); err != nil {
			m.invalid = err
		} else {
			m.report(r, nil)
		}
	}
	return m.result()
}
