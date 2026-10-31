package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: events are read token by token, in place
	"errors"
	"fmt"
	"net/http"
	"slices"
	"unsafe"
)

// aggregateLimit is the most capacity the pieces of one aggregated reply may hold (README.md「非流式聚合」). It bounds
// the reply's content, not the memory charged for it: a piece that waits to be assembled is charged twice.
const aggregateLimit = 16 << 20

// aggregator builds the body of a reply for a client that did not ask for a stream, from the events the client's
// stream would have. There is one per client format; the relay hands it each event as it writes it.
type aggregator interface {
	// event takes one event's data, as the relay writes it. An event of a type it has no use for is ignored.
	event(data []byte) error
	// body returns the reply, after the ending: its success body, or, after an error ending, the failure body.
	body() ([]byte, error)
	// hold makes room for an event the relay withholds until the normal end, which is fed after seal: whatever it
	// will start is charged now, so that the cap and the node's budget bind it too.
	hold(data []byte) error
	// seal ends the charging: the ending is built from bytes already charged and charges nothing, as Finish and
	// Fail do.
	seal()
	// taken is what the aggregator drew from the relay's accounting.
	taken() int64
	// failed tells it the stream failed, so it may let go of whatever the failure body does not need.
	failed()
}

// aggBase is what the three aggregators share: a reader for the events' data, in place, and the accounting.
type aggBase struct {
	reader
	src  bytes.Buffer
	data []byte // the event being read
	// draw asks the relay to charge n bytes. held is the capacity of the pieces and bounded by aggregateLimit; drawn
	// is what was charged for them, which is more for a piece that is copied once more to make the body.
	draw        func(n int) error
	refund      func(n int)
	held, drawn int64
	sealed      bool
}

func (a *aggBase) init(draw func(int) error, refund func(int)) {
	a.draw, a.refund = draw, refund
	a.charge = func(n int) error { return a.take(n, 1) }
}

func (a *aggBase) seal()           { a.sealed = true }
func (*aggBase) hold([]byte) error { return nil }
func (a *aggBase) taken() int64    { return a.drawn }

// release gives back the accounting of pieces that were replaced or discarded. The relay's budget is released with
// it: what a merge held for its peak is not part of the reply that remains.
func (a *aggBase) release(n int) {
	if a.sealed || n <= 0 {
		return
	}
	if int64(n) > a.held {
		n = int(a.held)
	}
	if a.refund != nil {
		a.refund(n)
	}
	a.held -= int64(n)
	a.drawn = max(0, a.drawn-int64(n))
}

// take accounts for n bytes of pieces, factor times over in the relay's budget.
func (a *aggBase) take(n, factor int) error {
	if a.sealed {
		return nil
	}
	if a.held+int64(n) > aggregateLimit {
		return fmt.Errorf("%w: a reply's pieces pass %d bytes", ErrMemory, aggregateLimit)
	}
	if err := a.draw(n * factor); err != nil {
		return err
	}
	a.held += int64(n)
	a.drawn += int64(n * factor)
	return nil
}

// grow returns b with room for n more elements, charged as take says before it grows. It doubles, as sse.Grow does,
// but the slack of the doubling stops at the limit, so that a reply of exactly the limit's size is not turned away
// for the room it would not use.
func grow[T any](a *aggBase, b []T, n, factor int) ([]T, error) {
	if cap(b)-len(b) >= n {
		return b, nil
	}
	size := max(2*cap(b), len(b)+n)
	elem := int(unsafe.Sizeof(*new(T)))
	size = max(len(b)+n, min(size, cap(b)+int(aggregateLimit-a.held)/elem))
	if err := a.take((size-cap(b))*elem, factor); err != nil {
		return b, err
	}
	return append(make([]T, 0, size), b...), nil
}

// begin starts reading v, a value of the event's data or the data itself.
func (a *aggBase) begin(v []byte) {
	a.data, a.src = v, *bytes.NewBuffer(v)
	if a.dec == nil {
		a.dec = jsontext.NewDecoder(&a.src)
	} else {
		a.dec.Reset(&a.src)
	}
}

