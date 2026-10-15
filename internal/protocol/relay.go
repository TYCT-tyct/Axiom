package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol/internal/sse"
)

// StepKind is what one step of a Relay asks of its caller (README.md「转发」).
type StepKind uint8

// The step kinds.
const (
	// StepProgress is a read that finished no event: nothing to write, and Progress counts the bytes that arrived.
	StepProgress StepKind = iota
	// StepBuffered is an event held back: before the commit point every event is, after it only the ending.
	StepBuffered
	// StepCommit is the commit point, the first content: Out is the events held until now and this one.
	StepCommit
	// StepData is an event to write.
	StepData
	// StepHeartbeat is the upstream's keep-alive, which resets no idle timer: Out is empty before the commit point.
	StepHeartbeat
	// StepUpstreamDone is the upstream's normal end: the caller settles, then writes what Finish returns.
	StepUpstreamDone
)

// Step is one step of a Relay. Out is valid until the next call to the Relay.
type Step struct {
	Out  []byte
	Kind StepKind
}

// Segment is the usage of one sampling the upstream bills on its own, such as a compaction, with the items the
// upstream reported exactly. Billing estimates the others.
type Segment struct {
	Usage    catalog.Usage
	Reported catalog.ItemSet
}

// Usage is an attempt's usage: the reply's segment first, then one per compaction. Invalid says why the upstream's
// report cannot be used, such as a count that went down; then no item is Reported, and billing estimates them all
// (docs/domains/billing.md「用量来源与暂估」).
type Usage struct {
	Segments []Segment
	Invalid  error
}

// Progress is what the relay has seen so far, for billing's meter after every step. ContentBytes are the text,
// reasoning and tool input of the events read, unquoted; PartialBytes are the raw bytes read but not yet returned as
// an event. Reported has the reply's counts reported so far, and Final those that are final: the input side once
// reported, the rest from the format's last usage event on; until then an output count only bounds the output from
// below (README.md「转发（草图）」).
type Progress struct {
	ContentBytes, PartialBytes, Searches int64
	Reported                             Segment
	Final                                catalog.ItemSet
}

// UsageContext is what the request sent upstream tells about its usage: whether it set a 1-hour cache breakpoint,
// and the service tier it asked for, used when the reply names none (catalog README.md「计费项与用量」).
type UsageContext struct {
	CacheTTL1h bool
	Tier       catalog.ServiceTier
}

// RelayOptions are the gateway's values for one attempt.
type RelayOptions struct {
	// ID is the call ID, written in place of the upstream's ID for the reply; Model is the model name the client
	// asked for (docs/domains/gateway.md「转发规则」).
	ID, Model string
	// Memory is the node's budget for what streams hold beyond their base allowance. It must not be nil.
	Memory *Memory
}

// Final is what the ending reports: the usage and cost settled and whether that usage was estimated, or, when the
// settlement could not be written, Pending and no cost (docs/domains/gateway.md「用量与费用」).
type Final struct {
	Usage              Usage
	Cost               platform.Exact
	Currency           platform.Currency
	Estimated, Pending bool
}

// ErrorType is the unified error type of an error ending (docs/domains/gateway.md「错误」).
type ErrorType uint8

// The error types an ending reports.
const (
	CreditsInsufficient ErrorType = iota
	BillingUnavailable
	UpstreamFailed
	Timeout
)

var errorTypeNames = [...]string{"credits_insufficient", "billing_unavailable", "upstream_error", "timeout"}

// Error is an error to end a stream with.
type Error struct {
	Type    ErrorType
	Message string
}

var (
	// ErrUpstream reports an upstream stream that broke off, broke its format or ended with an error event.
	ErrUpstream = errors.New("protocol: the upstream stream failed")
	// ErrMemory reports a stream that needed more memory than the node's budget has left.
	ErrMemory = errors.New("protocol: the node's memory budget for streams is spent")
	// ErrTerminated reports a step asked for after the upstream's normal end or after the ending.
	ErrTerminated = errors.New("protocol: the relay has ended")
)

// baseMemory is what each stream may hold without drawing on the node's budget; the gateway counts it per stream
// (internal/gateway/README.md「并发、背压与共享状态」).
const baseMemory = 64 << 10

// Memory is the node's memory budget for streams. It is safe for concurrent use.
type Memory struct{ left atomic.Int64 }

// NewMemory returns a budget of size bytes.
func NewMemory(size int64) *Memory {
	m := &Memory{}
	m.left.Store(size)
	return m
}

func (m *Memory) take(n int64) bool {
	for {
		left := m.left.Load()
		if left < n {
			return false
		}
		if m.left.CompareAndSwap(left, left-n) {
			return true
		}
	}
}

// state is where a relay is in its stream.
type state uint8

const (
	reading      state = iota
	upstreamDone       // the upstream ended normally; Finish or Fail writes the ending
	failed             // a step failed; Fail writes the ending
	ended              // the ending is written
)

