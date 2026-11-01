package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"unsafe"
)

// serverCapture records the assistant turn and the calls of the gateway's web search tool as the client-format events
// carry them. It reads the events the relay is about to write, after conversion: one capture serves every pair of
// formats, and a call's arguments accumulate across chunks whatever shape the client's format gives them.
type serverCapture struct {
	format    Format
	calls     []ServerToolCall
	index     map[int64]int // the format's call index to the entry in calls
	blocks    []capturedBlock
	blockAt   map[int64]int
	text      []byte
	reasoning []byte
	details   []byte // Chat's reasoning_details entries as the chunks gave them, comma-separated, merged by turn
	args      int
	client    bool
	held      int // the footprint the relay has charged for this capture
	nDetails  int // the reasoning_details entries in details, which turn decodes one by one

	walker            // the token walk's decoders, kept between events (walk.go, servercapture_read.go)
	scr    [11][]byte // the members an event is read into before it is acted on
}

// ServerTurn is the assistant turn one loop hop produced, in the client's format, together with every call it made.
// The gateway appends the server calls' results after this turn, so text, thinking and signatures the model emitted
// stay in the conversation. Client is true when a call is one the client runs; a loop hop may not mix those with a
// server call.
type ServerTurn struct {
	Assistant []byte
	Calls     []ServerToolCall
	Client    bool
}

type capturedBlock struct {
	kind, text, thinking, signature, id, name string
	input                                     []byte
	raw                                       []byte
	client                                    bool
}

func newServerCapture(f Format) *serverCapture {
	return &serverCapture{format: f, index: make(map[int64]int), blockAt: make(map[int64]int)}
}

// maxServerCallArguments bounds one call's arguments. A search query is short; a model that writes more has produced
// something this gateway will not copy or run.
const maxServerCallArguments = 64 << 10

// append adds an argument fragment to one call.
func (c *serverCapture) append(at int, fragment []byte) error {
	if len(fragment) == 0 {
		return nil
	}
	c.args += len(fragment)
	c.calls[at].Arguments += string(fragment)
	return c.bound()
}

func (c *serverCapture) bound() error {
	if c.args > maxServerCallArguments {
		return fmt.Errorf("%w: a web search call's arguments pass %d bytes", ErrUpstream, maxServerCallArguments)
	}
	return nil
}

// size is what the capture holds, in bytes: the measure the relay charges its growth by, and bounds the turn with.
func (c *serverCapture) size() int {
	n := len(c.text) + len(c.reasoning) + len(c.details) + c.args
	for _, b := range c.blocks {
		n += len(b.kind) + len(b.text) + len(b.thinking) + len(b.signature) + len(b.id) + len(b.name) + len(b.input) + len(b.raw)
	}
	for _, call := range c.calls {
		n += len(call.ID) + len(call.Name)
	}
	return n
}

// mapEntry is what one entry of the capture's index maps holds, key, value and bucket share, rounded up.
const mapEntry = 48

// footprint is the memory the capture keeps: the capacity of its byte buffers, its blocks and calls with the strings
// they hold, and its index maps. The relay charges it as it grows.
func (c *serverCapture) footprint() int {
	n := cap(c.text) + cap(c.reasoning) + cap(c.details)
	n += cap(c.blocks)*int(unsafe.Sizeof(capturedBlock{})) + cap(c.calls)*int(unsafe.Sizeof(ServerToolCall{}))
	n += (len(c.index) + len(c.blockAt)) * mapEntry
	for _, b := range c.blocks {
		n += len(b.text) + len(b.thinking) + len(b.signature) + len(b.id) + len(b.name) + cap(b.input) + cap(b.raw)
	}
	for _, call := range c.calls {
		n += len(call.ID) + len(call.Name) + len(call.Arguments)
	}
	for i := range c.levels {
		l := &c.levels[i]
		n += cap(l.name) // buf holds the event's own bytes, which the relay counts
		if l.dec != nil {
			n += decoderMemory
		}
	}
	for _, b := range c.scr {
		n += cap(b)
	}
	return n
}

