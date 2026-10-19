package mail

import (
	"bytes"
	"embed"
	"errors"
	"flag"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	netmail "net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

//go:embed testdata
var samples embed.FS

var update = flag.Bool("update", false, "rewrite the golden messages in testdata")

// goldenDate is fixed so the Date header of a golden message does not depend on the clock.
var goldenDate = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func TestMessageBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		c    compose
	}{
		{
			name: "text-and-html",
			c: compose{
				from:    netmail.Address{Name: "Axiom", Address: "no-reply@axiom.example"},
				to:      netmail.Address{Address: "user@example.com"},
				subject: "Welcome",
				text:    "Hello from Axiom.",
				html:    "<p>Hello from Axiom.</p>",
				date:    goldenDate, messageID: "<golden@axiom.example>", boundary: "axiom-golden",
			},
		},
		{
			name: "utf8",
			c: compose{
				from:    netmail.Address{Address: "no-reply@axiom.example"},
				to:      netmail.Address{Name: "Test User", Address: "user@example.com"},
				subject: "验证邮件",
				text:    "验证码 123456",
				html:    "<p>验证码 123456</p>",
				date:    goldenDate, messageID: "<utf8@axiom.example>", boundary: "axiom-utf8",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := c.c.bytes()
			assertWireFormat(t, got)
			golden(t, c.name+".eml", got)
		})
	}
}

