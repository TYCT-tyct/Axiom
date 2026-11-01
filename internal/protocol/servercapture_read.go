package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // events are walked token by token, so only kept members are allocated
	"slices"
)

// The capture reads each event token by token: it materialises only the members it keeps (text, thinking, signatures,
// call ids, names and arguments, raw blocks) and skips every other value without decoding it. No array an upstream
// controls becomes a slice, so what reading an event allocates is bounded by the event's length, not by how many
// elements its arrays hold (growthBound).

// walkDepth is the deepest object or array the capture opens: a Chat event, its choices, a choice, its delta, the
// delta's tool calls, a call and its function.
const walkDepth = 7

// decoderMemory is what one of the walk's decoders keeps between events, its own state over a buffer it does not copy.
const decoderMemory = 1 << 10

// walkLevel is one depth of the walk: a decoder, the value it walks, and the member name it last read. A level can
// walk a value twice, for an object whose members came in an order it could not act on in one pass.
type walkLevel struct {
	buf  bytes.Buffer
	dec  *jsontext.Decoder
	name []byte
}

// walkOptions skip the per-object name set: tracking names for duplicates allocates per member.
var walkOptions = [...]jsontext.Options{jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)}

// errNotRead is a value the capture cannot walk, which it leaves to the relay as it leaves an event it cannot read.
type errNotRead struct{}

func (errNotRead) Error() string { return "protocol: a captured event that is not well formed" }

// open points the decoder of depth at v, without copying it. v is the event or a value the level above read, which
// stays valid until that level reads again (jsontext's ReadValue), and it does not while this level walks: a level
// returns before the one above it moves on. A bytes.Buffer is what the decoder parses in place.
func (c *serverCapture) open(depth int, v []byte) *jsontext.Decoder {
	l := &c.levels[depth]
	l.buf = *bytes.NewBuffer(v)
	if l.dec == nil {
		l.dec = jsontext.NewDecoder(&l.buf, walkOptions[0], walkOptions[1])
	} else {
		l.dec.Reset(&l.buf, walkOptions[0], walkOptions[1])
	}
	return l.dec
}

// object calls f with each member of the object v, at depth; a value is valid only during its call. A v that is not
// an object has no members.
func (c *serverCapture) object(depth int, v []byte, f func(name []byte, v jsontext.Value) error) error {
	if len(v) == 0 || v[0] != '{' {
		return nil
	}
	d, l := c.open(depth, v), &c.levels[depth]
	if _, err := d.ReadToken(); err != nil {
		return errNotRead{}
	}
	for d.PeekKind() != '}' {
		n, err := d.ReadValue()
		if err != nil {
			return errNotRead{}
		}
		l.name = unquote(l.name[:0], n)
		value, err := d.ReadValue()
		if err != nil {
			return errNotRead{}
		}
		if err := f(l.name, value); err != nil {
			return err
		}
	}
	return nil
}

// array calls f with each element of the array v, at depth; an element is valid only during its call.
func (c *serverCapture) array(depth int, v []byte, f func(v jsontext.Value) error) error {
	if len(v) == 0 || v[0] != '[' {
		return nil
	}
	d := c.open(depth, v)
	if _, err := d.ReadToken(); err != nil {
		return errNotRead{}
	}
	for d.PeekKind() != ']' {
		e, err := d.ReadValue()
		if err != nil {
			return errNotRead{}
		}
		if err := f(e); err != nil {
			return err
		}
	}
	return nil
}

// unquote appends the string v holds to dst, and nothing for a value that is not a string. Invalid UTF-8 is replaced,
// as a decoder of the whole event would have.
func unquote(dst []byte, v jsontext.Value) []byte {
	if v.Kind() != '"' {
		return dst
	}
	// One growth for the whole string, which unquoted is no longer than quoted: appending it escape by escape would
	// grow dst many times in one event, each a copy of all of it.
	dst = slices.Grow(dst, len(v))
	out, _ := jsontext.AppendUnquote(dst, v) //nolint:errcheck // the error only reports a replaced invalid byte
	return out
}

// integer is the non-negative integer v holds, 0 for anything else, as a typed decode of an absent member gives.
func integer(v jsontext.Value) int64 {
	var n int64
	for i, b := range v {
		if b < '0' || b > '9' || i >= 18 {
			return 0
		}
		n = n*10 + int64(b-'0')
	}
	return n
}

