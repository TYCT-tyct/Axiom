package protocol

import (
	"bytes"

	"axiom/internal/protocol/internal/sse"
)

// choiceAggregate keeps the envelope common to Chat and text completions.
type choiceAggregate[S any] struct {
	aggBase
	aggregateFields
	span     S
	pending  []S
	ev       aggregateFields
	hadError bool
}

type aggregateFields struct {
	id, created, model, usage, errored, axiom []byte
}

func (a *choiceAggregate[S]) read(data []byte, readChoice func() error) (bool, error) {
	if t := bytes.TrimSpace(data); len(t) == 0 || string(t) == sse.Done {
		return false, nil
	}
	a.begin(data)
	var zero S
	a.ev, a.span, a.pending = aggregateFields{}, zero, a.pending[:0]
	err := a.eachMember(func() error {
		switch string(a.name) {
		case "id":
			return a.capture(&a.ev.id)
		case "created":
			return a.capture(&a.ev.created)
		case "model":
			return a.capture(&a.ev.model)
		case "usage":
			return a.capture(&a.ev.usage)
		case "error":
			return a.capture(&a.ev.errored)
		case "axiom":
			return a.capture(&a.ev.axiom)
		case "choices":
			return a.eachElement(readChoice)
		default:
			return a.skip()
		}
	})
	if err != nil {
		return false, upstreamFailure(err)
	}
	return true, nil
}

// keep takes the members of the chunk that are not choices: the first chunk's identity, and the last usage, error
// and axiom.
func (a *choiceAggregate[S]) keep() error {
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

func (a *choiceAggregate[S]) appendHead(out []byte, kind string) []byte {
	out = append(out, `{"id":`...)
	out = append(append(out, orElse(a.id, "null")...), `,"object":`...)
	out = append(appendQuoted(out, kind), `,"created":`...)
	out = append(append(out, orElse(a.created, "0")...), `,"model":`...)
	return append(append(out, orElse(a.model, "null")...), `,"choices":[`...)
}

func (a *choiceAggregate[S]) appendTail(out []byte) []byte {
	for _, m := range [...]struct {
		name string
		v    []byte
	}{{"usage", a.usage}, {"error", a.errored}, {"axiom", a.axiom}} {
		if len(m.v) > 0 {
			out = append(append(append(append(out, `,"`...), m.name...), `":`...), m.v...)
		}
	}
	return append(out, '}')
}
