package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: events are read token by token, in place
	"fmt"
	"slices"
)

// responsesAggregate builds a Responses response from the events of a Responses stream (README.md「非流式聚合」,
// OpenAI《Responses streaming events》, read 2026-10-14). A reply that ended well is the response the ending
// carries, which holds every output item whole; what the aggregate tracks is for the reply that failed: the items
// that are done as they were, and the ones still open rebuilt from their first event and the deltas that followed.
type responsesAggregate struct {
	aggBase
	ev    responsesAggEvent
	items []responseItem
	// createdAt is the created_at of response.created, which the failure we write has none of. snapshot is the response
	// of the ending, and failure the failure we wrote, both slices of the events' data.
	createdAt        []byte
	snapshot         []byte
	failure, errType []byte
	axiom            []byte // the ending's own member, which says the settlement is pending
}

// responseItem is an output item: whole once it is done, else its first event's item with the pieces the deltas
// made, which are strings as their contents.
type responseItem struct {
	raw     []byte
	done    bool
	args    []byte
	parts   []responsePart // content parts, or a reasoning item's text parts
	summary []responsePart
}

type responsePart struct {
	raw, text, annotations []byte
}

type responsesEventKind uint8

const (
	responsesOther responsesEventKind = iota
	responsesCreated
	responsesEnded
	responsesFailed
	itemAdded
	itemDone
	partAdded
	summaryAdded
	textDelta
	summaryDelta
	argumentsDelta
	annotationAdded
)

var responsesEventKinds = map[string]responsesEventKind{
	"response.created": responsesCreated, "response.completed": responsesEnded, "response.incomplete": responsesEnded,
	"response.failed": responsesFailed, "response.output_item.added": itemAdded, "response.output_item.done": itemDone,
	"response.content_part.added": partAdded, "response.reasoning_summary_part.added": summaryAdded,
	"response.output_text.delta": textDelta, "response.refusal.delta": textDelta, "response.reasoning_text.delta": textDelta,
	"response.reasoning_summary_text.delta": summaryDelta, "response.function_call_arguments.delta": argumentsDelta,
	"response.custom_tool_call_input.delta": argumentsDelta, "response.output_text.annotation.added": annotationAdded,
}

// responsesAggEvent is what an event says: its indexes, -1 when it has none, and its objects and delta as written.
type responsesAggEvent struct {
	kind                                      responsesEventKind
	item, content, summary                    int64
	object, part, annotation, response, delta []byte
	errType, axiom                            []byte
}

var responsesAggFields = map[string]func(*responsesAggregate) error{
	"type": func(a *responsesAggregate) error {
		v, err := a.str()
		a.ev.kind = responsesEventKinds[string(v)]
		return err
	},
	"output_index":  func(a *responsesAggregate) error { return a.index(&a.ev.item) },
	"content_index": func(a *responsesAggregate) error { return a.index(&a.ev.content) },
	"summary_index": func(a *responsesAggregate) error { return a.index(&a.ev.summary) },
	"item":          func(a *responsesAggregate) error { return a.capture(&a.ev.object) },
	"part":          func(a *responsesAggregate) error { return a.capture(&a.ev.part) },
	"annotation":    func(a *responsesAggregate) error { return a.capture(&a.ev.annotation) },
	"response":      func(a *responsesAggregate) error { return a.capture(&a.ev.response) },
	"delta":         func(a *responsesAggregate) error { return a.captureString(&a.ev.delta) },
	"error_type":    func(a *responsesAggregate) error { return a.capture(&a.ev.errType) },
	"axiom":         func(a *responsesAggregate) error { return a.capture(&a.ev.axiom) },
}

func (a *responsesAggregate) event(data []byte) error {
	a.begin(data)
	a.ev = responsesAggEvent{item: -1, content: -1, summary: -1}
	if err := walk(&a.reader, a, responsesAggFields); err != nil {
		return upstreamFailure(err)
	}
	e := &a.ev
	switch e.kind {
	case responsesCreated:
		return a.created()
	case responsesEnded:
		a.snapshot = e.response
		if len(e.response) == 0 || e.response[0] != '{' {
			return fmt.Errorf("%w: %s", ErrUpstream, "the ending has no response")
		}
		return a.set(&a.axiom, e.axiom, 1)
	case responsesFailed:
		a.snapshot, a.failure, a.errType = nil, e.response, e.errType
	case itemAdded, itemDone:
		return a.putItem(e.kind == itemDone)
	case partAdded, summaryAdded, textDelta, summaryDelta, argumentsDelta, annotationAdded:
		return a.addToItem()
	case responsesOther:
	}
	return nil
}

