package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // the request tree is decoded token by token, so each allocation can be charged first
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unsafe"

	"axiom/internal/platform/jsonx"
)

// noBudget is the budget of a Prepare outside a node: every draw and give is a no-op, and it is never mutated, so
// callers may share it.
var noBudget budget

// budget is what one Prepare may hold: every allocation it makes is drawn from the node's Memory before it is made,
// and what is dropped before Prepare returns is given back. A nil budget draws nothing, for the tests and callers that
// prepare a request outside a node.
type budget struct {
	m     *Memory
	held  int64 // drawn and not yet given back, for the tests that hold Prepare to it
	drawn int64 // what it has drawn over its life, refunds included, for the tests that hold it to what a run allocated
	peak  int64 // the most it held at once
	err   error // the first charge that failed, for the callers that build values without an error to return
	retry bool  // the failed draw could fit if competing preparations returned their charges
}

func (b *budget) draw(n int64) error {
	if b == nil || b.m == nil || n <= 0 {
		return nil
	}
	b.m.preparationMu.Lock()
	defer b.m.preparationMu.Unlock()
	if !b.m.Charge(n) {
		b.retry = b.m.preparationHeld > b.held && max(b.peak, b.held+n) <= b.m.left.Load()+b.m.preparationHeld
		b.err = ErrMemory
		return ErrMemory
	}
	b.m.preparationHeld += n
	b.held += n
	b.drawn += n
	b.peak = max(b.peak, b.held)
	return nil
}

func (b *budget) give(n int64) {
	if b == nil || b.m == nil || n <= 0 {
		return
	}
	b.m.preparationMu.Lock()
	defer b.m.preparationMu.Unlock()
	b.m.preparationHeld -= n
	b.m.Refund(n)
	b.held -= n
}

// fast reports whether nothing is drawn: a nil budget, or the shared noBudget of a Prepare outside a node.
func (b *budget) fast() bool { return b == nil || b.m == nil }

// bodyDecoderMemory is what a decoder of its own holds beyond the values it reads: jsontext's decoder and the
// bytes.Buffer it reads from, and the scratch a member name or an escaped string is unquoted into. Measured 2026-11-01
// at 1.2 KiB for a decoder over a 1 MiB body; the allowance leaves room for the runtime's own buffers.
const bodyDecoderMemory = 4 << 10

// mapEntryMemory is what an entry of a map with a string key and a value of up to 24 bytes takes, its share of every
// table the map grows through included; the key's own bytes are charged beside it. That share depends on where the
// count falls between the table's doublings: measured 2026-11-02 at about 165 bytes for 20,000 members of a request
// the decoder does not know and 197 for 4,000; TestPrepareAllocatesOnlyWhatItDrew holds it.
const mapEntryMemory = 256

// mapMemory is what a map takes before its first entry is charged: its header and the first group of eight slots,
// which the first insertion allocates.
const mapMemory = 512

// subDecoderMemory is what a decoder over one value of the body, a message's content, holds: its state and the buffer
// it reads from, about 400 bytes measured 2026-11-01 over 10,000 messages of one part each.
const subDecoderMemory = 512

// invaliderr is a body the decoder cannot read, in the words bodyFault gives the client.
func invaliderr(err error) error {
	return fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
}

// decodeCanonWithin reads a Chat body Inspect accepted, drawing each allocation from b first. It is the tree the conversions
// to Messages and Responses read; keeping it charged as it is built is what lets a cross-format request reserve what
// its shape actually takes rather than a bound over every shape.
// The decoder reads through a bytes.Buffer, whose bytes jsontext hands to ReadValue without copying and without
// invalidating them on the next read. The tree's values are slices of body, and are valid as long as body is.
func decodeCanonWithin(body []byte, b *budget) (*canon, error) {
	if b.fast() {
		return decodeCanon(body)
	}
	if err := b.draw(bodyDecoderMemory); err != nil {
		return nil, err
	}
	d := &decoder{dec: newDecoder(body), b: b}
	c := &canon{}
	if err := d.top(c); err != nil {
		return nil, d.fault(err)
	}
	if err := c.upgradeLegacy(b); err != nil {
		return nil, err
	}
	for i := range c.Messages {
		if err := settleMessage(&c.Messages[i], b); err != nil {
			return nil, err
		}
	}
	if b != nil && b.err != nil {
		return nil, b.err
	}
	return c, nil
}

// newDecoder reads v, a value of a body Inspect accepted, in place. Inspect has checked its names for duplicates, so
// the decoder does not track them again: jsontext records every name of an object past 64 of them and every level of
// nesting for that, which no draw sees.
func newDecoder(v []byte) *jsontext.Decoder {
	return jsontext.NewDecoder(bytes.NewBuffer(v), jsontext.AllowDuplicateNames(true))
}

