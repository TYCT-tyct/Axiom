package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // the request tree is decoded token by token, so each allocation can be charged first
	"errors"
	"fmt"
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
}

func (b *budget) draw(n int64) error {
	if b == nil || b.m == nil || n <= 0 {
		return nil
	}
	if !b.m.Charge(n) {
		b.err = ErrMemory
		return ErrMemory
	}
	b.held += n
	b.drawn += n
	b.peak = max(b.peak, b.held)
	return nil
}

func (b *budget) give(n int64) {
	if b == nil || b.m == nil || n <= 0 {
		return
	}
	b.m.Refund(n)
	b.held -= n
}

func (b *budget) charge(n int) error { return b.draw(int64(n)) }

// fast reports whether nothing is drawn: a nil budget, or the shared noBudget of a Prepare outside a node.
func (b *budget) fast() bool { return b == nil || b.m == nil }

// What a decoder of its own holds beyond the values it reads: jsontext's decoder and the bytes.Buffer it reads from,
// and the scratch a member name or an escaped string is unquoted into. Measured 2026-11-01 at 1.2 KiB for a decoder
// over a 1 MiB body; the allowance leaves room for the runtime's own buffers.
const decoderMemory = 4 << 10

// mapEntryMemory is what an entry of a map with a string key and a 24-byte value takes, buckets and load factor
// included: each entry is charged before it is inserted. Measured 2026-11-01: 100,000 unknown members of a request
// take 48.5 MiB, 509 bytes each including their keys; an entry of a decoded map is far smaller, and this covers the
// bucket it is inserted into as well.
const mapEntryMemory = 128

