package protocol

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
)

// completionsAggregate builds a text completion from the chunks of a completions stream. Its logprobs are the one
// member a chunk cannot simply be appended as it is: each chunk carries a slice of four arrays, and the reply wants
// each array once (docs/domains/gateway.md「决策与理由」). The aggregate keeps the elements, not the objects: every
// element is parsed once and copied into its array's buffer before it is charged, and the body is encoded once at the
// end, so neither a chunk's history nor a temporary object is ever copied.
type completionsAggregate struct {
	choiceAggregate[completionsChoice]
	at      map[int64]int
	choices []completionsChoiceAgg
	fixed   bool
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

	completionsAggChoiceFields = map[string]func(*completionsAggregate) error{
		"index":         func(a *completionsAggregate) error { return a.index(&a.span.index) },
		"text":          func(a *completionsAggregate) error { return a.capture(&a.span.text) },
		"finish_reason": func(a *completionsAggregate) error { return a.capture(&a.span.finish) },
		"logprobs":      func(a *completionsAggregate) error { return a.capture(&a.span.logprobs) },
	}
)

const (
	// completionsBodyFixed is what the body holds besides the choices: its braces and the members around them.
	completionsBodyFixed = 512
	// completionsChoiceFixed is what one choice holds besides its pieces: the index, the members' names and brackets,
	// and the null of a choice without logprobs.
	completionsChoiceFixed = 128
)

func (a *completionsAggregate) event(data []byte) error {
	if !a.fixed {
		if err := a.take(completionsBodyFixed, 1); err != nil {
			return err
		}
		a.fixed = true
	}
	if ok, err := a.read(data, a.readChoice); !ok || err != nil {
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

func (a *completionsAggregate) readChoice() error {
	a.span = completionsChoice{}
	if err := walk(&a.reader, a, completionsAggChoiceFields); err != nil {
		return err
	}
	pending, err := grow(&a.aggBase, a.pending, 1, 1)
	if err != nil {
		return err
	}
	a.pending = append(pending, a.span)
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
	if len(c.text) > 0 {
		if err := a.add(&ch.text, c.text, 2); err != nil {
			return err
		}
	}
	if len(c.finish) > 0 {
		if err := a.set(&ch.finish, c.finish, 1); err != nil {
			return err
		}
	}
	if len(c.logprobs) > 0 {
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
		return a.appendEntry(dst, v, 2)
	})
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
	out := a.appendHead(make([]byte, 0, size), "text_completion")
	for i := range a.choices {
		c := &a.choices[i]
		out = append(strconv.AppendInt(append(comma(out), `{"index":`...), c.index, 10), `,"text":`...)
		out = appendContents(out, c.text, true)
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
	}
	out = append(out, ']')
	return a.appendTail(out), nil
}

// appendLogprobs writes a choice's logprobs: the four arrays, each from its buffer, or null when no chunk had one.
func (c *completionsChoiceAgg) appendLogprobs(out []byte) []byte {
	if !c.logsSeen {
		return append(out, `,"logprobs":null`...)
	}
	out = append(out, `,"logprobs":{`...)
	for i, name := range [...]string{"tokens", "token_logprobs", "text_offset", "top_logprobs"} {
		out = appendLogArray(append(appendQuoted(comma(out), name), ':'), c.logs[i])
	}
	return append(out, '}')
}

func appendLogArray(out, elements []byte) []byte {
	out = append(out, '[')
	out = append(out, elements...)
	return append(out, ']')
}