// Relay turns one upstream stream into the client's stream. Only one goroutine calls it (README.md「转发」).
type Relay struct {
	events    *sse.Reader
	m         messagesStream
	o         RelayOptions
	state     state
	committed bool
	err       error
	// out is what the current step writes, and before the commit point every event held until then. ending is the
	// held ending event's data, ready for the client but for its cost, which goes at usageAt; stop is the data of the
	// upstream's last event.
	out, ending, stop    []byte
	usageAt              int
	endingName, stopName string
	meter                meter
	progress             Progress
	// held is the capacity of the buffers the relay and its reader hold; charged is the part drawn on o.Memory.
	held, charged int64
}

// relayEventLimit bounds one event: Messages sends deltas and control events, whose class limit this is (README.md
// 「转发」).
const relayEventLimit = 1 << 20

// NewRelay returns a Relay that reads the upstream stream body. Only same-format Messages streams are relayed yet;
// for others the first step fails.
func NewRelay(client, upstream Format, body io.Reader, u UsageContext, o RelayOptions) *Relay {
	r := &Relay{events: sse.NewReader(body, relayEventLimit), o: o}
	r.events.ChargeGrowth(r.charge)
	r.m.ctx = u
	r.m.rw.w.Identity = func(string) string { return o.ID }
	r.m.rw.t.Model = o.Model
	if client != Messages || upstream != Messages {
		r.state, r.err = failed, fmt.Errorf("protocol: relaying format %d to format %d is not supported yet", upstream, client)
	}
	return r
}

// Next reads until the next event or the end of one read of at most 16 KiB, whichever comes first. After an error,
// it returns the same error.
func (r *Relay) Next() (Step, error) {
	switch r.state {
	case reading:
	case failed:
		return Step{}, r.err
	case upstreamDone, ended:
		return Step{}, ErrTerminated
	}
	if r.committed {
		r.out = r.out[:0] // the caller wrote it
	}
	data, err := r.events.Poll()
	r.progress.PartialBytes = int64(r.events.Pending())
	if err == nil && data == nil {
		return Step{Kind: StepProgress}, nil
	}
	var step Step
	if err == nil {
		step, err = r.event(data)
	}
	if err != nil {
		return Step{}, r.fail(err)
	}
	return step, nil
}

func (r *Relay) event(data []byte) (Step, error) {
	e, err := r.m.event(data, &r.meter)
	if err != nil {
		return Step{}, err
	}
	r.observe(e)
	switch e.role {
	case heartbeat:
		if !r.committed {
			return Step{Kind: StepHeartbeat}, nil
		}
		return r.write(StepHeartbeat, e.name, e.data)
	case ending:
		return r.hold(e)
	case end:
		return r.end(e)
	case passive, content:
	}
	switch {
	case len(r.ending) > 0:
		return Step{}, fmt.Errorf("%w: %s after the ending", ErrUpstream, e.name)
	case e.role == content && !r.committed:
		r.committed = true
		return r.write(StepCommit, e.name, e.data)
	}
	return r.write(StepData, e.name, e.data)
}

// observe adds what an event reports to the progress.
func (r *Relay) observe(e event) {
	r.progress.ContentBytes += e.content
	r.progress.Searches += e.searches
	if e.usage || e.final {
		r.meter.final = r.meter.final || e.final
		r.progress.Reported, r.progress.Final = r.meter.progress()
	}
}

// hold keeps an ending event for Finish. An earlier one, which a stream may have, is written as it is, without a
// cost.
func (r *Relay) hold(e event) (step Step, err error) {
	step.Kind = StepBuffered
	if len(r.ending) > 0 {
		if step, err = r.write(StepData, r.endingName, r.ending); err != nil {
			return Step{}, err
		}
	}
	r.endingName, r.usageAt = e.name, e.usageAt
	r.ending, err = r.keep(r.ending, e.data)
	return step, err
}

// end takes the upstream's normal end, which must follow an ending: the usage is final, and it ends no stream
// before then.
func (r *Relay) end(e event) (Step, error) {
	if len(r.ending) == 0 {
		return Step{}, fmt.Errorf("%w: the stream ended without its usage event", ErrUpstream)
	}
	var err error
	if r.stop, err = r.keep(r.stop, e.data); err != nil {
		return Step{}, err
	}
	r.state, r.stopName, r.meter.final = upstreamDone, e.name, true
	r.progress.Reported, r.progress.Final = r.meter.progress()
	return Step{Kind: StepUpstreamDone}, nil
}

// keep copies data into buf, which it reuses.
func (r *Relay) keep(buf, data []byte) ([]byte, error) {
	buf, err := r.grow(buf[:0], len(data))
	if err != nil {
		return buf, err
	}
	return append(buf, data...), nil
}

