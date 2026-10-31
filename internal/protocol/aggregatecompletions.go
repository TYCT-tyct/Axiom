package protocol

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"strconv"

	"axiom/internal/protocol/internal/sse"
)

// completionsAggregate builds a text completion from the chunks of a completions stream. Its logprobs are the one
// member a chunk cannot simply be appended as it is: each chunk carries a slice of four arrays, and the reply wants
// each array once (docs/domains/gateway.md「决策与理由」). The aggregate keeps the elements, not the objects: every
// element is parsed once and copied into its array's buffer before it is charged, and the body is encoded once at the
// end, so neither a chunk's history nor a temporary object is ever copied.
type completionsAggregate struct {
	aggBase
	ev                 completionsAggEvent
	pending            []completionsChoice
	at                 map[int64]int
	id, created, model []byte
	usage, errored     []byte
	axiom              []byte
	choices            []completionsChoiceAgg
	hadError, fixed    bool
}

type completionsAggEvent struct {
	span                               completionsChoice
	id, created, model, usage, errored []byte
	axiom                              []byte
}

// completionsChoice is a choice as one chunk wrote it: its members are slices of the event's data, valid until the
// next event, and its logprobs object is parsed after the event is read, when the choice's index is known.
type completionsChoice struct {
	index    int64
	text     []byte
	finish   []byte
	logprobs []byte
}

// completionsChoiceAgg is a choice of the reply: its text, the four logprobs arrays' elements separated by commas, and
// whether any chunk had a logprobs object (otherwise the reply writes null).
type completionsChoiceAgg struct {
	index    int64
	text     []byte
	finish   []byte
	logs     [completionsLogprobs][]byte
	logsSeen bool
}

// The logprobs arrays, in the order the reply writes them.
const (
	completionsTokens = iota
	completionsTokenLogprobs
	completionsTextOffset
	completionsTopLogprobs
	completionsLogprobs
)