// growthBound is the most reading one event of n bytes can allocate (TestCaptureAllocatesWithinItsCharge measures it
// against hostile events). The walk allocates nothing per element it skips (servercapture_read.go), so what remains
// is what it keeps and how its buffers grow:
//   - what the event adds, its strings and raw values, each copied once: at most n;
//   - one growth of a buffer, slice or string it adds to, which copies what is there into a larger one: append grows
//     by up to twice what it holds, so 2×footprint covers it, and a string rewritten with its addition is no more;
//   - the decoders' state for the event's nesting, a few bytes a level, so under 3n with the n above inside 4n;
//   - one more block, call and two index entries.
func (c *serverCapture) growthBound(n int) int {
	return 2*c.footprint() + 4*n + int(unsafe.Sizeof(capturedBlock{})+unsafe.Sizeof(ServerToolCall{})) + 2*mapEntry
}

// turnSize is the most turn writes: every string escaped, six bytes a byte at worst (\u00XX, or an invalid byte's
// replacement), the raw values as they are, and the members and quotes around the message, each block and each call.
func (c *serverCapture) turnSize() int {
	return 6*c.size() + 128*(len(c.blocks)+len(c.calls)+1)
}

// turnBound is what building the turn allocates: one buffer of turnSize, which the appends never outgrow, and the copy
// of the calls (TestTurnAllocatesWithinItsCharge).
func (c *serverCapture) turnBound() int {
	return allocated(c.turnSize()) + allocated(len(c.calls)*int(unsafe.Sizeof(ServerToolCall{})))
}

// allocated is the most the runtime takes for an allocation of n bytes: a size class wastes at most an eighth of it,
// and an object past the largest class is rounded up to whole 8 KiB pages.
func allocated(n int) int {
	return n + n/8 + 8<<10
}

// turn builds the assistant turn the client format expects, so the next hop can replay it unchanged. Chat and Messages
// get one assistant message; Responses gets the array of output items. A block this capture did not understand keeps
// its raw start object rather than being dropped. Chat's reasoning_details go back as the chunks gave them, an entry a
// chunk, as an OpenRouter client hands them back: Prepare merges them by type and index (mergeDetails) for whatever
// format the next hop is converted to.
func (c *serverCapture) turn() ServerTurn {
	b := make([]byte, 0, c.turnSize())
	//nolint:exhaustive // only the three chat formats carry tools
	switch c.format {
	case Chat:
		b = c.chatTurn(b)
	case Messages:
		b = append(b, `{"role":"assistant","content":[`...)
		for i, blk := range c.blocks {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendBlock(b, &blk)
		}
		b = append(b, `]}`...)
	case Responses:
		b = c.responsesTurn(b)
	}
	return ServerTurn{Assistant: b, Calls: slices.Clone(c.calls), Client: c.client}
}

// chatTurn is the assistant message: text, reasoning and the server calls.
func (c *serverCapture) chatTurn(b []byte) []byte {
	b = append(b, `{"role":"assistant","content":`...)
	if len(c.text) == 0 {
		b = append(b, "null"...)
	} else {
		b = appendQuoted(b, c.text)
	}
	if len(c.reasoning) > 0 {
		b = appendQuoted(append(b, `,"reasoning_content":`...), c.reasoning)
	}
	if len(c.details) > 0 {
		b = append(append(append(b, `,"reasoning_details":[`...), c.details...), ']')
	}
	return append(c.appendToolCalls(b), '}')
}

// responsesTurn is the output items as the stream finished them.
func (c *serverCapture) responsesTurn(b []byte) []byte {
	b = append(b, '[')
	for i, blk := range c.blocks {
		if i > 0 {
			b = append(b, ',')
		}
		if len(blk.raw) > 0 {
			b = append(b, blk.raw...)
		} else {
			b = append(appendQuoted(append(b, `{"type":`...), blk.kind), '}')
		}
	}
	return append(b, ']')
}