// subDecoderMemory is what a decoder over one value of the body, a message's content or an object of a conversion, holds:
// its state and the buffer it reads from.
const subDecoderMemory = 256

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
	if err := jsonx.Validate(body); err != nil {
		return nil, invaliderr(err)
	}
	if err := b.draw(decoderMemory); err != nil {
		return nil, err
	}
	d := &decoder{dec: jsontext.NewDecoder(bytes.NewBuffer(body)), b: b}
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
func (d *decoder) fault(err error) error {
	if errors.Is(err, ErrInvalid) || errors.Is(err, ErrMemory) {
		return err
	}
	var unsupported *NotSupported
	if errors.As(err, &unsupported) {
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
		return nil, nil
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
		return nil, nil
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
	element := int64(unsafe.Sizeof(zero))
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

// rounded rounds an allocation up to the granularity of the runtime's size classes, so that what is charged covers
// what the runtime allocates for the request.
func rounded(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return (n + 15) &^ 15
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
//nolint:cyclop // one dispatch over the members canon names, each one line
func (d *decoder) top(c *canon) error {
	return d.object(func() error {
		switch string(d.name) {
		case "messages":
			return d.array(func() error {
				var err error
				if c.Messages, err = growWithin(c.Messages, d.b); err != nil {
					return err
				}
				return d.message(&c.Messages[len(c.Messages)-1])
			})
		case "tools":
			return d.array(func() error {
				var err error
				if c.Tools, err = growWithin(c.Tools, d.b); err != nil {
					return err
				}
				return d.tool(&c.Tools[len(c.Tools)-1])
			})
		case "functions":
			return d.array(func() error {
				var err error
				if c.Functions, err = growWithin(c.Functions, d.b); err != nil {
					return err
				}
				return d.function(&c.Functions[len(c.Functions)-1])
			})
		case "tool_choice":
			return d.field(&c.ToolChoice)
		case "function_call":
			return d.field(&c.FunctionCall)
		case "parallel_tool_calls":
			return d.pointer(&c.ParallelToolCalls, (*decoder).boolp)
		case "max_tokens":
			return d.pointer(&c.MaxTokens, (*decoder).intp)
		case "max_completion_tokens":
			return d.pointer(&c.MaxCompletionTokens, (*decoder).intp)
		case "temperature":
			return d.field(&c.Temperature)
		case "top_p":
			return d.field(&c.TopP)
		case "stop":
			return d.field(&c.Stop)
		case "stream":
			return d.pointer(&c.Stream, (*decoder).boolp)
		case "reasoning_effort":
			return d.pointer(&c.ReasoningEffort, (*decoder).strp)
		case "reasoning":
			return d.pointer(&c.Reasoning, (*decoder).reasoning)
		case "response_format":
			return d.pointer(&c.ResponseFormat, (*decoder).format)
		case "user":
			return d.pointer(&c.User, (*decoder).strp)
		case "safety_identifier":
			return d.pointer(&c.SafetyIdentifier, (*decoder).strp)
		case "prompt_cache_key":
			return d.pointer(&c.PromptCacheKey, (*decoder).strp)
		case "service_tier":
			return d.pointer(&c.ServiceTier, (*decoder).strp)
		case "verbosity":
			return d.field(&c.Verbosity)
		case "model":
			return d.field(&c.Model)
		case "stream_options":
			return d.field(&c.StreamOptions)
		case "preset":
			return d.field(&c.Preset)
		case "fallbacks":
			return d.field(&c.Fallbacks)
		case "provider":
			return d.field(&c.Provider)
		case "models":
			return d.field(&c.Models)
		case "route":
			return d.field(&c.Route)
		case "transforms":
			return d.field(&c.Transforms)
		case "plugins":
			return d.field(&c.Plugins)
		case "usage":
			return d.field(&c.Usage)
		case "session_id":
			return d.field(&c.SessionID)
		case "axiom":
			return d.field(&c.Axiom)
		default:
			return d.unknown(&c.Unknown)
		}
	})
}

// field reads a member kept as written.
func (d *decoder) field(dst *jsontext.Value) error {
	v, err := d.value()
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

// pointer reads an optional pointer member through read; null is nil.
func (d *decoder) pointer[T any](dst **T, read func(*decoder) (*T, error)) error {
	v, err := read(d)
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

// message reads one message or the zero value of null.
func (d *decoder) message(m *canonMessage) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "role":
			s, err := d.str()
			m.Role = s
			return err
		case "content":
			v, err := d.value()
			m.Content = v
			if err != nil {
				return err
			}
			m.Parts, err = d.parts(v)
			return err
		case "tool_calls":
			return d.array(func() error {
				var err error
				if m.ToolCalls, err = growWithin(m.ToolCalls, d.b); err != nil {
					return err
				}
				return d.toolCall(&m.ToolCalls[len(m.ToolCalls)-1])
			})
		case "tool_call_id":
			return d.pointer(&m.ToolCallID, (*decoder).strp)
		case "function_call":
			return d.field(&m.FunctionCall)
		case "reasoning_details":
			return d.array(func() error {
				var err error
				if m.ReasoningDetails, err = growWithin(m.ReasoningDetails, d.b); err != nil {
					return err
				}
				return d.reasoningDetail(&m.ReasoningDetails[len(m.ReasoningDetails)-1])
			})
		case "name":
			return d.field(&m.Name)
		case "refusal":
			return d.field(&m.Refusal)
		case "reasoning":
			return d.field(&m.ReasoningText)
		case "reasoning_content":
			return d.field(&m.ReasoningContent)
		default:
			return d.unknown(&m.Unknown)
		}
	})
}

// parts reads a message's content: a string is one text part, null none, an array its parts.
func (d *decoder) parts(v jsontext.Value) ([]canonPart, error) {
	switch v.Kind() {
	case 'n':
		return nil, nil
	case '"':
		if err := d.b.draw(int64(unsafe.Sizeof(canonPart{}))); err != nil {
			return nil, err
		}
		return []canonPart{{Type: "text", Text: v}}, nil
	case '[':
	default:
		return nil, d.typeError()
	}
	if err := d.b.draw(subDecoderMemory); err != nil {
		return nil, err
	}
	pd := &decoder{dec: jsontext.NewDecoder(bytes.NewBuffer(v)), b: d.b, parent: d}
	var parts []canonPart
	if err := pd.array(func() error {
		var err error
		if parts, err = growWithin(parts, d.b); err != nil {
			return err
		}
		return pd.part(&parts[len(parts)-1])
	}); err != nil {
		return nil, err
	}
	return parts, nil
}

// part reads one content part.
func (d *decoder) part(p *canonPart) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "type":
			s, err := d.str()
			p.Type = s
			return err
		case "text":
			return d.field(&p.Text)
		case "refusal":
			return d.field(&p.Refusal)
		case "image_url":
			return d.pointer(&p.ImageURL, (*decoder).imageURL)
		case "file":
			return d.pointer(&p.File, (*decoder).file)
		case "cache_control":
			return d.field(&p.CacheControl)
		default:
			return d.skip()
		}
	})
}