// created keeps the response's created_at.
func (a *responsesAggregate) created() error {
	if a.ev.response == nil {
		return nil
	}
	var v []byte
	if err := a.each(a.ev.response, func() error {
		if string(a.name) == "created_at" {
			return a.capture(&v)
		}
		return a.skip()
	}); err != nil {
		return upstreamFailure(err)
	}
	if v == nil {
		return nil
	}
	return a.set(&a.createdAt, v, 1)
}

// putItem takes an item as added, or as done: it replaces the item and its pieces, and when it comes alone it is the
// next one. An index out of turn is not tracked, as the relay does not track it.
func (a *responsesAggregate) putItem(done bool) error {
	e := &a.ev
	if e.object == nil || e.item < 0 || e.item > int64(len(a.items)) {
		return nil
	}
	if e.item == int64(len(a.items)) {
		var err error
		if a.items, err = grow(&a.aggBase, a.items, 1, 1); err != nil {
			return err
		}
		a.items = append(a.items, responseItem{})
	}
	if _, err := a.kindOf(e.object); err != nil {
		return err
	}
	it := &a.items[e.item]
	if done {
		it.done, it.args, it.parts, it.summary = true, nil, nil, nil
	}
	if !it.done || done {
		return a.set(&it.raw, e.object, 2)
	}
	return nil
}

// addToItem adds what an event brings to an item that is still open.
func (a *responsesAggregate) addToItem() error {
	e := &a.ev
	if e.item < 0 || e.item >= int64(len(a.items)) || a.items[e.item].done {
		return nil
	}
	it := &a.items[e.item]
	switch e.kind {
	case partAdded:
		return a.addPart(&it.parts, e.content, e.part)
	case summaryAdded:
		return a.addPart(&it.summary, e.summary, e.part)
	case argumentsDelta:
		return a.addDelta(&it.args)
	case textDelta:
		return a.addDeltaTo(it.parts, e.content)
	case summaryDelta:
		return a.addDeltaTo(it.summary, e.summary)
	case annotationAdded:
		return a.addAnnotation(it.parts)
	case responsesOther, responsesCreated, responsesEnded, responsesFailed, itemAdded, itemDone:
	}
	return nil
}

// addPart starts the part at index with its first event's part.
func (a *responsesAggregate) addPart(parts *[]responsePart, index int64, part []byte) error {
	if part == nil || index != int64(len(*parts)) {
		return nil
	}
	if _, err := a.kindOf(part); err != nil {
		return err
	}
	b, err := grow(&a.aggBase, *parts, 1, 1)
	if err != nil {
		return err
	}
	*parts = append(b, responsePart{})
	return a.set(&(*parts)[index].raw, part, 2)
}

func (a *responsesAggregate) addDelta(dst *[]byte) error {
	if a.ev.delta == nil {
		return nil
	}
	return a.add(dst, a.ev.delta, 2)
}

// addDeltaTo adds the delta to the text of the part at index.
func (a *responsesAggregate) addDeltaTo(parts []responsePart, index int64) error {
	if index < 0 || index >= int64(len(parts)) {
		return nil
	}
	return a.addDelta(&parts[index].text)
}

// addAnnotation keeps an annotation of the part for the end.
func (a *responsesAggregate) addAnnotation(parts []responsePart) error {
	e := &a.ev
	if e.content < 0 || e.content >= int64(len(parts)) || e.annotation == nil {
		return nil
	}
	p := &parts[e.content]
	b, err := grow(&a.aggBase, p.annotations, len(e.annotation)+1, 2)
	if err != nil {
		return err
	}
	if len(b) > 0 {
		b = append(b, ',')
	}
	p.annotations = append(b, e.annotation...)
	return nil
}

// body returns the response of the ending, or, for a reply that failed, the response we wrote for the failure with
// the output so far.
func (a *responsesAggregate) body() ([]byte, error) {
	if a.snapshot != nil {
		if len(a.axiom) == 0 {
			return a.snapshot, nil
		}
		// The snapshot is a slice of the ending, so the member goes on a copy of it.
		out := append(a.snapshot[:len(a.snapshot)-1:len(a.snapshot)-1], `,"axiom":`...)
		return append(append(out, a.axiom...), '}'), nil
	}
	if a.failure == nil {
		return nil, fmt.Errorf("%w: the stream had no response", ErrUpstream)
	}
	out := []byte{'{'}
	if err := a.each(a.failure, func() error {
		if string(a.name) == "output" {
			return a.skip()
		}
		v, err := a.value()
		out = append(a.quoted(comma(out)), v...)
		return err
	}); err != nil {
		return nil, upstreamFailure(err)
	}
	if len(a.createdAt) > 0 {
		out = append(comma(out), `"created_at":`...)
		out = append(out, a.createdAt...)
	}
	out, err := a.appendOutput(append(comma(out), `"output":`...))
	if err != nil {
		return nil, err
	}
	if a.errType != nil {
		out = append(append(comma(out), `"error_type":`...), a.errType...)
	}
	return append(out, '}'), nil
}