func (c *serverCapture) serverCalls() []ServerToolCall {
	out := make([]ServerToolCall, 0, len(c.calls))
	for _, call := range c.calls {
		if !call.Client {
			out = append(out, call)
		}
	}
	return out
}

// appendToolCalls appends Chat's tool_calls member with the server calls, none when there are none.
func (c *serverCapture) appendToolCalls(b []byte) []byte {
	first := true
	for _, call := range c.calls {
		if call.Client {
			continue
		}
		if first {
			b = append(b, `,"tool_calls":[`...)
		} else {
			b = append(b, ',')
		}
		first = false
		name := call.Name
		if name == "" {
			name = ServerToolName
		}
		b = appendQuoted(append(b, `{"id":`...), call.ID)
		b = appendQuoted(append(b, `,"type":"function","function":{"name":`...), name)
		b = appendQuoted(append(b, `,"arguments":`...), call.Arguments)
		b = append(b, `}}`...)
	}
	if !first {
		b = append(b, ']')
	}
	return b
}

func appendBlock(b []byte, blk *capturedBlock) []byte {
	switch blk.kind {
	case "text":
		b = appendQuoted(append(b, `{"type":"text","text":`...), blk.text)
	case "thinking":
		b = appendQuoted(append(b, `{"type":"thinking","thinking":`...), blk.thinking)
		if blk.signature != "" {
			b = appendQuoted(append(b, `,"signature":`...), blk.signature)
		}
	case "tool_use":
		input := blk.input
		if len(input) == 0 {
			input = []byte("{}")
		}
		b = appendQuoted(append(b, `{"type":"tool_use","id":`...), blk.id)
		b = appendQuoted(append(b, `,"name":`...), blk.name)
		b = append(append(b, `,"input":`...), input...)
	default:
		if len(blk.raw) > 0 {
			return append(b, blk.raw...)
		}
		return append(b, `{}`...)
	}
	return append(b, '}')
}

// capture reads the client-format events a step carries, which a conversion may frame several of.
func (r *Relay) capture(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if bytes.TrimSpace(data)[0] == '{' {
		return r.captureEvent(data)
	}
	for len(data) > 0 {
		ev, rest, err := r.unframe(data)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(ev)) > 0 {
			if err := r.captureEvent(ev); err != nil {
				return err
			}
		}
		data = rest
	}
	return nil
}

// captureEvent reads one event into the capture within the node's budget: what the event may grow the capture by is
// charged before it is read, so nothing is allocated uncounted, and the charge is then brought to what the capture
// holds. An event the budget cannot cover is not read.
func (r *Relay) captureEvent(ev []byte) error {
	c := r.srv
	bound := c.growthBound(len(ev))
	if err := r.charge(bound); err != nil {
		return err
	}
	err := c.event(ev)
	held := c.footprint()
	if over := held - (c.held + bound); over > 0 {
		err = errors.Join(err, r.charge(over)) // unreachable while growthBound bounds event; kept honest if it does not
	} else {
		r.uncharge(-over)
	}
	c.held = held
	return err
}

// ServerCalls are the web search tool's calls the relay has read, with their arguments as the client-format events
// carried them. It is read after the upstream's end, before the relay is released.
func (r *Relay) ServerCalls() []ServerToolCall {
	if r.srv == nil {
		return nil
	}
	return r.srv.serverCalls()
}

// ServerTurn is the assistant turn behind ServerCalls, for replay in the loop's next request. Its bound is charged to
// the relay before it is built, and given back with the relay's: the turn is copied into the next request before the
// relay is released. A turn over the node's budget is ErrMemory.
func (r *Relay) ServerTurn() (ServerTurn, error) {
	if r.srv == nil {
		return ServerTurn{}, nil
	}
	if err := r.charge(r.srv.turnBound()); err != nil {
		return ServerTurn{}, err
	}
	return r.srv.turn(), nil
}