func (d *decoder) imageURL() (*canonImage, error) {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return nil, err
	}
	if err := d.b.draw(int64(unsafe.Sizeof(canonImage{}))); err != nil {
		return nil, err
	}
	p := &canonImage{}
	return p, d.object(func() error {
		switch string(d.name) {
		case "url":
			s, err := d.str()
			p.URL = s
			return err
		case "detail":
			s, err := d.str()
			p.Detail = s
			return err
		default:
			return d.skip()
		}
	})
}

func (d *decoder) file() (*canonFile, error) {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return nil, err
	}
	if err := d.b.draw(int64(unsafe.Sizeof(canonFile{}))); err != nil {
		return nil, err
	}
	p := &canonFile{}
	return p, d.object(func() error {
		switch string(d.name) {
		case "file_data":
			return d.pointer(&p.FileData, (*decoder).strp)
		case "file_id":
			return d.pointer(&p.FileID, (*decoder).strp)
		case "filename":
			return d.field(&p.Filename)
		default:
			return d.skip()
		}
	})
}

func (d *decoder) tool(t *canonTool) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "type":
			s, err := d.str()
			t.Type = s
			return err
		case "function":
			return d.function(&t.Function)
		case "cache_control":
			return d.field(&t.CacheControl)
		default:
			return d.skip()
		}
	})
}

func (d *decoder) function(f *canonFunction) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "name":
			return d.field(&f.Name)
		case "description":
			return d.field(&f.Description)
		case "parameters":
			return d.field(&f.Parameters)
		case "strict":
			return d.pointer(&f.Strict, (*decoder).boolp)
		default:
			return d.skip()
		}
	})
}

func (d *decoder) toolCall(c *canonToolCall) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "id":
			s, err := d.str()
			c.ID = s
			return err
		case "type":
			s, err := d.str()
			c.Type = s
			return err
		case "function":
			return d.callFunction(&c.Function)
		default:
			return d.skip()
		}
	})
}

func (d *decoder) callFunction(f *canonCallFunction) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "name":
			return d.field(&f.Name)
		case "arguments":
			return d.field(&f.Arguments)
		default:
			return d.skip()
		}
	})
}

func (d *decoder) reasoning() (*canonReasoning, error) {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return nil, err
	}
	if err := d.b.draw(int64(unsafe.Sizeof(canonReasoning{}))); err != nil {
		return nil, err
	}
	v := &canonReasoning{}
	if err := d.object(func() error {
		switch string(d.name) {
		case "effort":
			return d.pointer(&v.Effort, (*decoder).strp)
		case "max_tokens":
			return d.pointer(&v.MaxTokens, (*decoder).intp)
		case "exclude":
			return d.pointer(&v.Exclude, (*decoder).boolp)
		case "enabled":
			return d.pointer(&v.Enabled, (*decoder).boolp)
		default:
			return d.skip()
		}
	}); err != nil {
		return nil, err
	}
	return v, nil
}

