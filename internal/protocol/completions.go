package protocol

import (
	"fmt"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// completionsStream reads OpenAI's legacy text completions stream (OpenAI《Create completion》, read 2026-10-31):
// chunks whose choices carry text instead of a delta, a usage object in the last chunk when the request asked for one,
// and [DONE]. The relay writes the client's usage chunk from the settlement, as it does for Chat.
type completionsStream struct {
	eventReader
	completionsWriter
	choices                                     map[int64]bool
	report                                      *openAIUsage
	reportErr                                   error
	failed, rewrites, noChoices, choiceFinished bool
	index                                       int64
}

func newCompletionsStream(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	s := &completionsStream{choices: map[int64]bool{}}
	s.init(u, o, nil, charge)
	s.completionsWriter = completionsWriter{id: s.eventReader.id, model: s.eventReader.model, created: "0"}
	return s
}

var (
	//nolint:dupl // the completions reader mirrors chat's members but keeps its own types and rules
	completionsFields = map[string]func(*completionsStream) error{
		"id":           func(s *completionsStream) error { return s.replace(s.eventReader.id) },
		"model":        func(s *completionsStream) error { return s.replace(s.eventReader.model) },
		"created":      (*completionsStream).createdAt,
		"choices":      func(s *completionsStream) error { s.noChoices = true; return s.eachElement(s.choice) },
		"usage":        (*completionsStream).usage,
		"error":        (*completionsStream).upstreamError,
		"service_tier": (*completionsStream).serviceTier,
		"provider":     (*completionsStream).rewritten,
		"axiom":        (*completionsStream).rewritten,
	}
	completionsChoiceFields = map[string]func(*completionsStream) error{
		"index":         (*completionsStream).choiceIndex,
		"text":          (*completionsStream).textField,
		"finish_reason": (*completionsStream).finishReason,
		"logprobs":      (*completionsStream).skip,
	}
	// completionsRewrites are for a chunk with members to drop: the others only get their edits in place. provider
	// names the upstream, which a client must not see (docs/domains/gateway.md「转发规则」).
	completionsRewrites = formatRules{members: map[string]rule{
		"id": (*rewriter).ourID, "model": (*rewriter).ourModel, "provider": (*rewriter).drop, "axiom": (*rewriter).drop,
	}}
)

func (s *completionsStream) event(data []byte, m *meter) (event, error) {
	s.start(data)
	if string(data) == sse.Done {
		return s.done()
	}
	s.failed, s.rewrites, s.noChoices = false, false, false
	if err := walk(&s.reader, s, completionsFields); err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if s.report != nil {
		meterUsage(m, s.report, s.reportErr, s.tier, s.failed)
	}
	if s.failed {
		return event{}, s.errorEvent("an error chunk")
	}
	s.settle()
	s.e.final = s.e.usage
	s.report, s.reportErr = nil, nil
	if s.rewrites {
		return s.rewrite(completionsRewrites)
	}
	return s.e, nil
}

// settle marks the usage chunk as the ending, as Chat's does, and the first chunk with text as the commit point.
func (s *completionsStream) settle() {
	switch {
	case s.report != nil && s.noChoices:
		s.e.role = ending
	case s.e.content > 0:
		s.e.role = content
	}
}

// done is [DONE]: the normal end, once every choice that started has finished.
func (s *completionsStream) done() (event, error) {
	for i, finished := range s.choices {
		if !finished {
			return event{}, fmt.Errorf("%w: [DONE] before choice %d finished", ErrUpstream, i)
		}
	}
	s.e.role = end
	return s.e, nil
}

func (s *completionsStream) choice() error {
	// The choices array decides whether the chunk is a pure usage ending; a chunk with a choice is not, even when the
	// choice also carries usage. Reset before reading: noChoices is set once for the array, not once per element.
	s.noChoices, s.index, s.choiceFinished = false, 0, false
	if err := walk(&s.reader, s, completionsChoiceFields); err != nil {
		return err
	}
	// finish_reason may come before index in the object; apply it only after the whole choice is read, so the state
	// lands on the choice's own index.
	if s.choiceFinished {
		s.choices[s.index] = true
	}
	return nil
}

func (s *completionsStream) choiceIndex() error {
	n, err := s.count()
	s.index = n
	if _, started := s.choices[s.index]; !started {
		if err := s.charge(choiceSize); err != nil {
			return err
		}
		s.choices[s.index] = false
	}
	return err
}

// textField counts a choice's text as content and leaves it in place.
func (s *completionsStream) textField() error {
	before := s.e.content
	err := s.text()
	if s.e.content > before {
		s.e.role = content
	}
	return err
}

// finishReason marks the choice finished and leaves the signal where the upstream wrote it: unlike Chat, whose
// finish_reason splits into a chunk of its own so the usage chunk can follow, completions' shape puts it in the last
// chunk of the choice.
func (s *completionsStream) finishReason() error {
	v, err := s.str()
	if err != nil || v == nil {
		return err
	}
	s.choiceFinished = true
	return nil
}

func (s *completionsStream) createdAt() error {
	v, err := s.dec.ReadValue()
	if err == nil && s.created == "0" && v.Kind() == '0' {
		s.created = string(v)
	}
	return err
}

func (s *completionsStream) usage() error {
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

func (s *completionsStream) upstreamError() error {
	s.failed = s.dec.PeekKind() != 'n'
	return errorObject(&s.reader, &s.eventReader, errorFields)
}

func (s *completionsStream) rewritten() error {
	s.rewrites = true
	return s.skip()
}

func (s *completionsStream) dropBuffers() {
	s.eventReader.dropBuffers()
	s.choices, s.report = nil, nil
}

// completionsWriter writes the chunks of ours a completions client gets: the usage chunk and the error chunk.
type completionsWriter struct {
	id, model, created string
}

func (s *completionsWriter) failure(dst []byte, e Error, u catalog.Usage, cost string) []byte {
	data := s.chunk(`[{"index":0,"text":"","finish_reason":"error"}]`)
	data = append(appendOpenAIError(append(data, `,"error":`...), e), `,"usage":`...)
	data = append(appendOpenAIUsage(data, chatNames, u, cost), '}')
	return sse.AppendEvent(dst, "", data)
}

func (s *completionsWriter) usageEvent(dst []byte, u catalog.Usage, cost, top string) []byte {
	data := append(appendOpenAIUsage(append(s.chunk("[]"), `,"usage":`...), chatNames, u, cost), top+"}"...)
	return sse.AppendEvent(dst, "", data)
}

// chunk is the start of a chunk of ours with choices, missing its closing brace.
func (s *completionsWriter) chunk(choices string) []byte {
	return []byte(`{"id":` + s.id + `,"object":"text_completion","created":` + s.created + `,"model":` + s.model +
		`,"choices":` + choices)
}