// decoder reads one JSON value into the request tree, charging b for every array it grows, every map entry it adds and
// every string it copies before the allocation is made. Values it does not read into a field are kept as written, a
// slice of the body, and cost nothing. A nil b draws nothing.
type decoder struct {
	dec     *jsontext.Decoder
	b       *budget
	name    []byte // the member being read, unquoted; valid until the next read
	scratch []byte // unquoting room, grown through b
	parent  *decoder
	frames  []pathFrame // the values being read, for the JSON pointer of a type error
}

// pathFrame is one step of the JSON pointer to the value being read: a member's name as written, or an array index.
type pathFrame struct {
	name    jsontext.Value
	index   int
	isIndex bool
}

// fault is what the caller is told of an error met while reading: ours already carry ErrInvalid or NotSupported.
func (*decoder) fault(err error) error {
	if errors.Is(err, ErrInvalid) || errors.Is(err, ErrMemory) {
		return err
	}
	if _, ok := errors.AsType[*NotSupported](err); ok { //nolint:errcheck // absence is reported by ok
		return err
	}
	return invaliderr(err)
}

// typeError is what the client reads of a value of the wrong type, named by its JSON pointer as Inspect names it.
func (d *decoder) typeError() error {
	return fmt.Errorf("%w: the value at %s is not of a type this member takes", ErrInvalid, d.jsonPointer())
}

