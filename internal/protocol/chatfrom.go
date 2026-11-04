package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: events are read token by token, in place
	"fmt"
	"strconv"

	"axiom/internal/protocol/internal/sse"
)

// converted is what the readers that convert an upstream's stream for a Chat client share (README.md「跨格式的流」):
// the upstream format's own reader, which reads each event for the meter, the content bound and the upstream's
// errors as it does for a client of that format; a second walk over the same data for what the chunk says; and the
// writer of our chunks.
type converted struct {
	chatWriter
	inner upstream
	walk  reader
	src   bytes.Buffer
	data  []byte // the event being converted
	draft chatDelta
	out   []byte // the chunk the event becomes, charged and reused
	head  []byte // every chunk's start, up to its delta's members
	// started: the role chunk is out; finished: the finish_reason is held, after which no content may come.
	hidesReasoning    bool
	started, finished bool
	tools, details    int64 // the tool calls and reasoning details started
}

// chatDelta is the delta a chunk is built from: content, refusal and reasoning as JSON strings, each the
// concatenation of its pieces, and the entries of the reasoning details and tool calls, comma-separated.
type chatDelta struct {
	role                                                   bool
	content, refusal, reasoning, details, tools, citations []byte
}

// done is the data of the event that ends a Chat stream.
var done = []byte(sse.Done)

func (c *converted) init(inner upstream, o RelayOptions, charge func(int) error) {
	c.chatWriter = newChatWriter(o, strconv.FormatInt(o.Created, 10))
	c.inner, c.walk.charge = inner, charge
	c.head = c.chunk(`[{"index":0,"delta":{`)
}

// begin reads an event with the upstream format's own reader, then starts the second walk over it.
func (c *converted) begin(data []byte, m *meter) (event, error) {
	e, err := c.inner.event(data, m)
	if err != nil {
		return event{}, err
	}
	c.data, c.src = data, *bytes.NewBuffer(data)
	if c.walk.dec == nil {
		c.walk.dec = jsontext.NewDecoder(&c.src)
	} else {
		c.walk.dec.Reset(&c.src)
	}
	c.draft = chatDelta{
		content: c.draft.content[:0], refusal: c.draft.refusal[:0], reasoning: c.draft.reasoning[:0],
		details: c.draft.details[:0], tools: c.draft.tools[:0],
	}
	return e, nil
}

// value reads a value as written: a slice of the event's data, which lasts as long as the event.
func (c *converted) value() ([]byte, error) {
	v, err := c.walk.dec.ReadValue()
	if err != nil {
		return nil, err
	}
	to := int(c.walk.dec.InputOffset())
	return c.data[to-len(v) : to], nil
}

// text reads a string as written, as value does, or nil for null.
func (c *converted) text() ([]byte, error) {
	if ok, err := c.walk.present('"', "a string"); !ok {
		return nil, err
	}
	return c.value()
}

// addCitation adds one Chat url_citation annotation entry. The gateway uses it when an upstream reports its own web
// search's citations, which a Chat client can read as annotations (docs/domains/gateway.md「联网搜索」).
func (c *converted) addCitation(entry []byte) error {
	return c.addEntry(&c.draft.citations, entry)
}

// addText adds piece, a JSON string, to the string in dst: the two concatenated are one JSON string, as each escape
// lies whole within a piece.
func (c *converted) addText(dst *[]byte, piece []byte) error {
	if piece == nil {
		return nil
	}
	b, err := sse.Grow(*dst, len(piece), c.walk.charge)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		*dst = append(b, piece...)
	} else {
		*dst = append(b[:len(b)-1], piece[1:]...)
	}
	return nil
}