// event reads one client-format event. An event it cannot walk is left to the relay, which will refuse it if it
// matters; only a matching call that grows past its bound fails here.
func (c *serverCapture) event(data []byte) error {
	data = bytes.TrimSpace(data)
	var err error
	//nolint:exhaustive // only the three chat formats carry tool calls
	switch c.format {
	case Chat:
		err = c.object(0, data, func(name []byte, v jsontext.Value) error {
			if string(name) != "choices" {
				return nil
			}
			return c.array(1, v, c.chatChoice)
		})
	case Messages:
		err = c.messagesEvent(data)
	case Responses:
		err = c.responsesEvent(data)
	}
	if _, ok := err.(errNotRead); ok { //nolint:errorlint // the walk returns it unwrapped
		return nil
	}
	return err
}

// scratch is one of the capture's reusable buffers, emptied.
func (c *serverCapture) scratch(i int) []byte { return c.scr[i][:0] }

// Chat: a choice's index decides where its delta goes, and the delta may come before it, so the choice is read twice.
func (c *serverCapture) chatChoice(e jsontext.Value) error {
	var index int64
	if err := c.object(2, e, func(name []byte, v jsontext.Value) error {
		if string(name) == "index" {
			index = integer(v)
		}
		return nil
	}); err != nil {
		return err
	}
	return c.object(2, e, func(name []byte, v jsontext.Value) error {
		if string(name) != "delta" {
			return nil
		}
		return c.chatDelta(index, v)
	})
}

// chatDelta keeps the first choice's text and reasoning, with the reasoning_details entries that carry signatures and
// encrypted reasoning, and every choice's tool calls. reasoning counts only when reasoning_content says nothing.
func (c *serverCapture) chatDelta(index int64, d jsontext.Value) error {
	if index != 0 {
		return c.object(3, d, func(name []byte, v jsontext.Value) error {
			if string(name) != "tool_calls" {
				return nil
			}
			return c.array(4, v, func(e jsontext.Value) error { return c.chatCall(index, e) })
		})
	}
	alt, said := c.scratch(0), false
	err := c.object(3, d, func(name []byte, v jsontext.Value) error {
		switch string(name) {
		case "content":
			c.text = unquote(c.text, v)
		case "reasoning_content":
			n := len(c.reasoning)
			c.reasoning = unquote(c.reasoning, v)
			said = said || len(c.reasoning) > n
		case "reasoning":
			alt = unquote(alt, v)
		case "reasoning_details":
			c.details = slices.Grow(c.details, len(v)) // the entries and their commas fit in the array's own length
			return c.array(4, v, c.chatDetail)
		case "tool_calls":
			return c.array(4, v, func(e jsontext.Value) error { return c.chatCall(0, e) })
		}
		return nil
	})
	if err == nil && !said {
		c.reasoning = append(c.reasoning, alt...)
	}
	c.scr[0] = alt
	return err
}

// chatDetail keeps one reasoning_details entry as written; turn merges them.
func (c *serverCapture) chatDetail(e jsontext.Value) error {
	if len(c.details) > 0 {
		c.details = append(c.details, ',')
	}
	c.details = append(c.details, e...)
	c.nDetails++
	return nil
}

// chatCall reads one tool call fragment: a name opens or renames the call at its index, the arguments grow it.
func (c *serverCapture) chatCall(choice int64, e jsontext.Value) error {
	var index int64
	id, name, args := c.scratch(1), c.scratch(2), c.scratch(3)
	err := c.object(5, e, func(member []byte, v jsontext.Value) error {
		switch string(member) {
		case "index":
			index = integer(v)
		case "id":
			id = unquote(id, v)
		case "function":
			return c.object(6, v, func(member []byte, v jsontext.Value) error {
				switch string(member) {
				case "name":
					name = unquote(name, v)
				case "arguments":
					args = unquote(args, v)
				}
				return nil
			})
		}
		return nil
	})
	c.scr[1], c.scr[2], c.scr[3] = id, name, args
	if err != nil {
		return err
	}
	return c.chatFragment(choice<<32|index, id, name, args)
}

