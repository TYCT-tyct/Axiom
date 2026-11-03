package declared

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// segment is one step of a field path: a member name, or an index into an array.
type segment struct {
	name  string
	index int
	array bool
}

// path is a field path in the declaration: members separated by dots, an array index written in brackets. It is the
// only way a declaration names a value in an answer, so the syntax is checked once at load and never guessed at a
// call: a path that cannot be read is a broken declaration, not an empty answer.
type path []segment

// parsePath reads the syntax of a field path. A member is a run of letters, digits, underscores and hyphens; a path has
// at most 16 steps, which is far beyond the few an answer needs and keeps a hostile declaration from deep recursion.
func parsePath(text string) (path, error) {
	if text == "" {
		return nil, errors.New("a field path is empty")
	}
	var p path
	for step := range strings.SplitSeq(text, ".") {
		if step == "" {
			return nil, fmt.Errorf("the field path %q has an empty step", text)
		}
		var err error
		if p, err = parseStep(p, step, text); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// parseStep reads one dot-separated step: a member name, then any number of array indexes.
func parseStep(p path, step, text string) (path, error) {
	if len(p) >= 16 {
		return nil, fmt.Errorf("the field path %q is deeper than 16 steps", text)
	}
	open := strings.IndexByte(step, '[')
	if open < 0 {
		if !memberName(step) {
			return nil, fmt.Errorf("the field path %q has a step %q that is not a member name", text, step)
		}
		return append(p, segment{name: step}), nil
	}
	name := step[:open]
	if !memberName(name) {
		return nil, fmt.Errorf("the field path %q has a step %q that is not a member name", text, name)
	}
	p = append(p, segment{name: name})
	rest := step[open:]
	for rest != "" {
		if rest[0] != '[' {
			return nil, fmt.Errorf("the field path %q has text %q after an index", text, rest)
		}
		close := strings.IndexByte(rest, ']')
		if close < 0 {
			return nil, fmt.Errorf("the field path %q has an index that does not close", text)
		}
		n, err := strconv.Atoi(rest[1:close])
		if err != nil || n < 0 || !allDigits(rest[1:close]) {
			return nil, fmt.Errorf("the field path %q has an index %q that is not a non-negative number", text, rest[1:close])
		}
		if len(p) >= 16 {
			return nil, fmt.Errorf("the field path %q is deeper than 16 steps", text)
		}
		p = append(p, segment{index: n, array: true})
		rest = rest[close+1:]
	}
	return p, nil
}

func memberName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// lookup reads the value at p. A missing member, an index out of range and a shape that is not an object or an array
// are all "not there": the caller decides whether that is an unrecognised answer.
func (p path) lookup(raw jsonx.Raw) (jsonx.Raw, bool) {
	for _, s := range p {
		if len(raw) == 0 {
			return nil, false
		}
		if s.array {
			var items []jsonx.Raw
			if jsonx.Unmarshal(raw, &items) != nil || s.index >= len(items) {
				return nil, false
			}
			raw = items[s.index]
			continue
		}
		var object map[string]jsonx.Raw
		if jsonx.Unmarshal(raw, &object) != nil {
			return nil, false
		}
		value, ok := object[s.name]
		if !ok {
			return nil, false
		}
		raw = value
	}
	return raw, true
}

// readText reads a string or a number as text. A boolean, an object and an array are not text, and neither is null.
func readText(raw jsonx.Raw) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	if raw[0] == '"' {
		var s string
		if jsonx.Unmarshal(raw, &s) != nil {
			return "", false
		}
		return s, true
	}
	if raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9' {
		return string(raw), true
	}
	return "", false
}

// readAmount reads a decimal amount without passing it through a float: a number as written, or a string of decimal
// digits. An exponent is refused because the supplier's precision is part of the amount and a rounded cost is not the
// cost (adapter.Amount).
func readAmount(raw jsonx.Raw) (adapter.Amount, bool) {
	text, ok := readText(raw)
	if !ok || !decimal(text) {
		return "", false
	}
	return adapter.Amount(text), true
}

// readBalance reads an account balance. Unlike a charge amount it may be negative: an overdrawn account is a fact
// routing needs, not an unrecognised answer.
func readBalance(raw jsonx.Raw) (adapter.Amount, bool) {
	text, ok := readText(raw)
	if !ok {
		return "", false
	}
	digits := strings.TrimPrefix(text, "-")
	if !decimal(digits) {
		return "", false
	}
	return adapter.Amount(text), true
}

func decimal(s string) bool {
	whole, frac, point := strings.Cut(s, ".")
	if !allDigits(whole) {
		return false
	}
	return !point || allDigits(frac)
}

// readArray reads a required list. JSON null decodes into a nil slice without an error, but a null answer is not an
// empty list: taking it for one would withdraw every row of a projection, or end a charge scan and advance its
// watermark. Only [] is an empty list.
func readArray(raw jsonx.Raw, what string) ([]jsonx.Raw, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("%w: the %s are null, not an array", adapter.ErrUnrecognised, what)
	}
	var items []jsonx.Raw
	if err := jsonx.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%w: the %s are not an array", adapter.ErrUnrecognised, what)
	}
	return items, nil
}

// readInteger reads a non-negative integer from a number or a string. Counts and ids are never floats.
func readInteger(raw jsonx.Raw) (int64, bool) {
	text, ok := readText(raw)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(text, 10, 64)
	return n, err == nil && n >= 0
}

// readBool reads a JSON boolean.
func readBool(raw jsonx.Raw) (bool, bool) {
	if string(raw) == "true" {
		return true, true
	}
	if string(raw) == "false" {
		return false, true
	}
	return false, false
}

// readTime reads a time the two ways a relay states one: RFC 3339 text, or Unix seconds as a number or a string.
func readTime(raw jsonx.Raw) (time.Time, bool) {
	if text, ok := readText(raw); ok {
		if t, err := time.Parse(time.RFC3339, text); err == nil {
			return t.UTC(), true
		}
	}
	n, ok := readInteger(raw)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(n, 0).UTC(), true
}

// missing is the error of a successful answer that does not hold a value the declaration says it holds. It wraps
// ErrUnrecognised: the task that read it stops and nothing it read is written, rather than taking the absence for an
// empty or zero value (adapter.ErrUnrecognised).
func missing(what string) error {
	return fmt.Errorf("%w: the answer has no %s", adapter.ErrUnrecognised, what)
}
