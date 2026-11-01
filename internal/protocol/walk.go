package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // values are walked token by token, so a skipped one allocates nothing
)

// walkDepth is the deepest object or array a walk opens; the capture goes deepest: a Chat event, its choices, a choice, its delta, the
// delta's tool calls, a call and its function.
const walkDepth = 7

// decoderMemory is what one of the walk's decoders keeps between events, its own state over a buffer it does not copy.
const decoderMemory = 1 << 10

// walker walks JSON values token by token, one decoder per depth over the value itself, so skipping a value or an
// element allocates nothing. The capture keeps one between events; a request-side scan makes one per call.
type walker struct {
	levels [walkDepth]walkLevel
}

// walkLevel is one depth of the walk: a decoder, the value it walks, and the member name it last read. A level can
// walk a value twice, for an object whose members came in an order it could not act on in one pass.
type walkLevel struct {
	buf  bytes.Buffer
	dec  *jsontext.Decoder
	name []byte
}

// walkOptions skip the per-object name set: tracking names for duplicates allocates per member.
var walkOptions = [...]jsontext.Options{jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)}

// errNotRead is a value a walk cannot read: the capture leaves such an event to the relay, a request scan refuses it.
type errNotRead struct{}

func (errNotRead) Error() string { return "protocol: a captured event that is not well formed" }

// open points the decoder of depth at v, without copying it. v is the event or a value the level above read, which
// stays valid until that level reads again (jsontext's ReadValue), and it does not while this level walks: a level
// returns before the one above it moves on. A bytes.Buffer is what the decoder parses in place.
func (c *walker) open(depth int, v []byte) *jsontext.Decoder {
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
func (c *walker) object(depth int, v []byte, f func(name []byte, v jsontext.Value) error) error {
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
func (c *walker) array(depth int, v []byte, f func(v jsontext.Value) error) error {
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
