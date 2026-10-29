package protocol

import (
	"errors"
	"strings"
	"testing"
)

func TestPluginControls(t *testing.T) {
	for _, tc := range []struct {
		members     string
		compression Compression
		healing     bool
		bad         string
	}{
		{`"plugins":[{"id":"response-healing"},{"id":"context-compression"}]`, CompressionEnabled, true, ""},
		{`"transforms":["middle-out"]`, CompressionEnabled, false, ""},
		{`"transforms":["middle\u002dout"]`, CompressionEnabled, false, ""},
		{`"plugins":[{"\u0069d":"response\u002dhealing"}]`, CompressionDefault, true, ""},
		{`"plugins":[{"id":"context-compression","enabled":false}],"transforms":["middle-out"]`, CompressionDisabled, false, ""},
		{`"transforms":[]`, CompressionDisabled, false, ""},
		{`"transforms":["middle-out"],"plugins":[{"id":"context-compression","enabled":false}]`, CompressionDisabled, false, ""},
		{`"plugins":[{"enabled":false,"id":"web"}]`, CompressionDefault, false, ""},
		{`"plugins":[{"id":"file-parser"}]`, 0, false, "not supported"},
		{`"plugins":[{"id":"web"}]`, 0, false, "not supported"},
		{`"plugins":[{"id":"unknown","enabled":false}]`, 0, false, "not supported"},
		{`"plugins":[{"id":"response-healing","oops":true}]`, 0, false, "not supported"},
		{`"plugins":[null]`, 0, false, "plugin object"},
		{`"plugins":[{}]`, 0, false, "not supported"},
		{`"transforms":["unknown"]`, 0, false, "not supported"},
		{`"plugins":true`, 0, false, "array"},
		{`"plugins":null,"transforms":null`, CompressionDefault, false, ""},
	} {
		t.Run(tc.members, func(t *testing.T) {
			r, err := Inspect(Chat, []byte(`{"model":"a",`+tc.members+`}`))
			if tc.bad != "" {
				if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.bad) {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err != nil || r.Compression != tc.compression || r.ResponseHealing != tc.healing {
				t.Fatalf("got %+v, %v", r, err)
			}
		})
	}
}

func TestPluginFormatBoundary(t *testing.T) {
	for _, format := range []Format{Messages, Responses} {
		_, err := Inspect(format, []byte(`{"model":"a","plugins":[{"id":"response-healing"}]}`))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("format %d: %v", format, err)
		}
		_, err = Inspect(format, []byte(`{"model":"a","plugins":[{"id":"response-healing","enabled":false}],"transforms":[]}`))
		if err != nil {
			t.Fatal(err)
		}
	}
}
