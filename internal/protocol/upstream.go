package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: events are read token by token, in place
	"fmt"

	"axiom/internal/catalog"
)

// upstream reads the events of one upstream format for the relay, and writes the endings the relay adds in that
// format (README.md「转发（草图）」).
type upstream interface {
	// event reads one event's data, giving any usage it reports to m.
	event(data []byte, m *meter) (event, error)
	// failure appends an error ending with the reply's usage u and cost, a list of members each led by a comma.
	failure(dst []byte, e Error, u catalog.Usage, cost string) []byte
	// usageEvent appends a usage event with u, cost and top, members for the usage and for the event; only a format
	// whose relayFormat says rendersUsage is asked.
	usageEvent(dst []byte, u catalog.Usage, cost, top string) []byte
}

// role is what an upstream event is to the relay.
type role uint8

const (
	passive   role = iota // written in order, held until the commit point
	content               // text, reasoning or a tool call: the first is the commit point
	heartbeat             // the upstream's keep-alive
	withheld              // a success signal, such as a Chat choice's finish_reason: held until the normal end
	ending                // carries the final usage; held until the settlement
	end                   // the upstream's normal end
)

// event is one upstream event as the relay sees it.
type event struct {
	role role
	name string
	// data is what the client gets in place of the event, but for edits, at most two replacements in place, ordered
	// by position; for an ending, usageAt is where its usage object closes, or -1 without one.
	data              []byte
	edits             [2]edit
	nEdits            int
	usageAt           int
	content, searches int64
	usage             bool // it reported usage, which the meter has taken
	final             bool // it is the format's last usage event
	ends              bool // an ending that is also the normal end, as Responses' response.completed is
}

// edit replaces data[from:to] with with.
type edit struct {
	from, to int
	with     string
}

// eventReader is what the readers of the three formats share: one decoder for every event, read in place, and the
// rewriter for the events whose identity and usage the client must not see as the upstream wrote them.
type eventReader struct {
	reader
	src bytes.Buffer
	ctx UsageContext
	rw  rewriter
	e   event
	// id and model are the call ID and the client's model name as JSON strings, for edits.
	id, model string
}

func (s *eventReader) init(u UsageContext, o RelayOptions, usageMembers map[string]rule) {
	s.ctx = u
	s.rw.w.Identity = func(string) string { return o.ID }
	s.rw.t.Model = o.Model
	s.rw.usageMembers = usageMembers
	s.id, s.model = string(appendQuoted(nil, o.ID)), string(appendQuoted(nil, o.Model))
}

// start begins reading an event.
func (s *eventReader) start(data []byte) {
	s.src = *bytes.NewBuffer(data)
	if s.dec == nil {
		s.dec = jsontext.NewDecoder(&s.src)
	} else {
		s.dec.Reset(&s.src)
	}
	s.e = event{role: passive, data: data, usageAt: -1}
}

// text counts a string member, unquoted, as content.
func (s *eventReader) text() error {
	v, err := s.raw()
	s.e.content += unquotedLen(v)
	return err
}

// rawContent counts a member as written, such as a tool's input or an output item, as content: its bytes bound
// the text in it from above.
func (s *eventReader) rawContent() error {
	v, err := s.dec.ReadValue()
	s.e.content += int64(len(v))
	return err
}

// replace reads a string member and edits it to with, a JSON string; null stays.
func (s *eventReader) replace(with string) error {
	v, err := s.raw()
	if v != nil && err == nil && s.e.nEdits < len(s.e.edits) {
		to := int(s.dec.InputOffset())
		s.e.edits[s.e.nEdits] = edit{to - len(v), to, with}
		s.e.nEdits++
	}
	return err
}

// rewrite rewrites the event by rules into a new array, which the relay copies before the next event.
func (s *eventReader) rewrite(rules formatRules) (event, error) {
	p := &s.rw
	p.dec, p.body, p.out, p.at, p.usageAt = jsontext.NewDecoder(bytes.NewBuffer(s.e.data)), s.e.data, nil, 0, -1
	if err := p.top(rules); err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	s.e.data, s.e.usageAt, s.e.nEdits = p.out, p.usageAt, 0
	return s.e, nil
}

// walk reads an object by fields, skipping the members fields does not name.
func walk[S any](r *reader, s S, fields map[string]func(S) error) error {
	return r.eachMember(func() error {
		if read := fields[string(r.name)]; read != nil {
			return read(s)
		}
		return r.skip()
	})
}

// editedSize is the length of e's data with its edits applied.
func editedSize(e *event) int {
	n := len(e.data)
	for _, ed := range e.edits[:e.nEdits] {
		n += len(ed.with) - (ed.to - ed.from)
	}
	return n
}

// edited appends data with the edits of e applied.
func edited(dst, data []byte, e *event) []byte {
	at := 0
	for _, ed := range e.edits[:e.nEdits] {
		dst = append(append(dst, data[at:ed.from]...), ed.with...)
		at = ed.to
	}
	return append(dst, data[at:]...)
}

// unquotedLen is the length of the JSON string v once unquoted, without unquoting it; 0 for null. v is valid JSON,
// so every escape is complete and every \u surrogate is half of a pair, which counts half of the pair's 4 bytes.
func unquotedLen(v jsontext.Value) int64 {
	if len(v) < 2 {
		return 0
	}
	var n int64
	for i := 1; i < len(v)-1; i++ {
		if v[i] != '\\' {
			n++
			continue
		}
		i++
		if v[i] != 'u' {
			n++
			continue
		}
		var r rune
		for _, c := range v[i+1 : i+5] {
			r = r<<4 | rune(c&0xf+c>>6*9) // a hex digit: '0'-'9' is 0x3_, 'A'-'F' and 'a'-'f' are 0x4_ and 0x6_
		}
		i += 4
		switch {
		case r < 0x80:
			n++
		case r < 0x800, 0xd800 <= r && r < 0xe000:
			n += 2
		default:
			n += 3
		}
	}
	return n
}

// usage copies a usage object's members the format defines and drops the rest, such as a relay's cost, and null,
// marking where the object closes (docs/domains/gateway.md「用量与费用」).
func (p *rewriter) usage() error {
	if ok, err := p.open('{', "an object"); !ok {
		p.at = p.offset()
		return err
	}
	if err := p.members(p.usageMembers, (*rewriter).drop); err != nil {
		return err
	}
	p.usageAt = len(p.out)
	return p.token()
}

// unknownCounts adds to r the non-zero counts in members, which the format's usage struct did not name, at path.
func unknownCounts(r *usageReport, path string, members map[string]jsontext.Value) {
	for name, v := range members {
		if v.Kind() == '0' && string(v) != "0" {
			r.unknown = append(r.unknown, catalog.UnknownUsage{Field: path + "." + name, Value: string(v)})
		}
	}
}

// keep is the rule set that keeps every member it names.
func keep(names ...string) map[string]rule {
	m := make(map[string]rule, len(names))
	for _, name := range names {
		m[name] = (*rewriter).skip
	}
	return m
}