// appendOutput appends the output items: the ones that are done as they were, the others as far as they got.
func (a *responsesAggregate) appendOutput(out []byte) ([]byte, error) {
	out = append(out, '[')
	for i := range a.items {
		var err error
		if out, err = a.appendItem(comma(out), &a.items[i]); err != nil {
			return nil, err
		}
	}
	return append(out, ']'), nil
}

// The members an open item rebuilds from its pieces, by the item's type; others are as its first event gave them. An
// item that stopped before it was done is incomplete, whatever its first event said.
var (
	rebuilt = map[string][]string{
		"message": {"status", "content"}, "reasoning": {"status", "summary", "content"},
		"function_call": {"status", "arguments"}, "custom_tool_call": {"status", "input"},
	}
	statusOnly     = []string{"status"}
	textMembers    = []string{"text", "annotations"}
	refusalMembers = []string{"refusal", "annotations"}
)

// appendItem appends the item: whole if it is done, else as far as it got.
func (a *responsesAggregate) appendItem(out []byte, it *responseItem) ([]byte, error) {
	if it.done {
		return append(out, it.raw...), nil
	}
	out, kind, err := a.copyMembers(append(out, '{'), it.raw, rebuilt, statusOnly)
	if err != nil {
		return nil, err
	}
	out = append(comma(out), `"status":"incomplete"`...)
	switch kind {
	case "message":
		out, err = a.appendParts(append(out, `,"content":`...), it.parts)
	case "reasoning":
		out, err = a.appendParts(append(out, `,"summary":`...), it.summary)
		if err == nil && len(it.parts) > 0 {
			out, err = a.appendParts(append(out, `,"content":`...), it.parts)
		}
	case "function_call":
		out = appendContents(append(out, `,"arguments":`...), it.args, true)
	case "custom_tool_call":
		out = appendContents(append(out, `,"input":`...), it.args, true)
	}
	return append(out, '}'), err
}

// appendParts appends the parts as an array, each with the text its deltas made.
func (a *responsesAggregate) appendParts(out []byte, parts []responsePart) ([]byte, error) {
	out = append(out, '[')
	for _, p := range parts {
		var kind string
		var err error
		if out, kind, err = a.copyMembers(append(comma(out), '{'), p.raw, map[string][]string{"refusal": refusalMembers}, textMembers); err != nil {
			return nil, err
		}
		name := "text"
		if kind == "refusal" {
			name = "refusal"
		}
		out = appendContents(append(append(append(comma(out), '"'), name...), `":`...), p.text, true)
		if kind == "output_text" || len(p.annotations) > 0 {
			out = append(append(append(comma(out), `"annotations":[`...), p.annotations...), ']')
		}
		out = append(out, '}')
	}
	return append(out, ']'), nil
}

// copyMembers appends the members of the object obj but the ones its type has in skips, or else in other, and
// returns the type.
func (a *responsesAggregate) copyMembers(out, obj []byte, skips map[string][]string, other []string) ([]byte, string, error) {
	kind, err := a.kindOf(obj)
	if err != nil {
		return nil, "", err
	}
	var members []struct {
		name string
		v    []byte
	}
	err = a.each(obj, func() error {
		v, err := a.value()
		members = append(members, struct {
			name string
			v    []byte
		}{string(a.name), v})
		return err
	})
	if err != nil {
		return nil, "", upstreamFailure(err)
	}
	skip, ok := skips[kind]
	if !ok {
		skip = other
	}
	for _, m := range members {
		if !slices.Contains(skip, m.name) {
			out = append(appendQuoted(comma(out), m.name), ':')
			out = append(out, m.v...)
		}
	}
	return out, kind, nil
}

// kindOf is the type of the object obj, empty when it has none. A type that is not a string is the upstream's failure,
// found when the object arrives, not when a failure is written from it.
func (a *responsesAggregate) kindOf(obj []byte) (string, error) {
	var kind string
	err := a.each(obj, func() error {
		if string(a.name) != "type" {
			return a.skip()
		}
		v, err := a.value()
		if err != nil {
			return err
		}
		b, err := jsontext.AppendUnquote(nil, v)
		kind = string(b)
		return err
	})
	if err != nil {
		return "", upstreamFailure(err)
	}
	return kind, nil
}

// failed has nothing to let go of: the items are what the failure writes.
func (*responsesAggregate) failed() {}