// value reads a value as written: a slice of the data, which lasts as long as the data.
func (a *aggBase) value() ([]byte, error) {
	v, err := a.dec.ReadValue()
	if err != nil {
		return nil, err
	}
	to := int(a.dec.InputOffset())
	return a.data[to-len(v) : to], nil
}

// capture reads a value into dst, which stays as it was for null.
func (a *aggBase) capture(dst *[]byte) error {
	v, err := a.value()
	if err == nil && string(v) != "null" {
		*dst = v
	}
	return err
}

// captureString is capture for a string.
func (a *aggBase) captureString(dst *[]byte) error {
	if ok, err := a.present('"', "a string"); !ok {
		return err
	}
	return a.capture(dst)
}

// index reads an index member into dst.
func (a *aggBase) index(dst *int64) error {
	n, err := a.count()
	*dst = n
	return err
}

// add appends the contents of piece, a JSON string as written, to those of the string in dst: the two together are
// one string's contents, as each escape lies whole within a piece.
func (a *aggBase) add(dst *[]byte, piece []byte, factor int) error {
	if len(piece) < 2 || piece[0] != '"' {
		return fmt.Errorf("%w: %s is not a string", ErrUpstream, a.name)
	}
	b, err := grow(a, *dst, len(piece)-2, factor)
	if err != nil {
		return err
	}
	*dst = append(b, piece[1:len(piece)-1]...)
	return nil
}

// set replaces what dst holds with v, reusing its room.
func (a *aggBase) set(dst *[]byte, v []byte, factor int) error {
	b, err := grow(a, (*dst)[:0], len(v), factor)
	if err != nil {
		return err
	}
	*dst = append(b, v...)
	return nil
}

// merge replaces the JSON object in dst with the one add makes of it (mergeObjects).
func (a *aggBase) merge(dst *[]byte, add []byte, factor int) error {
	merged, err := mergeObjects(*dst, add)
	if err != nil {
		return upstreamFailure(err)
	}
	return a.set(dst, merged, factor)
}

// each calls read for every member of the object v, after reading the member's name into name.
func (a *aggBase) each(v []byte, read func() error) error {
	a.begin(v)
	return a.eachMember(read)
}

// quoted appends the member name just read, quoted, and a colon.
func (a *aggBase) quoted(dst []byte) []byte {
	return append(appendQuoted(dst, string(a.name)), ':')
}

// objectMember is a member of a JSON object: its name unquoted, its value as written.
type objectMember struct {
	name  string
	value []byte
}

