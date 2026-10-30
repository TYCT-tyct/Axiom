package protocol

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"strconv"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// completionsStream reads OpenAI's legacy text completions stream (OpenAI《Create completion》, read 2026-10-30):
// chunks whose choices carry text instead of a delta, a usage object in the last chunk when the request asked for one,
// and [DONE]. The relay writes the client's usage chunk from the settlement, as it does for Chat.
type completionsStream struct {
	eventReader
	completionsWriter
	choices                     map[int64]bool
	report                      *openAIUsage
	reportErr                   error
	failed, rewrites, noChoices bool
	index                       int64
}

func newCompletionsStream(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	s := &completionsStream{choices: map[int64]bool{}}
	s.init(u, o, nil, charge)
	s.completionsWriter = completionsWriter{id: s.eventReader.id, model: s.eventReader.model, created: "0"}
	return s
}

var (
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
	s.index = 0
	return walk(&s.reader, s, completionsChoiceFields)
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
	s.choices[s.index] = true
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

// completionsAggregate builds a text completion from the chunks of a completions stream.
type completionsAggregate struct {
	aggBase
	ev                                        completionsAggEvent
	pending                                   []completionsChoice
	at                                        map[int64]int
	id, created, model, usage, errored, axiom []byte
	choices                                   []completionsChoiceAgg
	hadError                                  bool
}

type completionsAggEvent struct {
	span                                      completionsChoice
	id, created, model, usage, errored, axiom []byte
}

type completionsChoice struct {
	index        int64
	text, finish []byte
	logprobs     []byte
}

type completionsChoiceAgg struct {
	index        int64
	text, finish []byte
	logprobs     []byte
}

var (
	completionsAggFields = map[string]func(*completionsAggregate) error{
		"id":      func(a *completionsAggregate) error { return a.capture(&a.ev.id) },
		"created": func(a *completionsAggregate) error { return a.capture(&a.ev.created) },
		"model":   func(a *completionsAggregate) error { return a.capture(&a.ev.model) },
		"choices": func(a *completionsAggregate) error { return a.eachElement(a.readChoice) },
		"usage":   func(a *completionsAggregate) error { return a.capture(&a.ev.usage) },
		"error":   func(a *completionsAggregate) error { return a.capture(&a.ev.errored) },
		"axiom":   func(a *completionsAggregate) error { return a.capture(&a.ev.axiom) },
	}
	completionsAggChoiceFields = map[string]func(*completionsAggregate) error{
		"index":         func(a *completionsAggregate) error { return a.index(&a.ev.span.index) },
		"text":          func(a *completionsAggregate) error { return a.capture(&a.ev.span.text) },
		"finish_reason": func(a *completionsAggregate) error { return a.capture(&a.ev.span.finish) },
		"logprobs":      func(a *completionsAggregate) error { return a.capture(&a.ev.span.logprobs) },
	}
)

func (a *completionsAggregate) read(data []byte) (bool, error) {
	if t := bytes.TrimSpace(data); len(t) == 0 || string(t) == sse.Done {
		return false, nil
	}
	a.begin(data)
	a.ev, a.pending = completionsAggEvent{}, a.pending[:0]
	if err := walk(&a.reader, a, completionsAggFields); err != nil {
		return false, upstreamFailure(err)
	}
	return true, nil
}

func (a *completionsAggregate) event(data []byte) error {
	if ok, err := a.read(data); !ok || err != nil {
		return err
	}
	if err := a.keep(); err != nil {
		return err
	}
	for _, c := range a.pending {
		if err := a.apply(c); err != nil {
			return err
		}
	}
	return nil
}

func (a *completionsAggregate) keep() error {
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

func (a *completionsAggregate) readChoice() error {
	a.ev.span = completionsChoice{}
	if err := walk(&a.reader, a, completionsAggChoiceFields); err != nil {
		return err
	}
	a.pending = append(a.pending, a.ev.span)
	return nil
}

func (a *completionsAggregate) choice(index int64) int {
	if i, ok := a.at[index]; ok {
		return i
	}
	i := len(a.choices)
	a.choices = append(a.choices, completionsChoiceAgg{index: index})
	a.at[index] = i
	return i
}

func (a *completionsAggregate) apply(c completionsChoice) error {
	i := a.choice(c.index)
	ch := &a.choices[i]
	if len(c.text) > 0 {
		if err := a.add(&ch.text, c.text, 2); err != nil {
			return err
		}
	}
	if c.finish != nil {
		if err := a.set(&ch.finish, c.finish, 1); err != nil {
			return err
		}
	}
	if c.logprobs != nil {
		if err := a.set(&ch.logprobs, c.logprobs, 1); err != nil {
			return err
		}
	}
	return nil
}

func (*completionsAggregate) failed() {}

func (a *completionsAggregate) body() ([]byte, error) {
	slices.SortFunc(a.choices, func(x, y completionsChoiceAgg) int { return cmp.Compare(x.index, y.index) })
	size := 512 + len(a.id) + len(a.created) + len(a.model) + len(a.usage) + len(a.errored) + len(a.axiom)
	for i := range a.choices {
		size += 128 + len(a.choices[i].text) + len(a.choices[i].logprobs)
	}
	out := append(make([]byte, 0, size), `{"id":`...)
	out = append(append(out, orNull(a.id)...), `,"object":"text_completion","created":`...)
	out = append(append(out, orZero(a.created)...), `,"model":`...)
	out = append(append(out, orNull(a.model)...), `,"choices":[`...)
	for i := range a.choices {
		c := &a.choices[i]
		out = append(strconv.AppendInt(append(out, `{"index":`...), c.index, 10), `,"text":`...)
		if len(c.text) == 0 {
			out = append(out, `""`...)
		} else {
			out = append(append(out, '"'), c.text...)
			out = append(out, '"')
		}
		out = append(out, `,"finish_reason":`...)
		switch {
		case len(c.finish) > 0:
			out = append(out, c.finish...)
		case a.hadError:
			out = append(out, `"error"`...)
		default:
			out = append(out, `"stop"`...)
		}
		out = append(append(out, `,"logprobs":`...), orNull(c.logprobs)...)
		out = append(out, '}')
		if i < len(a.choices)-1 {
			out = append(out, ',')
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
