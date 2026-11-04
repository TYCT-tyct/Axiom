package protocol

import (
	"errors"
	"fmt"
	"io"

	"axiom/internal/protocol/internal/sse"
)

var (
	// ErrUpstream reports an upstream stream that broke off, broke its format or ended with an error event.
	ErrUpstream = errors.New("protocol: the upstream stream failed")
	// ErrMemory reports a stream that needed more memory than the node's budget has left.
	ErrMemory = errors.New("protocol: the node's memory budget for streams is spent")
	// ErrTerminated reports a step asked for after the upstream's normal end or after the ending.
	ErrTerminated = errors.New("protocol: the relay has ended")
)

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
	// held is the capacity of the buffers the relay and its reader hold; charged is the part drawn on o.Memory. free
	// is what held may reach without drawing: baseMemory, and nativeConversionMemory for a native-to-native relay.
	held, charged, free int64
	// For a reply that is aggregated: its aggregator, the data of an event of several lines, joined, and the HTTP
	// status of the body returned.
	agg    aggregator
	lines  []byte
	status int
	legacy *rewriter
	srv    *serverCapture // the gateway server tool's calls, nil when none is captured
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
}

var relayFormats = [...]relayFormat{
	Messages:    {newMessagesStream, 1 << 20, false},
	Responses:   {newResponsesStream, 32 << 20, false},
	Chat:        {newChatStream, 1 << 20, true},
	Completions: {newCompletionsStream, 1 << 20, true},
}

// chatConversions are the formats a Chat client's stream is converted from (README.md「跨格式的流」): the upstream's
// events, by its own bound, become chunks whose usage chunk the relay writes.
var chatConversions = [...]relayFormat{
	Messages:  {newMessagesToChat, relayFormats[Messages].limit, true},
	Responses: {newResponsesToChat, relayFormats[Responses].limit, true},
}

var fromChatConversions = [...]relayFormat{
	Messages:  {newChatToMessages, relayFormats[Chat].limit, true},
	Responses: {newChatToResponses, relayFormats[Chat].limit, true},
}

// NewRelay reads the upstream stream in its own format or converts between any two of the three formats.
// Unsupported pairs fail on their first step.
func NewRelay(client, upstream Format, body io.Reader, u UsageContext, o RelayOptions) *Relay {
	o.legacyFunctions = client == Chat && u.LegacyFunctions
	r := &Relay{o: o, client: client, held: meterMemory, free: baseMemory} // within the allowance, so nothing is drawn yet
	var native bool
	r.format, native = formatFor(client, upstream)
	if r.format == nil {
		r.state, r.err = failed, fmt.Errorf("protocol: relaying format %d to format %d is not supported", upstream, client)
		return r
	}
	if native {
		r.free += nativeConversionMemory
	}
	r.initReply(u, o)
	if o.ServerTool {
		r.srv = newServerCapture(client)
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
	r.observe(e) // a reader that fails the event may still report what ended with it, such as the tool calls checked
	if err != nil {
		return Step{}, err
	}
	if data, err = r.edit(&e); err != nil {
		return Step{}, err
	}
	if data, err = r.delivered(e, data); err != nil {
		return Step{}, err
	}
	step := Step{Kind: StepBuffered} // what a skipped event takes
	if e.role != skipped {
		step, err = r.route(e, data)
	}
	if err == nil && e.withheld != nil {
		data, err = r.legacyReply(e.withheld)
		if err == nil {
			_, err = r.withhold("", data)
		}
	}
	return step, err
}

// delivered is an event of the reply as the client gets it, legacy completions' rewritten, and captured for the server
// tool's loop. A skipped event and a heartbeat carry no reply.
func (r *Relay) delivered(e event, data []byte) ([]byte, error) {
	if e.role == skipped || e.role == heartbeat {
		return data, nil
	}
	data, err := r.legacyReply(data)
	if err != nil || r.srv == nil {
		return data, err
	}
	return data, r.capture(data)
}

// route writes or holds an event by its role.
func (r *Relay) route(e event, data []byte) (Step, error) {
	if e.framed {
		return r.writeBatch(e.role, data)
	}
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

// writeBatch routes the complete client events produced from one upstream chunk.
func (r *Relay) writeBatch(role role, data []byte) (Step, error) {
	kind := StepData
	if role == content && !r.committed {
		r.committed, kind = true, StepCommit
	}
	step, err := r.writeRaw(kind, data)
	if !r.committed {
		step = Step{Kind: StepBuffered}
	}
	return step, err
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
	r.progress.Text.Merge(e.shape)
	r.progress.Searches += e.searches
	r.progress.CodeExecutions += e.codeExecutions
	r.progress.FileSearches += e.fileSearches
	r.progress.ToolCalls += e.toolCalls
	r.progress.ToolChecks += e.toolChecks
	r.progress.ToolErrors += e.toolErrors
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
	over := r.held + int64(n) - r.free - r.charged
	if over > 0 {
		if !r.o.Memory.Charge(over) {
			return ErrMemory
		}
		r.charged += over
	}
	r.held += int64(n)
	return nil
}

// uncharge accounts for n fewer bytes held, giving back to the node's budget what was drawn past the base allowance and
// is no longer held.
func (r *Relay) uncharge(n int) {
	r.held -= int64(n)
	if back := r.charged - max(r.held-r.free, 0); back > 0 {
		r.o.Memory.Refund(back)
		r.charged -= back
	}
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
	switch {
	case errors.Is(err, io.EOF):
		err = fmt.Errorf("%w: the stream ended before its end event", ErrUpstream)
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, sse.ErrInvalid), errors.Is(err, sse.ErrTooLarge):
		err = fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	r.state, r.err = failed, err
	r.events, r.withheld, r.ending, r.trailing, r.stop, r.edited, r.lines = nil, nil, nil, nil, nil, nil, nil
	r.up.dropBuffers()
	r.legacy = nil
	r.held = int64(cap(r.out)) + meterMemory
	if r.agg != nil {
		r.agg.failed()
		r.held += r.agg.taken()
	}
	keep := max(0, r.held-r.free)
	r.o.Memory.left.Add(r.charged - keep)
	r.charged = keep
	return err
}

// Progress returns what the relay has seen so far. It does not allocate.
func (r *Relay) Progress() Progress { return r.progress }

// Usage returns the attempt's usage by segment, with only the final counts reported.
func (r *Relay) Usage() Usage { return r.meter.result() }

// KeepAlive returns the bytes that keep a client's stream of format f alive while the upstream sends nothing new:
// Messages' own ping event, and a comment for the others (docs/domains/gateway.md「保活」). They are made once and
// shared, so the caller must not change them.
func KeepAlive(f Format) []byte { return keepAlives[f] }

var keepAlives = [...][]byte{
	Messages:    []byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"),
	Responses:   []byte(": keep-alive\n\n"),
	Chat:        []byte(": keep-alive\n\n"),
	Completions: []byte(": keep-alive\n\n"),
}