func (d *decoder) format() (*canonFormat, error) {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return nil, err
	}
	if err := d.b.draw(int64(unsafe.Sizeof(canonFormat{}))); err != nil {
		return nil, err
	}
	v := &canonFormat{}
	if err := d.object(func() error {
		switch string(d.name) {
		case "type":
			s, err := d.str()
			v.Type = s
			return err
		case "json_schema":
			if d.dec.PeekKind() == 'n' {
				_, err := d.value()
				return err
			}
			if err := d.b.draw(int64(unsafe.Sizeof(*v.JSONSchema))); err != nil {
				return err
			}
			v.JSONSchema = &struct {
				Name        jsontext.Value `json:"name"`
				Description jsontext.Value `json:"description"`
				Schema      jsontext.Value `json:"schema"`
				Strict      *bool          `json:"strict"`
			}{}
			return d.object(func() error {
				switch string(d.name) {
				case "name":
					return d.field(&v.JSONSchema.Name)
				case "description":
					return d.field(&v.JSONSchema.Description)
				case "schema":
					return d.field(&v.JSONSchema.Schema)
				case "strict":
					return d.pointer(&v.JSONSchema.Strict, (*decoder).boolp)
				default:
					return d.skip()
				}
			})
		default:
			return d.skip()
		}
	}); err != nil {
		return nil, err
	}
	return v, nil
}

func (d *decoder) reasoningDetail(r *canonReasoningDetail) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "type":
			s, err := d.str()
			r.Type = s
			return err
		case "format":
			s, err := d.str()
			r.Format = s
			return err
		case "id":
			return d.field(&r.ID)
		case "text":
			return d.field(&r.Text)
		case "summary":
			return d.field(&r.Summary)
		case "signature":
			return d.field(&r.Signature)
		case "data":
			return d.field(&r.Data)
		case "index":
			return d.field(&r.Index)
		default:
			return d.skip()
		}
	})
}

// skip reads one value token by token, so a member the tree does not keep still validates its nesting.
func (d *decoder) skip() error {
	depth := d.dec.StackDepth()
	for {
		if err := d.token(); err != nil {
			return err
		}
		if d.dec.StackDepth() == depth {
			return nil
		}
	}
}

func (d *decoder) token() error {
	_, err := d.dec.ReadToken()
	return err
}

// membersFast and arrayFast are the unbudgeted readers, kept apart so the fast path inlines as it did before the
// budget existed.
func membersFast(v []byte) (requestObject, error) {
	var o requestObject
	if err := jsonx.Unmarshal(v, &o); err != nil || o == nil {
		return nil, shapeError("expected a request object")
	}
	return o, nil
}

func arrayFast(v jsontext.Value) ([]jsontext.Value, error) {
	if isNull(v) {
		return nil, nil
	}
	var out []jsontext.Value
	if err := jsonx.Unmarshal(v, &out); err != nil {
		return nil, shapeError("expected an array")
	}
	return out, nil
}

func jsonValueFast(v any) jsontext.Value {
	data, err := jsonx.MarshalCanonical(v)
	if err != nil {
		panic(fmt.Sprintf("protocol: invalid constructed JSON: %v", err))
	}
	return data
}

// membersWithin decodes an object of JSON values as written, charging each map entry first. Values alias v.
func membersWithin(v []byte, b *budget) (requestObject, error) {
	if b.fast() {
		return membersFast(v)
	}
	if err := b.draw(subDecoderMemory); err != nil {
		return nil, err
	}
	d := &decoder{dec: jsontext.NewDecoder(bytes.NewBuffer(v)), b: b}
	o := requestObject{}
	err := d.object(func() error {
		value, err := d.value()
		if err != nil {
			return err
		}
		if err := b.draw(mapEntryMemory + int64(len(d.name))); err != nil {
			return err
		}
		o[string(d.name)] = value
		return nil
	})
	if errors.Is(err, ErrMemory) {
		return nil, err
	}
	if err != nil || d.dec.PeekKind() != 0 {
		return nil, shapeError("expected a request object")
	}
	return o, nil
}

// arrayWithin decodes an array of JSON values as written, charging the slice as it grows.
func arrayWithin(v jsontext.Value, b *budget) ([]jsontext.Value, error) {
	if b.fast() {
		return arrayFast(v)
	}
	if isNull(v) {
		return nil, nil
	}
	if err := b.draw(subDecoderMemory); err != nil {
		return nil, err
	}
	d := &decoder{dec: jsontext.NewDecoder(bytes.NewBuffer(v)), b: b}
	var out []jsontext.Value
	err := d.array(func() error {
		value, err := d.value()
		if err != nil {
			return err
		}
		if out, err = growWithin(out, b); err != nil {
			return err
		}
		out[len(out)-1] = value
		return nil
	})
	if errors.Is(err, ErrMemory) {
		return nil, err
	}
	if err != nil {
		return nil, shapeError("expected an array")
	}
	return out, nil
}

