package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: events are read token by token, in place
	"fmt"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// EchoPrompts is a legacy completion's echo: Bytes holds each prompt's text bytes in prompt order, and Choices is the
// n each prompt generates. A choice's text starts with its prompt repeated; the relay drops only the part it has
// received, so a batch that ends before every choice arrives still charges the text that did arrive.
type EchoPrompts struct {
	Bytes   []int64
	Choices int64
}

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
	// delivered is whether the chunk carries an answer for the client: text, echo included, or, for a request that
	// asked for no output, a finished choice. It alone decides the commit point, so an echo-only reply or an empty one
	// to max_tokens 0 is an answer, not an empty response. The meter's content counts only the generated text after
	// the echo.
	delivered, zeroOutput bool
	index                 int64
	indexSet              bool
	// pending holds text read before its choice's index; JSON members may come in any order. Only an upstream that
	// puts text before index allocates it.
	pending []jsontext.Value
	// echo holds each prompt's text bytes and the n each generates; echoSeen is how much of a choice's echo has
	// already arrived, so a batch that ends early only deducts what it received. Both are nil without echo.
	echo        *EchoPrompts
	echoChoices int64
	echoSeen    map[int64]int64
}

func newCompletionsStream(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	s := &completionsStream{choices: map[int64]bool{}, zeroOutput: o.ZeroOutput}
	if o.Echo != nil && len(o.Echo.Bytes) > 0 {
		s.echo, s.echoChoices, s.echoSeen = o.Echo, max(o.Echo.Choices, 1), map[int64]int64{}
	}
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
	s.failed, s.rewrites, s.noChoices, s.delivered = false, false, false, false
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

// settle marks the usage chunk as the ending, as Chat's does, and the first chunk with text, echoed or generated, as the
// commit point.
func (s *completionsStream) settle() {
	switch {
	case s.report != nil && s.noChoices:
		s.e.role = ending
	case s.delivered:
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
	s.noChoices, s.index, s.indexSet, s.choiceFinished = false, 0, false, false
	s.pending = s.pending[:0]
	if err := walk(&s.reader, s, completionsChoiceFields); err != nil {
		return err
	}
	if err := s.applyPendingText(); err != nil {
		return err
	}
	// finish_reason may come before index in the object; apply it only after the whole choice is read, so the state
	// lands on the choice's own index.
	if s.choiceFinished {
		s.choices[s.index] = true
		s.delivered = s.delivered || s.zeroOutput
	}
	return nil
}

func (s *completionsStream) choiceIndex() error {
	n, err := s.count()
	s.index, s.indexSet = n, true
	if err != nil {
		return err
	}
	if err := s.applyPendingText(); err != nil {
		return err
	}
	if _, started := s.choices[s.index]; !started {
		// echoSeen takes a slot for the choice too, of the same size as choices'.
		size := choiceSize
		if s.echo != nil {
			size *= 2
		}
		if err := s.charge(size); err != nil {
			return err
		}
		s.choices[s.index] = false
	}
	return nil
}

// textField keeps a choice's text in place. A choice's index decides how much of its text is the prompt's echo, and the
// index may come after the text, so text read before the index waits until it is known.
func (s *completionsStream) textField() error {
	v, err := s.raw()
	if err != nil || v == nil {
		return err
	}
	if s.indexSet {
		return s.addChoiceText(v)
	}
	s.pending = append(s.pending, v)
	return nil
}

// applyPendingText counts text that arrived before its choice's index.
func (s *completionsStream) applyPendingText() error {
	for _, v := range s.pending {
		if err := s.addChoiceText(v); err != nil {
			return err
		}
	}
	s.pending = s.pending[:0]
	return nil
}

// addChoiceText marks the chunk delivered when it has any text and meters the text after the echo this choice has
// already received. The prompt is repeated at the start of
// every choice, so each choice tracks its own prefix: a batch that ends before choice N returns must not deduct N's
// echo from the text choice 0 generated. The echoed bytes are still forwarded; only the meter leaves them out.
func (s *completionsStream) addChoiceText(v jsontext.Value) error {
	text, err := s.unquote(v)
	if err != nil {
		return err
	}
	if s.capture != nil {
		s.capture.add(v)
	}
	s.delivered = s.delivered || len(text) > 0
	if s.index >= 0 && s.echo != nil {
		if prompt := s.index / s.echoChoices; prompt < int64(len(s.echo.Bytes)) {
			seen := s.echoSeen[s.index]
			drop := min(int64(len(text)), max(s.echo.Bytes[prompt]-seen, 0))
			s.echoSeen[s.index] = seen + drop
			text = text[drop:]
		}
	}
	if len(text) == 0 {
		return nil
	}
	s.e.content += int64(len(text))
	s.e.shape.Add(text)
	return nil
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
	s.choices, s.report, s.pending, s.echo, s.echoSeen = nil, nil, nil, nil, nil
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
