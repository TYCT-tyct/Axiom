package observability

import (
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"
)

// TestCursorRoundTrip: a cursor read back is the place it was made at, to the microsecond the column keeps, and its
// text is short and URL-safe.
func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()
	for _, at := range []time.Time{
		time.Date(2026, 10, 16, 12, 30, 45, 123456000, time.UTC),
		time.Date(1970, 1, 1, 0, 0, 0, 1000, time.UTC),
		time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 16, 20, 30, 45, 5000, time.FixedZone("UTC+8", 8*3600)),
	} {
		in := Cursor{At: at, ID: uuid.NewV7()}
		text := in.String()
		out, err := ParseCursor(text)
		if err != nil || !out.At.Equal(in.At) || out.ID != in.ID || len(text) != 32 || strings.ContainsAny(text, "+/=") {
			t.Errorf("%v: %q read back as %+v, %v", in, text, out, err)
		}
	}
}

// TestParseCursorRefusals: text that is not a cursor this module gave out is ErrBadCursor, however it is wrong.
func TestParseCursorRefusals(t *testing.T) {
	t.Parallel()
	good := Cursor{At: time.Now(), ID: uuid.NewV7()}.String()
	for name, text := range map[string]string{
		"empty":      "",
		"too short":  good[:len(good)-4],
		"too long":   good + "AAAA",
		"not base64": strings.Repeat("!", 32),
		"padded":     good + "=",
		"standard":   strings.Repeat("+", 32),
		"a key's ID": "key_01hxyz0000000000000000000a",
		"whitespace": " " + good,
	} {
		if _, err := ParseCursor(text); !errors.Is(err, ErrBadCursor) {
			t.Errorf("%s: %v, want %v", name, err, ErrBadCursor)
		}
	}
}