// write appends an event to out and returns the step that writes out; before the commit point the event is held in
// out instead, and the step writes nothing.
func (r *Relay) write(kind StepKind, name string, data []byte) (Step, error) {
	var err error
	if r.out, err = r.grow(r.out, sse.EventSize(name, data)); err != nil {
		return Step{}, err
	}
	r.out = sse.AppendEvent(r.out, name, data)
	if !r.committed {
		return Step{Kind: StepBuffered}, nil
	}
	return Step{Out: r.out, Kind: kind}, nil
}

func (r *Relay) grow(b []byte, n int) ([]byte, error) { return sse.Grow(b, n, r.charge) }

// charge accounts for n more bytes held, drawing what passes the base allowance from the node's budget.
func (r *Relay) charge(n int) error {
	over := r.held + int64(n) - baseMemory - r.charged
	if over > 0 {
		if !r.o.Memory.take(over) {
			return ErrMemory
		}
		r.charged += over
	}
	r.held += int64(n)
	return nil
}

// release gives back what the relay drew on the node's budget, at its end: its buffers go with it.
func (r *Relay) release() {
	r.o.Memory.left.Add(r.charged)
	r.charged = 0
}

// fail ends the relay's reading. A stream that breaks off or breaks its format is the upstream's failure; a failed
// read, such as the one the attempt's cancellation causes, is returned as it is.
func (r *Relay) fail(err error) error {
	switch {
	case errors.Is(err, io.EOF):
		err = fmt.Errorf("%w: the stream ended before its end event", ErrUpstream)
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, sse.ErrInvalid), errors.Is(err, sse.ErrTooLarge):
		err = fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	r.state, r.err = failed, err
	r.release()
	return err
}

// Progress returns what the relay has seen so far. It does not allocate.
func (r *Relay) Progress() Progress { return r.progress }

// Usage returns the attempt's usage by segment, with only the final counts reported.
func (r *Relay) Usage() Usage { return r.meter.result() }

// Finish returns the ending after the upstream's normal end: the held ending event with the cost of f, and the
// upstream's last event. It returns nil unless Next returned StepUpstreamDone and no ending was written. Like Fail,
// it builds the ending from bytes already charged and then gives them all back, so it charges nothing.
func (r *Relay) Finish(f Final) []byte {
	if r.state != upstreamDone {
		return nil
	}
	usage, top := cost(&f), ""
	if f.Pending {
		top = `,"axiom":{"billing_pending":true}`
	}
	at := r.usageAt
	if at < 0 { // no usage object: the ending gets one
		at, usage = bytes.LastIndexByte(r.ending, '}'), `,"usage":{`+strings.TrimPrefix(usage, ",")+"}"
	} else if bytes.HasSuffix(bytes.TrimRight(r.ending[:at], " \t\r\n"), []byte("{")) {
		usage = strings.TrimPrefix(usage, ",") // the usage object is empty
	}
	last := bytes.LastIndexByte(r.ending, '}') // the data is a JSON object, which ends with its brace
	data := slices.Concat(r.ending[:at], []byte(usage), r.ending[at:last], []byte(top), r.ending[last:])
	out := sse.AppendEvent(sse.AppendEvent(r.endOut(), r.endingName, data), r.stopName, r.stop)
	r.state = ended
	r.release()
	return out
}

// Fail returns an error ending with the usage and cost of f, or, when f is nil because the settlement was not
// written, with the usage reported so far and no cost. It returns nil once an ending was written.
func (r *Relay) Fail(e Error, f *Final) []byte {
	if r.state == ended {
		return nil
	}
	u := r.Usage()
	if f != nil {
		u = f.Usage
	}
	var reply catalog.Usage
	if len(u.Segments) > 0 {
		reply = u.Segments[0].Usage
	}
	out := messagesError(r.endOut(), e, reply, cost(f))
	r.state = ended
	r.release()
	return out
}

// endOut is out ready for an ending: still holding the events before the commit point when there was none.
func (r *Relay) endOut() []byte {
	if r.committed {
		return r.out[:0]
	}
	return r.out
}

// cost is the members an ending's usage gets for f, each led by a comma: none without a settled cost.
func cost(f *Final) string {
	if f == nil || f.Pending {
		return ""
	}
	amount := strings.TrimRight(strings.TrimRight(f.Cost.Rat().FloatString(15), "0"), ".")
	s := `,"cost":` + amount + `,"cost_currency":"` + f.Currency.String() + `"`
	if f.Estimated {
		s += `,"cost_estimated":true`
	}
	return s
}

// KeepAlive returns the bytes that keep a client's stream of format f alive while the upstream sends nothing new:
// Messages' own ping event, and a comment for the others (docs/domains/gateway.md「保活」). They are made once and
// shared, so the caller must not change them.
func KeepAlive(f Format) []byte { return keepAlives[f] }

var keepAlives = [...][]byte{
	Messages:  []byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"),
	Responses: []byte(": keep-alive\n\n"),
	Chat:      []byte(": keep-alive\n\n"),
}