// TestMessageLongSubject: a subject longer than one encoded word is split into several, each on a folded line, and a
// reader joins them back; the wire stays within the line limit (RFC 2047 section 2, RFC 5322 section 2.1.1).
func TestMessageLongSubject(t *testing.T) {
	t.Parallel()
	subject := strings.Repeat("模型切换通知 ", 12)
	c := compose{
		from:    netmail.Address{Name: "Axiom", Address: "no-reply@axiom.example"},
		to:      netmail.Address{Address: "user@example.com"},
		subject: subject, text: "Hello", html: "<p>Hello</p>",
		date: goldenDate, messageID: "<long@axiom.example>", boundary: "axiom-long",
	}
	payload := c.bytes()
	assertWireFormat(t, payload)
	msg, err := netmail.ReadMessage(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || decoded != subject {
		t.Errorf("subject decodes to %q, %v; want the subject back", decoded, err)
	}
}

// TestMessageParses reads a message built from wrapping bodies the way a receiving mail library would: CRLF line
// endings and soft line breaks must not change what the reader sees.
func TestMessageParses(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("验证码 123456 ", 20)
	html := "<p>" + text + "</p>"
	c := compose{
		from:    netmail.Address{Name: "Axiom", Address: "no-reply@axiom.example"},
		to:      netmail.Address{Name: "Test User", Address: "user@example.com"},
		subject: "验证邮件",
		text:    text, html: html,
		date: goldenDate, messageID: "<parse@axiom.example>", boundary: "axiom-parse",
	}
	payload := c.bytes()
	assertWireFormat(t, payload)

	msg, err := netmail.ReadMessage(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || subject != c.subject {
		t.Errorf("subject = %q, %v; want %q", subject, err, c.subject)
	}
	if got := msg.Header.Get("From"); got != c.from.String() {
		t.Errorf("From = %q; want %q", got, c.from.String())
	}
	if _, err := netmail.ParseDate(msg.Header.Get("Date")); err != nil {
		t.Errorf("Date header = %q: %v", msg.Header.Get("Date"), err)
	}
	if got := msg.Header.Get("Message-Id"); got != c.messageID {
		t.Errorf("Message-Id = %q; want %q", got, c.messageID)
	}
	if text, html := bodyParts(t, payload); text != c.text || html != c.html {
		t.Errorf("bodies = %q, %q; want %q, %q", text, html, c.text, c.html)
	}
}

// bodyParts decodes both alternatives of a message the way a receiving library does, so a test can compare what a
// reader would show rather than the encoded bytes.
func bodyParts(t *testing.T, payload []byte) (text, html string) {
	t.Helper()
	msg, err := netmail.ReadMessage(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type = %q, %v; want multipart/alternative", msg.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(msg.Body, params["boundary"])
	var bodies []string
	for _, want := range []string{"text/plain; charset=UTF-8", "text/html; charset=UTF-8"} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatalf("NextPart() error = %v", err)
		}
		if got := part.Header.Get("Content-Type"); got != want {
			t.Errorf("part Content-Type = %q; want %q", got, want)
		}
		// multipart decodes quoted-printable itself, so this is what a reader gets.
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		bodies = append(bodies, string(body))
	}
	if part, err := reader.NextPart(); !errors.Is(err, io.EOF) {
		t.Errorf("NextPart() = %v, %v; want io.EOF after two alternatives", part, err)
	}
	return bodies[0], bodies[1]
}

func TestMessageValidation(t *testing.T) {
	t.Parallel()
	valid := Message{To: "user@example.com", Subject: "Welcome", Text: "Hello", HTML: "<p>Hello</p>"}
	cases := []struct {
		name string
		edit func(*Message)
	}{
		{name: "an empty subject", edit: func(m *Message) { m.Subject = "" }},
		{name: "a subject of spaces", edit: func(m *Message) { m.Subject = " \t" }},
		{name: "a subject spanning lines", edit: func(m *Message) { m.Subject = "Welcome\r\nBcc: other@example.com" }},
		{name: "no text body", edit: func(m *Message) { m.Text = "" }},
		{name: "no HTML body", edit: func(m *Message) { m.HTML = "" }},
		{name: "no recipient", edit: func(m *Message) { m.To = "" }},
		{name: "a recipient that is not an address", edit: func(m *Message) { m.To = "planted-recipient" }},
		{name: "a recipient spanning lines", edit: func(m *Message) { m.To = "user@example.com\r\nBcc: other@example.com" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			msg := valid
			c.edit(&msg)
			if _, err := msg.validate(); !errors.Is(err, ErrInvalidMessage) {
				t.Errorf("validate() error = %v; want ErrInvalidMessage", err)
			}
		})
	}
	if _, err := valid.validate(); err != nil {
		t.Errorf("validate() of a valid message = %v", err)
	}
}

func TestNewMessageID(t *testing.T) {
	t.Parallel()
	id := newMessageID("no-reply@axiom.example")
	if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@axiom.example>") {
		t.Errorf("newMessageID() = %q; want an id at axiom.example", id)
	}
	if other := newMessageID("no-reply@axiom.example"); other == id {
		t.Errorf("two messages got the same id %q", id)
	}
	if id := newMessageID("planted"); !strings.HasSuffix(id, "@localhost>") {
		t.Errorf("newMessageID() of an address without a domain = %q; want the localhost fallback", id)
	}
}

// assertWireFormat checks what SMTP requires of the bytes: every line break is CRLF and every line fits RFC 5321's
// limit, so no relay has to guess where a line ends.
func assertWireFormat(t *testing.T, payload []byte) {
	t.Helper()
	for i, b := range payload {
		if b == '\n' && (i == 0 || payload[i-1] != '\r') {
			t.Errorf("byte %d is a bare LF", i)
		}
		if b == '\r' && (i+1 == len(payload) || payload[i+1] != '\n') {
			t.Errorf("byte %d is a bare CR", i)
		}
	}
	for i, line := range bytes.Split(payload, []byte("\r\n")) {
		if len(line) > 998 {
			t.Errorf("line %d is %d bytes; want at most 998", i+1, len(line))
		}
	}
}

// golden compares got with the golden message at name, which go test -update rewrites. The file is stored with LF so
// git and editors agree on its bytes; the message on the wire uses CRLF.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	if *update {
		stored := bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n"))
		if err := os.WriteFile(filepath.Join("testdata", name), stored, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := fs.ReadFile(samples, "testdata/"+name)
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	want = bytes.ReplaceAll(want, []byte("\n"), []byte("\r\n"))
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("%s (-golden +got):\n%s", name, diff)
	}
}
