package protocol_test

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// TestText: the request's text is the body without an image's or a file's own data, in the order it appears, and it is
// the bytes Request.TextBytes counts; the data is left out whichever member carries it and in whichever format.
func TestText(t *testing.T) {
	t.Parallel()
	const image = `"data:image/png;base64,QUJD"`
	for _, tc := range []struct {
		name   string
		format protocol.Format
		body   string
		data   string // the data left out of the text
	}{
		{
			"a message is all text", protocol.Messages,
			`{"model":"m","messages":[{"role":"user","content":"hi"}]}`, "",
		},
		{
			"an image's data", protocol.Messages,
			`{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}},{"type":"text","text":"hi"}]}]}`,
			`"QUJD"`,
		},
		{
			"a document's data, before its type", protocol.Messages,
			`{"model":"m","messages":[{"role":"user","content":[{"type":"document","source":{"data":"REVG","media_type":"application/pdf","type":"base64"}}]}]}`,
			`"REVG"`,
		},
		{
			"an image's data URL", protocol.Chat,
			`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":` + image + `}},{"type":"text","text":"hi"}]}]}`,
			image,
		},
		{
			"a chat file's data", protocol.Chat,
			`{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"data:application/pdf;base64,REVG"}}]}]}`,
			`"data:application/pdf;base64,REVG"`,
		},
		{
			"an input image's data URL", protocol.Responses,
			`{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":` + image + `}]}]}`,
			image,
		},
		{
			"an input file's data", protocol.Responses,
			`{"model":"m","input":[{"role":"user","content":[{"type":"input_file","file_data":"REVG"}]}]}`,
			`"REVG"`,
		},
		{
			"a URL that carries no data", protocol.Responses,
			`{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`,
			"",
		},
		{
			"the data of two parts", protocol.Messages,
			`{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"QUJD"}},{"type":"image","source":{"type":"base64","data":"REVG"}}]}]}`,
			"QUJD",
		},
	} {
		req, err := protocol.Inspect(tc.format, []byte(tc.body))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var pieces []string
		if err := protocol.Text(tc.format, []byte(tc.body), func(piece []byte) bool {
			pieces = append(pieces, string(piece))
			return true
		}); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		text := strings.Join(pieces, "")
		if int64(len(text)) != req.TextBytes {
			t.Errorf("%s: the text is %d bytes, the request counts %d", tc.name, len(text), req.TextBytes)
		}
		if tc.data == "" && text != tc.body {
			t.Errorf("%s: the text is %q, want the body", tc.name, text)
		}
		if tc.data != "" && strings.Contains(text, tc.data) {
			t.Errorf("%s: the text holds the data %s", tc.name, tc.data)
		}
	}
}

// TestTextStops: the walk hands out no more text once the caller has what it came for, and a body Inspect refuses is
// refused here too.
func TestTextStops(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"QUJD"}},{"type":"text","text":"hi"}]}]}`)
	var pieces int
	if err := protocol.Text(protocol.Messages, body, func([]byte) bool {
		pieces++
		return false
	}); err != nil {
		t.Fatal(err)
	}
	if pieces != 1 {
		t.Errorf("took %d pieces after the first was refused, want 1", pieces)
	}
	if err := protocol.Text(protocol.Messages, []byte(`{"model":`), func([]byte) bool { return true }); !errors.Is(err, protocol.ErrInvalid) {
		t.Errorf("a body cut short gave %v, want ErrInvalid", err)
	}
}

// checkText holds Text to the bytes TextBytes counts, for a body Inspect accepted: every one of those walks, and its
// pieces are the body less the image and file data.
func checkTextBytes(t *testing.T, f protocol.Format, body []byte, want int64) {
	t.Helper()
	var text int64
	if err := protocol.Text(f, body, func(piece []byte) bool {
		text += int64(len(piece))
		return true
	}); err != nil {
		t.Fatalf("the text of a body Inspect accepted: %v", err)
	}
	if text != want {
		t.Fatalf("the text is %d bytes, TextBytes says %d", text, want)
	}
}