// chatFragment applies one tool call fragment to the call at key.
func (c *serverCapture) chatFragment(key int64, id, name, args []byte) error {
	at, ok := c.index[key]
	switch {
	case len(name) > 0:
		if !ok {
			at = len(c.calls)
			c.index[key] = at
			c.calls = append(c.calls, ServerToolCall{})
		}
		call := &c.calls[at]
		if call.Name != string(name) {
			call.Name = string(name)
		}
		call.Client = call.Name != ServerToolName
		c.client = c.client || call.Client
		if len(id) > 0 && call.ID != string(id) {
			call.ID = string(id)
		}
	case !ok:
		return nil
	}
	return c.append(at, args)
}

// messagesEvent: the type decides which member is read, and may come after it, so the event is read twice.
func (c *serverCapture) messagesEvent(data []byte) error {
	typ, index, err := c.typed(data, "index")
	if err != nil {
		return err
	}
	switch string(typ) {
	case "content_block_start":
		return c.object(0, data, func(name []byte, v jsontext.Value) error {
			if string(name) != "content_block" {
				return nil
			}
			return c.messagesBlock(index, v)
		})
	case "content_block_delta":
		return c.object(0, data, func(name []byte, v jsontext.Value) error {
			if string(name) != "delta" {
				return nil
			}
			return c.messagesDelta(index, v)
		})
	}
	return nil
}

// typed reads an event's type, into scratch 4, and the index member named.
func (c *serverCapture) typed(data []byte, indexName string) ([]byte, int64, error) {
	typ := c.scratch(4)
	var index int64
	err := c.object(0, data, func(name []byte, v jsontext.Value) error {
		switch string(name) {
		case "type":
			typ = unquote(typ[:0], v)
		case indexName:
			index = integer(v)
		}
		return nil
	})
	c.scr[4] = typ
	return typ, index, err
}

// messagesBlock starts a content block. A kind this capture does not know keeps its start object as written.
func (c *serverCapture) messagesBlock(index int64, v jsontext.Value) error {
	var b capturedBlock
	s, input := c.scratch(5), c.scratch(10)
	err := c.object(1, v, func(name []byte, v jsontext.Value) error {
		if string(name) == "input" {
			input = append(input[:0], v...)
		} else if dst := b.member(name); dst != nil {
			s = unquote(s[:0], v)
			*dst = string(s)
		}
		return nil
	})
	c.scr[5], c.scr[10] = s, input
	if err != nil {
		return err
	}
	switch b.kind {
	case "text", "thinking":
	case "tool_use":
		if len(input) > 0 && string(input) != "{}" {
			b.input = bytes.Clone(input)
		}
	default:
		b.raw = bytes.Clone(v)
	}
	return c.addBlock(index, b)
}

// member is the string field a content block's member of that name is kept in, nil for one it does not keep.
func (b *capturedBlock) member(name []byte) *string {
	switch string(name) {
	case "type":
		return &b.kind
	case "id":
		return &b.id
	case "name":
		return &b.name
	case "text":
		return &b.text
	case "thinking":
		return &b.thinking
	case "signature":
		return &b.signature
	}
	return nil
}

// addBlock appends a started block; a tool_use block of the gateway's tool is also a server call.
func (c *serverCapture) addBlock(index int64, b capturedBlock) error {
	c.blockAt[index] = len(c.blocks)
	if b.kind == "tool_use" {
		b.client = b.name != ServerToolName
		c.client = c.client || b.client
		if !b.client {
			c.index[index] = len(c.calls)
			c.calls = append(c.calls, ServerToolCall{ID: b.id, Name: b.name, Arguments: string(b.input)})
			c.args += len(b.input)
		}
	}
	c.blocks = append(c.blocks, b)
	if b.kind == "tool_use" && len(b.input) > 0 {
		return c.bound()
	}
	return nil
}

// messagesDelta adds a delta to its block. Its type may come after the text it carries, so each text member is read
// into a scratch of its own and the type picks one after the delta is read.
func (c *serverCapture) messagesDelta(index int64, v jsontext.Value) error {
	at, ok := c.blockAt[index]
	if !ok {
		return nil
	}
	typ := c.scratch(5)
	var text [len(deltaMembers)][]byte
	for i := range text {
		text[i] = c.scratch(6 + i)
	}
	err := c.object(1, v, func(name []byte, v jsontext.Value) error {
		if string(name) == "type" {
			typ = unquote(typ[:0], v)
		} else if i := slices.Index(deltaMembers[:], string(name)); i >= 0 {
			text[i] = unquote(text[i][:0], v)
		}
		return nil
	})
	c.scr[5] = typ
	for i := range text {
		c.scr[6+i] = text[i]
	}
	if err != nil {
		return err
	}
	return c.applyDelta(index, at, typ, &text)
}