// jsonValueWithin is jsonValue drawing the encoded value's allocation from b first: the same value jsonValue writes,
// whose size is counted without encoding it.
func jsonValueWithin(v any, b *budget) jsontext.Value {
	if b.fast() {
		return jsonValueFast(v)
	}
	if err := b.draw(encodedSize(v) + structureMemory(v)); err != nil {
		return jsontext.Value("null") // the stage checks b.err before it returns
	}
	data, err := jsonx.MarshalCanonical(v)
	if err != nil {
		panic(fmt.Sprintf("protocol: invalid constructed JSON: %v", err))
	}
	return data
}

// encodedSize is how many bytes jsonx.MarshalCanonical writes for a value the conversions construct, counted without
// allocating it.
func encodedSize(v any) int64 {
	switch v := v.(type) {
	case nil:
		return 4
	case jsontext.Value:
		return int64(len(v))
	case string:
		return int64(quotedLength(v))
	case bool:
		if v {
			return 4
		}
		return 5
	case int:
		return int64(len(strconv.Itoa(v)))
	case int64:
		return int64(len(strconv.FormatInt(v, 10)))
	case requestObject:
		n := int64(2)
		first := true
		for name, value := range v {
			if !first {
				n++
			}
			first = false
			n += int64(quotedLength(name)) + 1 + encodedSize(value)
		}
		return n
	case []requestObject:
		return listSize(len(v), func(i int) int64 { return encodedSize(v[i]) })
	case []jsontext.Value:
		return listSize(len(v), func(i int) int64 { return encodedSize(v[i]) })
	case []any:
		return listSize(len(v), func(i int) int64 { return encodedSize(v[i]) })
	case map[string]any:
		n := int64(2)
		first := true
		for name, value := range v {
			if !first {
				n++
			}
			first = false
			n += int64(quotedLength(name)) + 1 + encodedSize(value)
		}
		return n
	case float64:
		return int64(len(strconv.FormatFloat(v, 'g', -1, 64)))
	}
	// A type the conversions do not build: encode it to learn its size, which is exact though it allocates first.
	data, err := jsonx.MarshalCanonical(v)
	if err != nil {
		panic(fmt.Sprintf("protocol: invalid constructed JSON: %v", err))
	}
	return int64(len(data))
}

// structureMemory is what the maps and slices of a constructed value take beyond the bytes they encode, counted
// recursively: the maps were made while reading the request and are alive until the value is written.
func structureMemory(v any) int64 {
	switch v := v.(type) {
	case requestObject:
		n := mapEntryMemory * int64(len(v))
		for _, value := range v {
			n += structureMemory(value)
		}
		return n
	case []requestObject:
		n := sliceMemory[requestObject](len(v))
		for _, value := range v {
			n += structureMemory(value)
		}
		return n
	case []jsontext.Value:
		n := sliceMemory[jsontext.Value](len(v))
		for _, value := range v {
			n += structureMemory(value)
		}
		return n
	case []any:
		n := sliceMemory[any](len(v))
		for _, value := range v {
			n += structureMemory(value)
		}
		return n
	}
	return 0
}

func sliceMemory[T any](n int) int64 {
	return rounded(int64(n) * int64(unsafe.Sizeof(*new(T))))
}

func listSize(n int, element func(int) int64) int64 {
	if n == 0 {
		return 2
	}
	size := int64(2 + n - 1)
	for i := range n {
		size += element(i)
	}
	return size
}

// unmarshalWithin decodes a small value of a request, drawing its allocation from b first.
func unmarshalWithin(v []byte, dst any, b *budget) error {
	if err := b.draw(int64(len(v)) + 512); err != nil {
		return err
	}
	return jsonx.Unmarshal(v, dst)
}
