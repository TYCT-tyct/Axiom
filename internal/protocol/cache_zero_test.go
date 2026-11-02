package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // the zeroing walks the value it is given, not a second parser
	"testing"
)

// TestZeroJSONNumbersKeepsTheShapeOfUsage: every number in a usage member becomes zero, including empty and non-empty
// arrays and nested objects. The replay keeps the upstream's members, so a client sees the same shape it would from a
// live answer.
func TestZeroJSONNumbersKeepsTheShapeOfUsage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, in, want string
	}{
		{"number", "12", "0"},
		{"empty array", "[]", "[]"},
		{"numbers", "[1,2]", "[0,0]"},
		{"objects", `[{"input_tokens":3,"detail":{"cache_read":4}},{"ok":true}]`, `[{"input_tokens":0,"detail":{"cache_read":0}},{"ok":true}]`},
		{"empty object", "{}", "{}"},
		{"nested object", `{"ephemeral_5m_input_tokens":12,"detail":{"a":1,"b":[]}}`, `{"ephemeral_5m_input_tokens":0,"detail":{"a":0,"b":[]}}`},
		{"strings and booleans", `{"tier":"standard","ok":true,"n":null}`, `{"tier":"standard","ok":true,"n":null}`},
	} {
		if got := string(zeroJSONNumbers(jsontext.Value(tc.in))); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}
