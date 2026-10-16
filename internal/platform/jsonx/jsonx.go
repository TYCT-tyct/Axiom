// Package jsonx holds the project's JSON policy, and depguard keeps encoding/json out of other packages so the policy
// is set in one place: input is strict RFC 7493 (valid UTF-8, unique member names), which is encoding/json/v2's
// default, and nests at most MaxDepth levels. Duplicate names are refused because two parsers may each keep a
// different copy: the usage we bill would not be the usage the client reads.
package jsonx

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
)

// MaxDepth is the deepest nesting of objects and arrays accepted. jsontext's own limit is a fixed 10000 with no
// option to lower it (WithDepthLimit is not implemented as of Go 1.27). 256 is far beyond any request or event the
// three call formats produce, and bounds the recursion of any code, ours or a library's, that walks a value.
const MaxDepth = 256

// Raw is a JSON value kept as written, for a field a module stores without interpreting it. Unmarshal checks it
// like any other input; a module that keeps it canonical passes it through MarshalCanonical.
type Raw = jsontext.Value

var (
	errTooDeep  = fmt.Errorf("jsonx: nesting deeper than %d levels", MaxDepth)
	errEmpty    = errors.New("jsonx: no JSON value")
	errTrailing = errors.New("jsonx: data after the JSON value")
)

// keepMax is the largest document a Validator keeps its decoder after: what a decoder retains between documents
// (its names and nesting stacks) grows with the document it read, so a Validator that outlives a stream's large event
// would hold what the event needed. 4 KiB covers the events streams are made of; a larger document, whose cost
// dwarfs a decoder's, gets a decoder of its own.
const keepMax = 4 << 10

// Validator is Validate for a caller that validates many documents one after another, such as the events of a stream:
// it keeps the decoder instead of building one each time. The zero value is ready; it is not safe for concurrent use.
type Validator struct {
	buf bytes.Buffer
	dec *jsontext.Decoder
}

// Validate returns nil when data is exactly one JSON value, optionally surrounded by whitespace, that satisfies the
// policy in the package comment.
func Validate(data []byte) error {
	// A bytes.Buffer lets the decoder parse data in place instead of copying it.
	return validate(jsontext.NewDecoder(bytes.NewBuffer(data)))
}

// Validate is the function Validate, reusing the decoder of the documents before.
func (v *Validator) Validate(data []byte) error {
	v.buf = *bytes.NewBuffer(data)
	if v.dec == nil {
		v.dec = jsontext.NewDecoder(&v.buf)
	} else {
		v.dec.Reset(&v.buf)
	}
	err := validate(v.dec)
	v.buf = bytes.Buffer{} // neither the buffer nor the decoder may keep data alive
	if len(data) > keepMax {
		v.dec = nil
	} else {
		v.dec.Reset(&v.buf)
	}
	return err
}

func validate(dec *jsontext.Decoder) error {
	for {
		if _, err := dec.ReadToken(); err != nil {
			if errors.Is(err, io.EOF) {
				return errEmpty
			}
			return err
		}
		if depth := dec.StackDepth(); depth > MaxDepth {
			return errTooDeep
		} else if depth == 0 {
			break
		}
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errTrailing
		}
		return err
	}
	return nil
}

// Unmarshal decodes data into v when Validate accepts it. It reads data twice, once to enforce MaxDepth, which
// json.Unmarshal cannot, and once to decode, whose defaults are the rest of the policy. Members v does not name are
// skipped, and names match exactly, case included.
func Unmarshal(data []byte, v any) error {
	return unmarshal(data, v)
}

// Decode reads r to its end and unmarshals it into v as Unmarshal does, for the request bodies that generated handlers
// decode. A body with nothing in it is io.EOF, as encoding/json's decoder reports it. The caller bounds how much r
// can give.
func Decode(r io.Reader, v any) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return io.EOF
	}
	return Unmarshal(data, v)
}

// UnmarshalKnown is Unmarshal that refuses members v does not name, for stored forms we define ourselves: there a
// member we do not know is a condition we cannot honour, and skipping it would silently change the meaning.
func UnmarshalKnown(data []byte, v any) error {
	return unmarshal(data, v, json.RejectUnknownMembers(true))
}

func unmarshal(data []byte, v any, opts ...json.Options) error {
	if err := Validate(data); err != nil {
		return err
	}
	return json.Unmarshal(data, v, opts...)
}

// MarshalCanonical encodes v in RFC 8785 member order and spelling, so equal values give equal bytes. Integers stay as
// written instead of passing through float64 as RFC 8785 would have them, because a count or threshold above 2⁵³
// would otherwise change.
func MarshalCanonical(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	t := jsontext.Value(b)
	// Canonicalize may replace t's backing array, so t is read only after it returns.
	if err := t.Canonicalize(jsontext.CanonicalizeRawInts(false)); err != nil {
		return nil, err
	}
	return t, nil
}