var (
	completionsLogSlots = map[string]int{
		"tokens": completionsTokens, "token_logprobs": completionsTokenLogprobs,
		"text_offset": completionsTextOffset, "top_logprobs": completionsTopLogprobs,
	}

	//nolint:dupl // the completions aggregate mirrors chat's members but keeps its own types and rules
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

const (
	// completionsBodyFixed is what the body holds besides the choices: its braces and the members around them.
	completionsBodyFixed = 512
	// completionsChoiceFixed is what one choice holds besides its pieces: the index, the members' names and brackets,
	// and the null of a choice without logprobs.
	completionsChoiceFixed = 128
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
	if !a.fixed {
		if err := a.take(completionsBodyFixed, 1); err != nil {
			return err
		}
		a.fixed = true
	}
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

// keep takes the members of the chunk that are not choices: the first chunk's identity, and the last usage, error and
// axiom.
//
//nolint:dupl // the completions aggregate mirrors chat's members but keeps its own types and rules
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
	pending, err := grow(&a.aggBase, a.pending, 1, 1)
	if err != nil {
		return err
	}
	a.pending = append(pending, a.ev.span)
	return nil
}

func (a *completionsAggregate) choice(index int64) (int, error) {
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
	if err := a.take(completionsChoiceFixed, 1); err != nil {
		return 0, err
	}
	i := len(choices)
	a.choices = append(choices, completionsChoiceAgg{index: index})
	a.at[index] = i
	return i, nil
}

// apply adds a choice of a chunk to the reply. It runs after the whole event is read, so that parsing the logprobs
// object cannot disturb the reader that is still walking the chunk's choices.
func (a *completionsAggregate) apply(c completionsChoice) error {
	i, err := a.choice(c.index)
	if err != nil {
		return err
	}
	ch := &a.choices[i]
	// null is the value OpenAI writes for a member that says nothing yet; the reader reads it as absent, and the reply
	// writes its own default for a choice that never finished.
	if len(c.text) > 0 && !bytes.Equal(c.text, []byte("null")) {
		if err := a.add(&ch.text, c.text, 2); err != nil {
			return err
		}
	}
	if len(c.finish) > 0 && !bytes.Equal(c.finish, []byte("null")) {
		if err := a.set(&ch.finish, c.finish, 1); err != nil {
			return err
		}
	}
	if len(c.logprobs) > 0 && !bytes.Equal(c.logprobs, []byte("null")) {
		// Finish feeds the events it withheld after the charging ended, and a chunk after the usage ending is not
		// content: nothing it carries may be stored once grow can no longer ask for room.
		if a.sealed {
			return fmt.Errorf("%w: logprobs after the ending", ErrUpstream)
		}
		if err := a.logprobs(ch, c.logprobs); err != nil {
			return err
		}
	}
	return nil
}

// logprobs appends a chunk's logprobs object to the choice's arrays. The four members are the ones OpenAI documents;
// any other is dropped rather than carried into a reply whose shape the client does not expect.
func (a *completionsAggregate) logprobs(ch *completionsChoiceAgg, v []byte) error {
	if v[0] != '{' {
		return fmt.Errorf("%w: logprobs is not an object", ErrUpstream)
	}
	if err := a.each(v, func() error {
		slot, ok := completionsLogSlots[string(a.name)]
		if !ok {
			return a.skip()
		}
		return a.logprobsArray(&ch.logs[slot])
	}); err != nil {
		return upstreamFailure(err)
	}
	ch.logsSeen = true
	return nil
}

// logprobsArray appends one array's elements to the choice's buffer for it.
func (a *completionsAggregate) logprobsArray(dst *[]byte) error {
	return a.eachElement(func() error {
		v, err := a.value()
		if err != nil {
			return err
		}
		return a.appendLog(dst, v)
	})
}

// appendLog appends one element, separated from the ones before it. The room is charged before it is made, and the
// element is copied into it: a piece that waits for the body is charged twice (docs/domains/gateway.md「决策与理由」).
func (a *completionsAggregate) appendLog(dst *[]byte, v []byte) error {
	b, err := grow(&a.aggBase, *dst, len(v)+1, 2)
	if err != nil {
		return err
	}
	if len(b) > 0 {
		b = append(b, ',')
	}
	*dst = append(b, v...)
	return nil
}

func (*completionsAggregate) failed() {}

func (a *completionsAggregate) body() ([]byte, error) {
	slices.SortFunc(a.choices, func(x, y completionsChoiceAgg) int { return cmp.Compare(x.index, y.index) })
	size := completionsBodyFixed + len(a.id) + len(a.created) + len(a.model) + len(a.usage) + len(a.errored) + len(a.axiom)
	for i := range a.choices {
		c := &a.choices[i]
		size += completionsChoiceFixed + len(c.text) + len(c.finish)
		for _, l := range c.logs {
			size += len(l)
		}
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
		out = c.appendLogprobs(out)
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

// appendLogprobs writes a choice's logprobs: the four arrays, each from its buffer, or null when no chunk had one.
func (c *completionsChoiceAgg) appendLogprobs(out []byte) []byte {
	if !c.logsSeen {
		return append(out, `,"logprobs":null`...)
	}
	out = append(out, `,"logprobs":{"tokens":`...)
	out = appendLogArray(out, c.logs[completionsTokens])
	out = append(out, `,"token_logprobs":`...)
	out = appendLogArray(out, c.logs[completionsTokenLogprobs])
	out = append(out, `,"text_offset":`...)
	out = appendLogArray(out, c.logs[completionsTextOffset])
	out = append(out, `,"top_logprobs":`...)
	out = appendLogArray(out, c.logs[completionsTopLogprobs])
	return append(out, '}')
}

func appendLogArray(out, elements []byte) []byte {
	out = append(out, '[')
	out = append(out, elements...)
	return append(out, ']')
}