func (d *decoder) jsonPointer() string {
	var b strings.Builder
	if d.parent != nil {
		b.WriteString(d.parent.jsonPointer())
	}
	for _, f := range d.frames {
		b.WriteByte('/')
		if f.isIndex {
			b.WriteString(strconv.Itoa(f.index))
			continue
		}
		name, _ := jsontext.AppendUnquote(nil, f.name) //nolint:errcheck // a member name is a string token
		name = bytes.ReplaceAll(name, []byte("~"), []byte("~0"))
		name = bytes.ReplaceAll(name, []byte("/"), []byte("~1"))
		b.Write(name)
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

// object reads an object, calling read after each member's name is read into d.name.
func (d *decoder) object(read func() error) error {
	if d.dec.PeekKind() != '{' {
		return d.typeError()
	}
	if _, err := d.dec.ReadToken(); err != nil {
		return err
	}
	for d.dec.PeekKind() != '}' {
		v, err := d.dec.ReadValue()
		if err != nil {
			return err
		}
		if d.name, err = d.unquote(v); err != nil {
			return err
		}
		if d.frames, err = growWithin(d.frames, d.b); err != nil {
			return err
		}
		d.frames[len(d.frames)-1] = pathFrame{name: v}
		if err := read(); err != nil {
			return err
		}
		d.frames = d.frames[:len(d.frames)-1]
	}
	_, err := d.dec.ReadToken()
	return err
}

// array reads an array or null, calling each for each element.
func (d *decoder) array(each func() error) error {
	//nolint:exhaustive // the default rejects every other JSON kind with the client's error
	switch d.dec.PeekKind() {
	case 'n':
		_, err := d.dec.ReadValue()
		return err
	case '[':
	default:
		return d.typeError()
	}
	if _, err := d.dec.ReadToken(); err != nil {
		return err
	}
	for i := 0; d.dec.PeekKind() != ']'; i++ {
		var err error
		if d.frames, err = growWithin(d.frames, d.b); err != nil {
			return err
		}
		d.frames[len(d.frames)-1] = pathFrame{index: i, isIndex: true}
		if err := each(); err != nil {
			return err
		}
		d.frames = d.frames[:len(d.frames)-1]
	}
	_, err := d.dec.ReadToken()
	return err
}

// value reads one value as written.
func (d *decoder) value() (jsontext.Value, error) { return d.dec.ReadValue() }

// unquote unquotes a string token into d.scratch, charging its growth first; one without escapes is the body's own
// bytes, so it costs nothing.
func (d *decoder) unquote(v jsontext.Value) ([]byte, error) {
	var err error
	if d.scratch, err = growBytes(d.scratch[:0], len(v), d.b); err != nil {
		return nil, err
	}
	return jsontext.AppendUnquote(d.scratch[:0], v)
}

// str reads a string field; null is the empty string.
func (d *decoder) str() (string, error) {
	v, err := d.value()
	if err != nil {
		return "", err
	}
	//nolint:exhaustive // the default rejects every other JSON kind with the client's error
	switch v.Kind() {
	case 'n':
		return "", nil
	case '"':
	default:
		return "", d.typeError()
	}
	b, err := d.unquote(v)
	if err != nil {
		return "", err
	}
	if err := d.b.draw(int64(len(b))); err != nil {
		return "", err
	}
	return string(b), nil
}

// strp reads a string pointer; null is nil.
func (d *decoder) strp() (*string, error) {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return nil, err
	}
	s, err := d.str()
	if err != nil {
		return nil, err
	}
	if err := d.b.draw(int64(unsafe.Sizeof(s))); err != nil {
		return nil, err
	}
	return &s, nil
}

// intp reads an integer pointer; null is nil.
func (d *decoder) intp() (*int64, error) {
	v, err := d.value()
	if err != nil {
		return nil, err
	}
	if v.Kind() == 'n' {
		return nil, nil //nolint:nilnil // null means the member is absent
	}
	n, err := strconv.ParseInt(string(v), 10, 64)
	if err != nil {
		return nil, d.typeError()
	}
	if err := d.b.draw(int64(unsafe.Sizeof(n))); err != nil {
		return nil, err
	}
	return &n, nil
}

// boolp reads a boolean pointer; null is nil.
func (d *decoder) boolp() (*bool, error) {
	v, err := d.value()
	if err != nil {
		return nil, err
	}
	var b bool
	switch string(v) {
	case "null":
		return nil, nil //nolint:nilnil // null means the member is absent
	case "true":
		b = true
	case "false":
	default:
		return nil, d.typeError()
	}
	if err := d.b.draw(int64(unsafe.Sizeof(b))); err != nil {
		return nil, err
	}
	return &b, nil
}

// growWithin appends a zero value to a slice, drawing the array the append allocates from b first and giving back the
// one it replaces. The whole new array is charged, not only the part beyond the old capacity: the old array is still
// held until a collection, and a measurement of what a run allocates counts it.
func growWithin[T any](s []T, b *budget) ([]T, error) {
	var zero T
	if cap(s)-len(s) >= 1 {
		return append(s, zero), nil
	}
	size := max(2*cap(s), len(s)+1)
	size = max(size, 1)
	element := int64(unsafe.Sizeof(zero)) //nolint:gosec // a Go type's size fits in int64
	if err := b.draw(rounded(int64(size) * element)); err != nil {
		return s, err
	}
	out := make([]T, len(s), size)
	copy(out, s)
	b.give(rounded(int64(cap(s)) * element))
	return append(out, zero), nil
}

// growBytes is growWithin for a byte slice: room for n more bytes, charging the array it allocates first.
func growBytes(b []byte, n int, budget *budget) ([]byte, error) {
	if cap(b)-len(b) >= n {
		return b, nil
	}
	size := max(2*cap(b), len(b)+n)
	if err := budget.draw(rounded(int64(size))); err != nil {
		return b, err
	}
	out := make([]byte, len(b), size)
	copy(out, b)
	budget.give(rounded(int64(cap(b))))
	return out, nil
}

// rounded is what the runtime allocates for n bytes, so that what is charged covers it: the smallest of its size classes
// that holds n (runtime/sizeclasses.go), and whole 8 KiB pages past the largest. TestRoundedIsWhatTheRuntimeAllocates
// holds the table to the runtime the binary is built with.
func rounded(n int64) int64 {
	if n <= 0 {
		return 0
	}
	if n > sizeClasses[len(sizeClasses)-1] {
		return (n + 8191) &^ 8191
	}
	i, _ := slices.BinarySearch(sizeClasses[:], n)
	return sizeClasses[i]
}

var sizeClasses = [...]int64{
	8, 16, 24, 32, 48, 64, 80, 96, 112, 128, 144, 160, 176, 192, 208, 224, 240, 256, 288, 320, 352, 384, 416, 448, 480,
	512, 576, 640, 704, 768, 896, 1024, 1152, 1280, 1408, 1536, 1792, 2048, 2304, 2688, 3072, 3200, 3456, 4096, 4864,
	5376, 6144, 6528, 6784, 6912, 8192, 9472, 9728, 10240, 10880, 12288, 13568, 14336, 16384, 18432, 19072, 20480,
	21760, 24576, 27264, 28672, 32768,
}

// unknown adds a member the tree does not name to its unknown map, charging the entry first.
func (d *decoder) unknown(m *map[string]jsontext.Value) error {
	v, err := d.value()
	if err != nil {
		return err
	}
	if err := d.b.draw(mapEntryMemory); err != nil {
		return err
	}
	if *m == nil {
		*m = map[string]jsontext.Value{}
	}
	if err := d.b.draw(int64(len(d.name))); err != nil {
		return err
	}
	(*m)[string(d.name)] = v
	return nil
}

// top reads the Chat request's members.
//

// validateMemory is what one check of v's names for duplicates takes: jsontext records each name of an object past 64
// of them, about 150 bytes a name measured 2026-11-02 for 20,000. A colon is counted for each member, inside strings
// too, so it bounds them.
func validateMemory(v []byte) int64 {
	return mapEntryMemory * int64(bytes.Count(v, []byte(":")))
}

// unmarshalWithin decodes a small value of a request, drawing its allocation from b first.
//
// What it takes grows with the value's bytes and, as jsonx checks names for duplicates twice, once validating and
// once decoding, with its members.
func unmarshalWithin(v []byte, dst any, b *budget) error {
	if err := b.draw(rounded(int64(len(v))) + 512 + 2*validateMemory(v)); err != nil {
		return err
	}
	return jsonx.Unmarshal(v, dst)
}
