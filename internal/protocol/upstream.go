package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: events are read token by token, in place
	"fmt"

	"axiom/internal/catalog"
	"axiom/internal/protocol/internal/sse"
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
	// dropBuffers lets go of everything the reader holds for reading events, once no more are read: only the
	// strings failure writes stay.
	dropBuffers()
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
	skipped               // nothing the client needs, such as a Chat chunk left empty once its finish signals are out
)

// event is one upstream event as the relay sees it.
type event struct {
	role   role
	framed bool // a conversion may expand one upstream chunk into several complete SSE events
	name   string
	// data is what the client gets in place of the event, but for edits, replacements in place ordered by position;
	// for an ending, usageAt is where its usage object closes, or -1 without one. withheld, when set, is an event to
	// withhold besides, such as the finish signals Chat splits from a chunk that also carries content.
	data              []byte
	edits             []edit
	withheld          []byte
	usageAt           int
	content, searches int64
	usage             bool // it reported usage, which the meter has taken
	final             bool // it is the format's last usage event
	ends              bool // an ending that is also the normal end, as Responses' response.completed is
}

// edit replaces data[from:to] with with, then echo: a value of the event itself, copied again.
type edit struct {
	from, to int
	with     string
	echo     []byte
}

// eventReader is what the readers of the three formats share: one decoder for every event, read in place, and the
// rewriter for the events whose identity and usage the client must not see as the upstream wrote them, which
// writes into buf, charged as the relay's own buffers are and reused.
type eventReader struct {
	reader
	src bytes.Buffer
	ctx UsageContext
	rw  rewriter
	e   event
	buf []byte
	pre []byte // the event with its edits applied, before a rewrite; reused and charged as buf is
	// id and model are the call ID and the client's model name as JSON strings, for edits.
	id, model string
	tier      *string // the service tier the OpenAI formats name outside usage, kept for every usage report
	// converting is set for a reader whose events are converted to another format: nothing of the upstream's
	// identity reaches the client, so there is nothing to rewrite.
	converting bool
	// errClass and code are the fault the event's error names by its class and by its code, FaultNone when it names
	// none; the error event's failure is made of them. rejection is what it says besides, in its own words.
	errClass, code Fault
	rejection      Rejection
}

func (s *eventReader) init(u UsageContext, o RelayOptions, usageMembers map[string]rule, charge func(int) error) {
	s.ctx, s.charge, s.rw.charge = u, charge, charge
	s.rw.hidesReasoning = u.HidesReasoning
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
	s.e = event{role: passive, data: data, usageAt: -1, edits: s.e.edits[:0]}
	s.errClass, s.code = FaultNone, FaultNone
	s.rejection = Rejection{}
}

func (s *eventReader) dropBuffers() {
	s.reader, s.src, s.rw, s.buf, s.pre, s.e, s.tier = reader{}, bytes.Buffer{}, rewriter{}, nil, nil, event{}, nil
	s.rejection = Rejection{}
}

// serviceTier keeps the service tier the event names, charging what it holds beyond the one before.
func (s *eventReader) serviceTier() error {
	v, err := s.str()
	if v == nil || err != nil || s.tier != nil && *s.tier == string(v) {
		return err
	}
	had := 0
	if s.tier != nil {
		had = len(*s.tier)
	}
	if err := s.charge(max(0, len(v)-had)); err != nil {
		return err
	}
	t := string(v)
	s.tier = &t
	return nil
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

// replace reads a member and edits it to with, a JSON string, whatever the upstream wrote but null.
func (s *eventReader) replace(with string) error {
	v, err := s.dec.ReadValue()
	if err != nil || v.Kind() == 'n' {
		return err
	}
	return s.edit(len(v), with)
}

// edit replaces the n bytes just read with with.
func (s *eventReader) edit(n int, with string) error {
	to := int(s.dec.InputOffset())
	return s.editEcho(to-n, to, with, nil)
}

// editEcho replaces data[from:to] with with and then the bytes of echo, which lie in the event.
func (s *eventReader) editEcho(from, to int, with string, echo []byte) error {
	var err error
	if s.e.edits, err = sse.Grow(s.e.edits, 1, s.charge); err != nil {
		return err
	}
	s.e.edits = append(s.e.edits, edit{from, to, with, echo})
	return nil
}

// rewrite rewrites the event, its edits applied, by rules into buf, which the relay copies before the next event. Its
// room is charged first: a rewrite replaces two strings at most, the id and the model, with ours, and only drops the
// rest.
func (s *eventReader) rewrite(rules formatRules) (event, error) {
	if s.converting {
		return s.e, nil
	}
	data, err := s.apply()
	if err != nil {
		return event{}, err
	}
	if s.buf, err = sse.Grow(s.buf[:0], len(data)+len(s.id)+len(s.model), s.charge); err != nil {
		return event{}, err
	}
	p := &s.rw
	p.chatPayload, p.chatOpaque = false, false
	p.body, p.out, p.at, p.usageAt, p.ending = data, s.buf[:0], 0, -1, s.e.role == ending
	if p.dec == nil {
		p.dec = jsontext.NewDecoder(bytes.NewBuffer(data))
	} else {
		p.dec.Reset(bytes.NewBuffer(data))
	}
	if err := p.top(rules); err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if grown := cap(p.out) - cap(s.buf); grown > 0 { // the bound above failed: account for what append took
		if err := s.charge(grown); err != nil {
			return event{}, err
		}
	}
	s.buf = p.out
	s.e.data, s.e.usageAt, s.e.edits = p.out, p.usageAt, s.e.edits[:0]
	return s.e, nil
}

// apply returns the event's data with its edits applied, in pre when there are any.
func (s *eventReader) apply() ([]byte, error) {
	if len(s.e.edits) == 0 {
		return s.e.data, nil
	}
	var err error
	if s.pre, err = sse.Grow(s.pre[:0], editedSize(&s.e), s.charge); err != nil {
		return nil, err
	}
	s.pre = edited(s.pre, s.e.data, &s.e)
	return s.pre, nil
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
	for _, ed := range e.edits {
		n += len(ed.with) + len(ed.echo) - (ed.to - ed.from)
	}
	return n
}

// edited appends data with the edits of e applied.
func edited(dst, data []byte, e *event) []byte {
	at := 0
	for _, ed := range e.edits {
		dst = append(append(append(dst, data[at:ed.from]...), ed.with...), ed.echo...)
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

// usage copies a usage object's members the format defines and drops the rest, such as a relay's cost, marking where
// the object closes (docs/domains/gateway.md「用量与费用」). A null usage is kept, as OpenAI writes it in the events
// before the response ends (response.created); the ending's is dropped, since the settlement writes the usage object
// there and a second member of the name would be a duplicate.
func (p *rewriter) usage() error {
	if ok, err := p.open('{', "an object"); !ok {
		if err == nil && !p.ending {
			return nil
		}
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

// ourID and ourModel put the call ID and the client's model name in place of whatever value but null the upstream
// wrote: an id or a model of another type is still the upstream's, not the client's.
func (p *rewriter) ourID() error { return p.ours(p.w.Identity("")) }

func (p *rewriter) ourModel() error { return p.ours(p.t.Model) }

func (p *rewriter) ours(s string) error {
	v, err := p.dec.ReadValue()
	if err != nil || v.Kind() == 'n' {
		return err
	}
	return p.replaceString(len(v), s)
}

// keep is the rule set that keeps every member it names.
func keep(names ...string) map[string]rule {
	m := make(map[string]rule, len(names))
	for _, name := range names {
		m[name] = (*rewriter).skip
	}
	return m
}