// deltaMembers are the members a Messages delta carries its text in, by delta type in applyDelta's order.
var deltaMembers = [...]string{"text", "thinking", "signature", "partial_json"}

// applyDelta adds the member a delta of type typ carries to block at.
func (c *serverCapture) applyDelta(index int64, at int, typ []byte, text *[len(deltaMembers)][]byte) error {
	b := &c.blocks[at]
	switch string(typ) {
	case "text_delta":
		b.text += string(text[0])
	case "thinking_delta":
		b.thinking += string(text[1])
	case "signature_delta":
		b.signature += string(text[2])
	case "input_json_delta":
		if b.kind != "tool_use" {
			return nil
		}
		b.input = append(b.input, text[3]...)
		if call, ok := c.index[index]; ok {
			return c.append(call, text[3])
		}
	}
	return nil
}

// responsesEvent: the type decides which member is read, and may come after it, so the event is read twice.
func (c *serverCapture) responsesEvent(data []byte) error {
	typ, at, err := c.typed(data, "output_index")
	if err != nil {
		return err
	}
	switch string(typ) {
	case "response.output_item.added":
		return c.object(0, data, func(name []byte, v jsontext.Value) error {
			if string(name) != "item" {
				return nil
			}
			return c.responsesItem(at, v)
		})
	case "response.function_call_arguments.delta":
		call, ok := c.index[at]
		if !ok {
			return nil
		}
		return c.object(0, data, func(name []byte, v jsontext.Value) error {
			if string(name) != "delta" {
				return nil
			}
			s := unquote(c.scratch(5), v)
			c.scr[5] = s
			return c.append(call, s)
		})
	case "response.function_call_arguments.done", "response.output_item.done":
		return c.responsesDone(string(typ) == "response.output_item.done", at, data)
	}
	return nil
}

// responsesItem starts an output item, kept as written; a function call of the gateway's tool is a server call.
func (c *serverCapture) responsesItem(output int64, v jsontext.Value) error {
	typ, name, id, args := c.scratch(5), c.scratch(6), c.scratch(1), c.scratch(3)
	err := c.object(1, v, func(member []byte, v jsontext.Value) error {
		switch string(member) {
		case "type":
			typ = unquote(typ[:0], v)
		case "name":
			name = unquote(name[:0], v)
		case "call_id":
			id = unquote(id[:0], v)
		case "arguments":
			args = unquote(args[:0], v)
		}
		return nil
	})
	c.scr[5], c.scr[6], c.scr[1], c.scr[3] = typ, name, id, args
	if err != nil {
		return err
	}
	c.blockAt[output] = len(c.blocks)
	c.blocks = append(c.blocks, capturedBlock{kind: string(typ), raw: bytes.Clone(v)})
	if string(typ) != "function_call" {
		return nil
	}
	c.client = c.client || string(name) != ServerToolName
	if string(name) != ServerToolName {
		return nil
	}
	c.index[output] = len(c.calls)
	c.calls = append(c.calls, ServerToolCall{ID: string(id), Name: ServerToolName, Arguments: string(args)})
	c.args += len(args)
	return c.bound()
}

// responsesDone takes a call's final arguments, from the done event or the finished item, which also replaces the
// item's start as what the turn replays.
func (c *serverCapture) responsesDone(item bool, output int64, data []byte) error {
	args := c.scratch(3)
	err := c.object(0, data, func(name []byte, v jsontext.Value) error {
		switch {
		case !item && string(name) == "arguments":
			args = unquote(args[:0], v)
		case item && string(name) == "item":
			if at, ok := c.blockAt[output]; ok {
				c.blocks[at].raw = bytes.Clone(v)
			}
			return c.object(1, v, func(member []byte, v jsontext.Value) error {
				if string(member) == "arguments" {
					args = unquote(args[:0], v)
				}
				return nil
			})
		}
		return nil
	})
	c.scr[3] = args
	if err != nil {
		return err
	}
	at, ok := c.index[output]
	if !ok || len(args) == 0 {
		return nil
	}
	if c.calls[at].Arguments != string(args) {
		c.args += len(args) - len(c.calls[at].Arguments)
		c.calls[at].Arguments = string(args)
	}
	return c.bound()
}