// addEntry adds an entry, the concatenation of parts, to the comma-separated entries in dst.
func (c *converted) addEntry(dst *[]byte, parts ...[]byte) error {
	n := 1
	for _, p := range parts {
		n += len(p)
	}
	b, err := sse.Grow(*dst, n, c.walk.charge)
	if err != nil {
		return err
	}
	if len(b) > 0 {
		b = append(b, ',')
	}
	for _, p := range parts {
		b = append(b, p...)
	}
	*dst = b
	return nil
}

// render writes the chunk the delta makes into out, or nothing for an empty delta, and reports whether it carries
// content.
func (c *converted) render() (data []byte, content bool, err error) {
	d := &c.draft
	members := []struct {
		name string
		v    []byte
	}{{"content", d.content}, {"refusal", d.refusal}, {"reasoning", d.reasoning}, {"reasoning_content", d.reasoning}}
	n := len(c.head) + len(d.content) + len(d.refusal) + 2*len(d.reasoning) + len(d.details) + len(d.tools) +
		len(d.citations) + 192
	content = n > len(c.head)+192
	if !content && !d.role {
		return nil, false, nil
	}
	if c.out, err = sse.Grow(c.out[:0], n, c.walk.charge); err != nil {
		return nil, false, err
	}
	out := append(c.out, c.head...)
	sep := ""
	if d.role {
		out, sep = append(out, `"role":"assistant"`...), ","
		if len(d.content) == 0 {
			out = append(out, `,"content":""`...)
		}
	}
	for _, m := range members {
		if len(m.v) > 0 {
			out, sep = append(append(out, sep+`"`+m.name+`":`...), m.v...), ","
		}
	}
	if len(d.details) > 0 {
		out, sep = append(append(append(out, sep+`"reasoning_details":[`...), d.details...), ']'), ","
	}
	if len(d.citations) > 0 {
		out, sep = append(append(append(out, sep+`"annotations":[`...), d.citations...), ']'), ","
	}
	if len(d.tools) > 0 {
		out = append(append(append(out, sep+`"tool_calls":[`...), d.tools...), ']')
	}
	c.out = append(out, `},"finish_reason":null}]}`...)
	return c.out, content, nil
}

// hold makes the chunk with the finish_reason reason, a JSON string, held until the end: Chat's clients take it for
// the choice's end.
func (c *converted) hold(reason string) []byte {
	c.finished, c.first = true, reason
	return append(c.chunk(`[{"index":0,"delta":{},"finish_reason":`+reason+`}]`), '}')
}

// settle is the event the relay gets: the upstream format's own reader's view e of it, with what the conversion
// made of it. A role of heartbeat or end is the event's own; otherwise the delta decides, and finish, when set, is
// the chunk to hold.
func (c *converted) settle(e event, r role, finish []byte) (event, error) {
	out := event{role: r, usageAt: -1, content: e.content, shape: e.shape, searches: e.searches, toolCalls: e.toolCalls, toolChecks: e.toolChecks, toolErrors: e.toolErrors, usage: e.usage, final: e.final}
	if r == heartbeat { // the client's own keep-alive, which the relay writes
		return out, nil
	}
	if r == end {
		out.data, out.withheld = done, finish
		return out, nil
	}
	data, isContent, err := c.render()
	switch {
	case err != nil:
		return event{}, err
	case isContent && c.finished && finish == nil:
		return event{}, fmt.Errorf("%w: content after the finish reason", ErrUpstream)
	case isContent:
		out.role, out.data, out.withheld = content, data, finish
	case data != nil:
		out.role, out.data, out.withheld = passive, data, finish
	case finish != nil:
		out.role, out.data = withheld, finish
	default:
		out.role = skipped
	}
	return out, nil
}

func (c *converted) dropBuffers() {
	c.inner.dropBuffers()
	c.walk, c.src, c.data, c.draft, c.out = reader{}, bytes.Buffer{}, nil, chatDelta{}, nil
}

// detailIndex is a reasoning detail's index member: the client merges the entries that share one.
func detailIndex(n int64) []byte { return strconv.AppendInt([]byte(`,"index":`), n, 10) }