// membersOf reads the members of the JSON object obj, none for an empty obj.
func membersOf(obj []byte) ([]objectMember, error) {
	if len(obj) == 0 {
		return nil, nil
	}
	dec := jsontext.NewDecoder(bytes.NewBuffer(obj))
	if dec.PeekKind() != '{' {
		return nil, fmt.Errorf("%.40q is not a JSON object", obj)
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	var members []objectMember
	for dec.PeekKind() != '}' {
		name, err := dec.ReadValue()
		if err != nil {
			return nil, err
		}
		key, err := jsontext.AppendUnquote(nil, name)
		if err != nil {
			return nil, err
		}
		value, err := dec.ReadValue()
		if err != nil {
			return nil, err
		}
		members = append(members, objectMember{string(key), bytes.Clone(value)})
	}
	return members, nil
}

// mergeObjects returns the JSON object that has the members of old, each that add also has with the value from add,
// and then the other members of add. An empty old is an object without members. Both are objects.
func mergeObjects(old, add []byte) ([]byte, error) {
	have, err := membersOf(old)
	if err != nil {
		return nil, err
	}
	more, err := membersOf(add)
	if err != nil {
		return nil, err
	}
	for _, m := range more {
		if i := slices.IndexFunc(have, func(h objectMember) bool { return h.name == m.name }); i >= 0 {
			have[i].value = m.value
		} else {
			have = append(have, m)
		}
	}
	out := []byte{'{'}
	for _, m := range have {
		out = append(append(appendQuoted(comma(out), m.name), ':'), m.value...)
	}
	return append(out, '}'), nil
}

// newAggregator returns the aggregator for a client of format f.
//
//nolint:exhaustive // only the generation formats have an aggregator
func newAggregator(f Format, draw func(int) error, refund func(int)) aggregator {
	switch f {
	case Messages:
		a := &messagesAggregate{}
		a.init(draw, refund)
		return a
	case Responses:
		a := &responsesAggregate{}
		a.init(draw, refund)
		return a
	case Chat:
	case Completions:
		a := &completionsAggregate{at: map[int64]int{}}
		a.init(draw, refund)
		return a
	}
	a := &chatAggregate{at: map[int64]int{}}
	a.init(draw, refund)
	return a
}

// aggregate hands the aggregator what the step wrote, and lets nothing out: the body is the one thing written.
func (r *Relay) aggregate(step Step) (Step, error) {
	err := r.feed(r.out)
	r.out, step.Out = r.out[:0], nil
	return step, err
}

// feed gives the aggregator the events in b, framed as sse.AppendEvent frames them: the event's name, which the
// data repeats as its type, is not needed, and a comment is not an event.
func (r *Relay) feed(b []byte) error {
	for len(b) > 0 {
		var data []byte
		var err error
		if data, b, err = r.unframe(b); err != nil {
			return err
		}
		if err := r.agg.event(data); err != nil {
			return err
		}
	}
	return nil
}

// unframe cuts the first event off b and returns its data: a slice of b, or, for data of several lines, those lines
// joined in r.lines.
func (r *Relay) unframe(b []byte) (data, rest []byte, err error) {
	lines := 0
	for len(b) > 0 {
		var line []byte
		line, b, _ = bytes.Cut(b, []byte("\n"))
		if len(line) == 0 {
			return data, b, nil
		}
		piece, isData := bytes.CutPrefix(line, []byte("data: "))
		switch {
		case !isData:
		case lines == 0:
			data, lines = piece, 1
		default:
			if lines == 1 {
				r.lines = append(r.lines[:0], data...)
			}
			if r.lines, err = r.grow(r.lines, len(piece)+1); err != nil {
				return nil, nil, err
			}
			r.lines = append(append(r.lines, '\n'), piece...)
			data, lines = r.lines, lines+1
		}
	}
	return data, nil, nil
}

// finishAggregate returns the body of a reply that ended well, from the ending out, or, when the ending cannot make
// one, the upstream's failure.
func (r *Relay) finishAggregate(out []byte, f Final) []byte {
	r.agg.seal()
	if err := r.feed(out); err == nil {
		if body, err := r.agg.body(); err == nil {
			r.status, r.state = http.StatusOK, ended
			return body
		}
	}
	return r.Fail(Error{Type: UpstreamFailed}, &f)
}

// failAggregate returns the body of a reply that failed. Nothing generated yet: the answer the status table gives,
// which the gateway may still replace with another endpoint's. After the first content, Chat and Responses answer
// 200 with what was generated and the error in it, and Messages, which has no stop reason for an error, answers
// 502 with the error alone (docs/domains/gateway.md「错误」); either way the usage and cost are in the body.
func (r *Relay) failAggregate(e Error, f *Final, u Usage) []byte {
	pay := charges{usage: reply(u), cost: cost(f, r.client), pending: f != nil && f.Pending}
	status, body := renderError(r.client, e, &pay)
	r.status = status
	if !r.committed {
		return body
	}
	if r.client == Messages {
		r.status = http.StatusBadGateway
		return body
	}
	r.agg.seal()
	if err := r.feed(r.up.failure(nil, e, pay.usage, pay.cost)); err != nil {
		return body
	}
	failed, err := r.agg.body()
	if err != nil {
		return body
	}
	r.status = http.StatusOK
	return failed
}

// Status returns the HTTP status of the body Finish or Fail returned for a Relay that aggregates, 0 before either.
func (r *Relay) Status() int { return r.status }

// upstreamFailure is err as the upstream's failure, but for ErrMemory, which is ours: a stream that passed the cap is
// not the upstream's to retry on another endpoint.
func upstreamFailure(err error) error {
	if errors.Is(err, ErrMemory) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrUpstream, err)
}
