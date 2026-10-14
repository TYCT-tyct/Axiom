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

var (
	errTooDeep  = fmt.Errorf("jsonx: nesting deeper than %d levels", MaxDepth)
	errEmpty    = errors.New("jsonx: no JSON value")
	errTrailing = errors.New("jsonx: data after the JSON value")
)

// Validate returns nil when data is exactly one JSON value, optionally surrounded by whitespace, that satisfies the
// policy in the package comment.
func Validate(data []byte) error {
	// A bytes.Buffer lets the decoder parse data in place instead of copying it.
	dec := jsontext.NewDecoder(bytes.NewBuffer(data))
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
	if err := Validate(data); err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// UnmarshalStrict decodes b into v and refuses invalid UTF-8, duplicate member names and members v has no field for:
// what we decode is what any other strict reader would decode, and a member we do not know is an error rather than
// silently ignored.
func UnmarshalStrict(b []byte, v any) error {
	return json.Unmarshal(b, v, json.RejectUnknownMembers(true))
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
	return t, t.Canonicalize(jsontext.CanonicalizeRawInts(false))
}
