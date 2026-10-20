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
	// Created is when the call started, in Unix seconds: the created of every chunk a Chat client gets from an
	// upstream of another format, which has none of its own to give.
	Created int64
	// Memory is the node's budget for what streams hold beyond their base allowance. It must not be nil.
	Memory *Memory
	// Aggregate is set for a client that did not ask for a stream: nothing is written until the end, Next gives no
	// Out, and Finish and Fail return the reply's body, whose HTTP status Status gives (README.md「非流式聚合」).
	Aggregate bool
}

// Final is what the ending reports: the usage and cost settled and whether that usage was estimated, or, when the
// settlement could not be written, Pending and no cost (docs/domains/gateway.md「用量与费用」).
type Final struct {
	Usage              Usage
	Cost               platform.Exact
	Currency           platform.Currency
	Estimated, Pending bool
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

// meterMemory is what the meter may hold, at its bounds (meter.go), charged when the relay starts: the meter lives
// until the settlement, past every buffer. TestRelayMemoryHeld measures it.
const meterMemory = 16 << 10

// Memory is the node's memory budget for streams. It is safe for concurrent use.
type Memory struct{ left atomic.Int64 }

// NewMemory returns a budget of size bytes.
func NewMemory(size int64) *Memory {
	m := &Memory{}
	m.left.Store(size)
	return m
}

// Charge draws n bytes from the budget and reports whether it had them; Refund gives them back. The gateway charges
// the request body it holds, the relay what its buffers hold past their base allowance.
func (m *Memory) Charge(n int64) bool {
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

// Refund returns n bytes Charge drew.
func (m *Memory) Refund(n int64) { m.left.Add(n) }

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
	up        upstream
	format    *relayFormat
	client    Format
	o         RelayOptions
	state     state
	committed bool
	err       error
	// out is what the current step writes, and before the commit point every event held until then. withheld are the
	// events held until the normal end, written; ending is the held ending event's data, ready for the client but for
	// its cost, which goes at usageAt; trailing are the events without content after it, written, which follow it;
	// stop is the data of the upstream's last event, nil when the ending ended the stream. edited is room for an event
	// edited in place.
	out, withheld, ending, trailing, stop, edited []byte
	usageAt                                       int
	endingName, stopName                          string
	meter                                         meter
	progress                                      Progress
	// pending is the upstream's error event, held for the frame that may end the stream with the attempt's usage
	// (relayFormat.holdsFailure); it is the failure the stream ends with otherwise.
	pending error
	// held is the capacity of the buffers the relay and its reader hold; charged is the part drawn on o.Memory.
	held, charged int64
	// For a reply that is aggregated: its aggregator, the data of an event of several lines, joined, and the HTTP
	// status of the body returned.
	agg    aggregator
	lines  []byte
	status int
}

// relayFormat is what the relay needs to know of a format.
type relayFormat struct {
	// newUpstream returns the format's reader, which charges what it holds with charge.
	newUpstream func(u UsageContext, o RelayOptions, charge func(int) error) upstream
	// limit bounds one event: Messages and Chat send deltas and control events; Responses' snapshot events repeat
	// the request and the whole output, and get streamcheck's ceiling (README.md「检查流结构」).
	limit int
	// rendersUsage is set for a format whose usage event the relay writes itself, from the settlement: Chat's, which
	// an upstream not asked for usage leaves out, and whose shape follows OpenRouter's (docs/domains/gateway.md「用量与
	// 费用」).
	rendersUsage bool
	// holdsFailure is set for a format whose error event the upstream may follow with the frame that ends the stream
	// with the attempt's usage, as OpenAI's bare error event is sometimes followed by response.failed: the relay holds
	// the error until the next frame, which is bounded by the caller's idle and total deadlines, and reports it when
	// the stream ends instead (docs/domains/billing.md「用量来源与暂估」).
	holdsFailure bool
}

var relayFormats = [...]relayFormat{
	Messages:  {newMessagesStream, 1 << 20, false, false},
	Responses: {newResponsesStream, 32 << 20, false, true},
	Chat:      {newChatStream, 1 << 20, true, false},
}

// chatConversions are the formats a Chat client's stream is converted from (README.md「跨格式的流」): the upstream's
// events, by its own bound, become chunks whose usage chunk the relay writes.
var chatConversions = [...]relayFormat{
	Messages:  {newMessagesToChat, relayFormats[Messages].limit, true, false},
	Responses: {newResponsesToChat, relayFormats[Responses].limit, true, false},
}

// NewRelay returns a Relay that reads the upstream stream body. Only a client of the upstream's format is relayed
// yet; for others the first step fails.
func NewRelay(client, upstream Format, body io.Reader, u UsageContext, o RelayOptions) *Relay {
	r := &Relay{o: o, client: client, held: meterMemory} // within the base allowance, so nothing is drawn yet
	switch {
	case client == upstream && int(upstream) < len(relayFormats):
		r.format = &relayFormats[upstream]
	case client == Chat && int(upstream) < len(chatConversions) && chatConversions[upstream].newUpstream != nil:
		r.format = &chatConversions[upstream]
	default:
		r.state, r.err = failed, fmt.Errorf("protocol: relaying format %d to format %d is not supported", upstream, client)
		return r
	}
	r.up = r.format.newUpstream(u, o, r.charge)
	if o.Aggregate {
		r.agg = newAggregator(client, r.charge)
	}
	r.meter.tier = u.Tier
	r.events = sse.NewReader(body, r.format.limit)
	r.events.ChargeGrowth(r.charge)
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
	if err == nil && r.agg != nil {
		step, err = r.aggregate(step)
	}
	if err != nil {
		return Step{}, r.fail(err)
	}
	return step, nil
}

func (r *Relay) event(data []byte) (Step, error) {
	e, err := r.up.event(data, &r.meter)
	if err != nil {
		switch {
		case r.pending != nil:
			// the frame after a held error event: this one says how the stream failed, and its usage, if it carried one,
			// was taken above
			r.pending = nil
		case r.format.holdsFailure && errorEventOf(err):
			// an error event that a later frame may end the stream for, with the attempt's usage: hold it until that
			// frame, or until the stream ends (docs/domains/billing.md「用量来源与暂估」)
			r.pending = err
			return Step{Kind: StepBuffered}, nil
		}
		return Step{}, err
	}
	r.observe(e)
	if r.pending != nil {
		// the error event was the stream's failure after all: this frame says nothing the client may read, and its usage
		// was taken above
		err, r.pending = r.pending, nil
		return Step{}, err
	}
	if data, err = r.edit(&e); err != nil {
		return Step{}, err
	}
	step := Step{Kind: StepBuffered} // what a skipped event takes
	if e.role != skipped {
		step, err = r.route(e, data)
	}
	if err == nil && e.withheld != nil {
		_, err = r.withhold("", e.withheld)
	}
	return step, err
}

// route writes or holds an event by its role.
func (r *Relay) route(e event, data []byte) (Step, error) {
	switch e.role {
	case heartbeat:
		return r.heartbeat(e.name, data)
	case ending:
		step, err := r.hold(e.name, data, e.usageAt)
		if err != nil || !e.ends {
			return step, err
		}
		return r.end(e.name, nil)
	case end:
		return r.end(e.name, data)
	case passive, content, withheld, skipped:
	}
	switch {
	case len(r.ending) > 0:
		return r.trail(e, data)
	case e.role == withheld:
		return r.withhold(e.name, data)
	case e.role == content && !r.committed:
		r.committed = true
		return r.write(StepCommit, e.name, data)
	}
	return r.write(StepData, e.name, data)
}

// edit returns e's data with its edits applied, in a buffer the relay reuses.
func (r *Relay) edit(e *event) ([]byte, error) {
	if len(e.edits) == 0 {
		return e.data, nil
	}
	var err error
	if r.edited, err = r.grow(r.edited[:0], editedSize(e)); err != nil {
		return nil, err
	}
	r.edited = edited(r.edited, e.data, e)
	return r.edited, nil
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

// hold keeps an ending event for Finish. An earlier one, which a stream may have, is withheld as it is, without a
// cost, and the events that followed it after it.
func (r *Relay) hold(name string, data []byte, usageAt int) (Step, error) {
	if r.agg != nil && len(data) > aggregateLimit { // Responses' ending is the reply: the cap is on it too
		return Step{}, fmt.Errorf("%w: the ending of %d bytes passes the %d a reply may hold", ErrMemory, len(data), aggregateLimit)
	}
	var err error
	if len(r.ending) > 0 {
		if _, err = r.withhold(r.endingName, r.ending); err != nil {
			return Step{}, err
		}
		if r.withheld, err = r.grow(r.withheld, len(r.trailing)); err != nil {
			return Step{}, err
		}
		r.withheld, r.trailing = append(r.withheld, r.trailing...), r.trailing[:0]
	}
	r.endingName, r.usageAt = name, usageAt
	r.ending, err = r.keep(r.ending, data)
	return Step{Kind: StepBuffered}, err
}

// trail takes an event after the held ending: one without content, such as one of a type we do not know, follows the
// ending; content there is the upstream's failure, since the usage in the ending would not count it.
func (r *Relay) trail(e event, data []byte) (Step, error) {
	if e.role != passive {
		return Step{}, fmt.Errorf("%w: %s after the ending", ErrUpstream, e.name)
	}
	var err error
	r.trailing, err = r.appendEvent(r.trailing, e.name, data)
	return Step{Kind: StepBuffered}, err
}

// withhold keeps an event until the normal end.
func (r *Relay) withhold(name string, data []byte) (Step, error) {
	var err error
	r.withheld, err = r.appendEvent(r.withheld, name, data)
	if err == nil && r.agg != nil {
		err = r.agg.hold(data)
	}
	return Step{Kind: StepBuffered}, err
}

// appendEvent appends an event to buf, charging its room first.
func (r *Relay) appendEvent(buf []byte, name string, data []byte) ([]byte, error) {
	buf, err := r.grow(buf, sse.EventSize(name, data))
	if err != nil {
		return buf, err
	}
	return sse.AppendEvent(buf, name, data), nil
}

// end takes the upstream's normal end, whose last event is stop, or nil when the ending ended the stream. A format
// whose usage is not optional must have sent its ending by then. The usage is final.
func (r *Relay) end(name string, stop []byte) (Step, error) {
	if len(r.ending) == 0 && !r.format.rendersUsage {
		return Step{}, fmt.Errorf("%w: the stream ended without its usage event", ErrUpstream)
	}
	if stop != nil {
		var err error
		if r.stop, err = r.keep(r.stop, stop); err != nil {
			return Step{}, err
		}
	}
	r.state, r.stopName, r.meter.final = upstreamDone, name, true
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

// heartbeat passes the upstream's keep-alive on once the stream is committed: as it is, or for a converted stream,
// whose data is nil, as the client's own.
func (r *Relay) heartbeat(name string, data []byte) (Step, error) {
	switch {
	case !r.committed:
		return Step{Kind: StepHeartbeat}, nil
	case data == nil:
		return r.writeRaw(StepHeartbeat, KeepAlive(r.client))
	}
	return r.write(StepHeartbeat, name, data)
}

// writeRaw appends bytes already framed as events to out and returns the step that writes out.
func (r *Relay) writeRaw(kind StepKind, b []byte) (Step, error) {
	var err error
	if r.out, err = r.grow(r.out, len(b)); err != nil {
		return Step{}, err
	}
	r.out = append(r.out, b...)
	return Step{Out: r.out, Kind: kind}, nil
}

func (r *Relay) grow(b []byte, n int) ([]byte, error) { return sse.Grow(b, n, r.charge) }

// charge accounts for n more bytes held, drawing what passes the base allowance from the node's budget.
func (r *Relay) charge(n int) error {
	over := r.held + int64(n) - baseMemory - r.charged
	if over > 0 {
		if !r.o.Memory.Charge(over) {
			return ErrMemory
		}
		r.charged += over
	}
	r.held += int64(n)
	return nil
}

// Release gives back what the relay drew on the node's budget, at its end: its buffers go with it. Finish and Fail
// call it, but for a relay that aggregates, whose body is those buffers: the gateway holds the budget until the body
// is written and calls Release then. Further calls do nothing.
func (r *Relay) Release() {
	if r.charged > 0 {
		r.o.Memory.left.Add(r.charged)
		r.charged = 0
	}
}

// fail ends the relay's reading. A stream that breaks off or breaks its format is the upstream's failure; a failed
// read, such as the one the attempt's cancellation causes, is returned as it is. The gateway settles before it asks
// for the failure ending, so the relay lives on meanwhile: it lets go of every buffer but out, which still holds the
// events before the commit point, and gives back only what it no longer holds.
func (r *Relay) fail(err error) error {
	if r.pending != nil {
		// the stream ended after the upstream's error event without the frame that would have ended it: the error event
		// is the failure, whatever ended the read, and the usage observed so far only bounds the attempt from below
		err, r.pending = r.pending, nil
	}
	switch {
	case errors.Is(err, io.EOF):
		err = fmt.Errorf("%w: the stream ended before its end event", ErrUpstream)
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, sse.ErrInvalid), errors.Is(err, sse.ErrTooLarge):
		err = fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	r.state, r.err = failed, err
	r.events, r.withheld, r.ending, r.trailing, r.stop, r.edited, r.lines = nil, nil, nil, nil, nil, nil, nil
	r.up.dropBuffers()
	r.held = int64(cap(r.out)) + meterMemory
	if r.agg != nil {
		r.agg.failed()
		r.held += r.agg.taken()
	}
	keep := max(0, r.held-baseMemory)
	r.o.Memory.left.Add(r.charged - keep)
	r.charged = keep
	return err
}

// Progress returns what the relay has seen so far. It does not allocate.
func (r *Relay) Progress() Progress { return r.progress }

// Usage returns the attempt's usage by segment, with only the final counts reported.
func (r *Relay) Usage() Usage { return r.meter.result() }

// Finish returns the ending after the upstream's normal end: the withheld events, the held ending event with the
// cost of f, or for a format whose usage event the relay writes, one with the usage and cost of f, the events without
// content that followed it, and the upstream's last event. It returns nil unless Next returned StepUpstreamDone and no
// ending was written. Like Fail, it builds the ending from bytes already charged and then gives them all back, so it
// charges nothing; a relay that aggregates keeps them charged until Release, for the body it returned.
func (r *Relay) Finish(f Final) []byte {
	if r.state != upstreamDone {
		return nil
	}
	usage, top := cost(&f), ""
	if f.Pending {
		top = `,"axiom":{"billing_pending":true}`
	}
	out := append(r.endOut(), r.withheld...)
	if r.format.rendersUsage {
		out = r.up.usageEvent(out, reply(f.Usage), usage, top)
	} else {
		out = sse.AppendEvent(out, r.endingName, r.costed(usage, top))
	}
	out = append(out, r.trailing...)
	if r.stop != nil {
		out = sse.AppendEvent(out, r.stopName, r.stop)
	}
	if r.agg != nil {
		return r.finishAggregate(out, f)
	}
	r.state = ended
	r.Release()
	return out
}

// costed is the held ending with the members usage added to its usage object and top to the event: an ending
// without a usage object gets one.
func (r *Relay) costed(usage, top string) []byte {
	at := r.usageAt
	if at < 0 { // no usage object: the ending gets one
		at, usage = bytes.LastIndexByte(r.ending, '}'), `,"usage":{`+strings.TrimPrefix(usage, ",")+"}"
	} else if bytes.HasSuffix(bytes.TrimRight(r.ending[:at], " \t\r\n"), []byte("{")) {
		usage = strings.TrimPrefix(usage, ",") // the usage object is empty
	}
	last := bytes.LastIndexByte(r.ending, '}') // the data is a JSON object, which ends with its brace
	return slices.Concat(r.ending[:at], []byte(usage), r.ending[at:last], []byte(top), r.ending[last:])
}

// reply is the reply's segment of u.
func reply(u Usage) catalog.Usage {
	if len(u.Segments) == 0 {
		return catalog.Usage{}
	}
	return u.Segments[0].Usage
}

// Fail returns an error ending with the usage and cost of f, or, when f is nil because the settlement was not
// written, with the usage reported so far and no cost. It returns nil once an ending was written.
// A relay that aggregates keeps its charge past Fail too (Release).
func (r *Relay) Fail(e Error, f *Final) []byte {
	if r.state == ended {
		return nil
	}
	u := r.Usage()
	if f != nil {
		u = f.Usage
	}
	var out []byte
	switch {
	case r.agg != nil:
		out = r.failAggregate(e, f, u)
	case r.up != nil: // nil for a pair of formats not relayed
		out = r.up.failure(r.endOut(), e, reply(u), cost(f))
	}
	r.state = ended
	if r.agg == nil {
		r.Release()
	}
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
