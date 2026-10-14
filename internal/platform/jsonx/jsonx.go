// Package jsonx holds the project's JSON policy, and depguard keeps encoding/json out of other packages so the policy
// is set in one place: input is strict RFC 7493 (valid UTF-8, unique member names), which is encoding/json/v2's
// default, and nests at most MaxDepth levels. Duplicate names are refused because two parsers may each keep a
// different copy: the usage we bill would not be the usage the client reads.
package jsonx

import (
	"bytes"
	"encoding/json/jsontext"
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
