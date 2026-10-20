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
			got, err := c.c.bytes()
			if err != nil {
				t.Fatalf("bytes() error = %v", err)
			}
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
	payload, err := c.bytes()
	if err != nil {
		t.Fatalf("bytes() error = %v", err)
	}
	assertWireFormat(t, payload)
	msg, err := netmail.ReadMessage(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || decoded != subject {
		t.Errorf("subject decodes to %q, %v; want the subject back", decoded, err)
	}
	// Every line of the header section is folded at the recommended width, not merely under the hard limit.
	maxLine(t, payload, foldAt)
}

// TestMessageLongDisplayNames: a display name too long for one line is encoded and folded like a subject, and the
// recipient's library gives back the name the caller wrote.
func TestMessageLongDisplayNames(t *testing.T) {
	t.Parallel()
	name := strings.Repeat("运维通知", 20)
	c := compose{
		from:    netmail.Address{Name: name, Address: "no-reply@axiom.example"},
		to:      netmail.Address{Name: name, Address: "user@example.com"},
		subject: "Welcome", text: "Hello", html: "<p>Hello</p>",
		date: goldenDate, messageID: "<names@axiom.example>", boundary: "axiom-names",
	}
	payload, err := c.bytes()
	if err != nil {
		t.Fatalf("bytes() error = %v", err)
	}
	assertWireFormat(t, payload)
	maxLine(t, payload, foldAt)
	msg, err := netmail.ReadMessage(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	for _, field := range []string{"From", "To"} {
		list, err := msg.Header.AddressList(field)
		if err != nil || len(list) != 1 {
			t.Fatalf("%s = %v, %v; want one address", field, list, err)
		}
		if list[0].Name != name {
			t.Errorf("%s name = %q; want the name back", field, list[0].Name)
		}
	}
}

// TestMessageLongASCII: a plain-ASCII value that fits no single line is folded at its spaces and still reads as written;
// one with nowhere to fold is refused, because no header line may pass 998 characters.
func TestMessageLongASCII(t *testing.T) {
	t.Parallel()
	c := compose{
		from:    netmail.Address{Name: "Axiom", Address: "no-reply@axiom.example"},
		to:      netmail.Address{Address: "user@example.com"},
		subject: strings.Repeat("word ", 400), text: "Hello", html: "<p>Hello</p>",
		date: goldenDate, messageID: "<ascii@axiom.example>", boundary: "axiom-ascii",
	}
	payload, err := c.bytes()
	if err != nil {
		t.Fatalf("bytes() error = %v", err)
	}
	assertWireFormat(t, payload)
	maxLine(t, payload, foldAt)
	msg, err := netmail.ReadMessage(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	if got := strings.TrimSuffix(msg.Header.Get("Subject"), " "); got != strings.TrimSuffix(c.subject, " ") {
		t.Errorf("subject is %d bytes; want the %d bytes written", len(got), len(c.subject))
	}

	unbreakable := c
	unbreakable.subject = strings.Repeat("x", 1000)
	if _, err := unbreakable.bytes(); !errors.Is(err, ErrInvalidMessage) {
		t.Errorf("bytes() of a 1000-byte word error = %v; want ErrInvalidMessage", err)
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
	payload, err := c.bytes()
	if err != nil {
		t.Fatalf("bytes() error = %v", err)
	}
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
		name  string
		edit  func(*Message)
		quiet string // a value the refusal must not quote
	}{
		{name: "an empty subject", edit: func(m *Message) { m.Subject = "" }},
		{name: "a subject of spaces", edit: func(m *Message) { m.Subject = " \t" }},
		{name: "a subject spanning lines", edit: func(m *Message) { m.Subject = "Welcome\r\nBcc: other@example.com" }},
		{name: "a subject holding a control character", edit: func(m *Message) { m.Subject = "Welcome\x00" }},
		{name: "no text body", edit: func(m *Message) { m.Text = "" }},
		{name: "no HTML body", edit: func(m *Message) { m.HTML = "" }},
		{name: "no recipient", edit: func(m *Message) { m.To = "" }},
		{
			name: "a recipient that is not an address", edit: func(m *Message) { m.To = "planted-recipient" },
			quiet: "planted-recipient",
		},
		{
			name:  "a recipient spanning lines",
			edit:  func(m *Message) { m.To = "user@example.com\r\nBcc: other@example.com" },
			quiet: "Bcc: other@example.com",
		},
		// What the envelope cannot carry, and what no header line can: the send would refuse both, so the enqueue does.
		{
			name: "a recipient only SMTPUTF8 could carry", edit: func(m *Message) { m.To = "用户@example.com" },
			quiet: "用户@example.com",
		},
		{
			name: "a recipient past the path limit", edit: func(m *Message) { m.To = strings.Repeat("x", 250) + "@example.com" },
		},
		{name: "a subject no header line can carry", edit: func(m *Message) { m.Subject = strings.Repeat("x", 1000) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			msg := valid
			c.edit(&msg)
			_, err := msg.validate()
			if !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("validate() error = %v; want ErrInvalidMessage", err)
			}
			// The refusal names the fault and never the value: it reaches a caller, a log line and, from a job, a table
			// no tenant policy covers.
			if c.quiet != "" && strings.Contains(err.Error(), c.quiet) {
				t.Errorf("validate() error = %q; it quotes the value", err)
			}
			if strings.Contains(err.Error(), msg.To) && msg.To != "" {
				t.Errorf("validate() error = %q; it quotes the recipient", err)
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
	// The domain comes from the address even when its local part holds one of its own.
	if got := newMessageID(`"post"@"no-reply"@axiom.example`); !strings.HasSuffix(got, "@axiom.example>") {
		t.Errorf("newMessageID() = %q; want an id at axiom.example", got)
	}
	if other := newMessageID("no-reply@axiom.example"); other == id {
		t.Errorf("two messages got the same id %q", id)
	}
}

// TestEncodedWords: the encoder matches the standard library byte for byte, so a message this package renders is the
// message mime.QEncoding would have rendered, and every word keeps within the 75-character limit.
func TestEncodedWords(t *testing.T) {
	t.Parallel()
	cases := []string{
		"验证邮件",
		strings.Repeat("模型切换通知 ", 12),
		"Hello, 世界! =?UTF-8?q?not-a-word?=",
		"tab\tand a very long tail " + strings.Repeat("的话", 40),
	}
	for _, value := range cases {
		got := strings.Join(encodeWords(value), " ")
		if want := mime.QEncoding.Encode("UTF-8", value); got != want {
			t.Errorf("encodeWords(%q) = %q; want %q", value, got, want)
		}
		for _, word := range encodeWords(value) {
			if len(word) > 75 {
				t.Errorf("encodeWords(%q) produced %d characters; want at most 75", value, len(word))
			}
		}
	}
}

// TestMailbox: the envelope path is what the server routes on, so it is quoted and escaped, never truncated.
func TestMailbox(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		addr netmail.Address
		want string
	}{
		{name: "a dot-atom", addr: netmail.Address{Address: "user.name+tag@example.com"}, want: "user.name+tag@example.com"},
		{name: "a space", addr: netmail.Address{Address: "a b@example.com"}, want: `"a b"@example.com`},
		{name: "a quote and a backslash", addr: netmail.Address{Address: `a"b\c@example.com`}, want: `"a\"b\\c"@example.com`},
		{name: "an at sign inside the local part", addr: netmail.Address{Address: "a@b@example.com"}, want: `"a@b"@example.com`},
		{name: "a domain literal", addr: netmail.Address{Address: "user@[192.0.2.1]"}, want: "user@[192.0.2.1]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := mailbox(c.addr)
			if err != nil {
				t.Fatalf("mailbox() error = %v", err)
			}
			if got != c.want {
				t.Errorf("mailbox() = %q; want %q", got, c.want)
			}
			// What the server parses back is the address the caller gave.
			if back, err := netmail.ParseAddress(got); err != nil || back.Address != c.addr.Address {
				t.Errorf("ParseAddress(%q) = %v, %v; want %q", got, back, err, c.addr.Address)
			}
		})
	}
}

// TestMailboxRefusals: what the envelope cannot carry is refused, and the refusal never quotes the address.
func TestMailboxRefusals(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		addr netmail.Address
	}{
		{name: "no domain", addr: netmail.Address{Address: "planted"}},
		{name: "a local part only SMTPUTF8 carries", addr: netmail.Address{Address: "用户@example.com"}},
		{name: "a domain only SMTPUTF8 carries", addr: netmail.Address{Address: "user@例え.example"}},
		{name: "a path past 256 octets", addr: netmail.Address{Address: strings.Repeat("x", 250) + "@example.com"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := mailbox(c.addr)
			if !errors.Is(err, ErrInvalidMessage) {
				t.Errorf("mailbox() = %q, %v; want ErrInvalidMessage", got, err)
			}
			// The refusal is handed to a caller and, through a job, to a row no tenant policy covers: it names the
			// fault and never the address.
			if strings.Contains(err.Error(), c.addr.Address) {
				t.Errorf("mailbox() error = %q; it quotes the address", err)
			}
		})
	}
}

// TestAddressHeader: a display name is quoted when it is plain, encoded when it is not, and either way the mailbox it
// wraps is the one the envelope is given.
func TestAddressHeader(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		addr netmail.Address
		want string
	}{
		{name: "no name", addr: netmail.Address{Address: "user@example.com"}, want: "<user@example.com>"},
		{name: "a plain name", addr: netmail.Address{Name: "Axiom", Address: "no-reply@axiom.example"}, want: `"Axiom" <no-reply@axiom.example>`},
		{name: "a name with a quote", addr: netmail.Address{Name: `A "quoted" name`, Address: "user@example.com"}, want: `"A \"quoted\" name" <user@example.com>`},
		{name: "an encoded name", addr: netmail.Address{Name: "运维", Address: "user@example.com"}, want: `=?UTF-8?q?=E8=BF=90=E7=BB=B4?= <user@example.com>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := addressHeader(c.addr)
			if err != nil {
				t.Fatalf("addressHeader() error = %v", err)
			}
			if got != c.want {
				t.Errorf("addressHeader() = %q; want %q", got, c.want)
			}
			back, err := netmail.ParseAddress(got)
			if err != nil || back.Address != c.addr.Address || back.Name != c.addr.Name {
				t.Errorf("ParseAddress(%q) = %v, %v; want %q at %q", got, back, err, c.addr.Name, c.addr.Address)
			}
		})
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
	maxLine(t, payload, lineMax)
}

// maxLine checks that no line of the header section passes width, which is where a long value has to be folded.
func maxLine(t *testing.T, payload []byte, width int) {
	t.Helper()
	headers, _, _ := bytes.Cut(payload, []byte("\r\n\r\n"))
	for i, line := range bytes.Split(headers, []byte("\r\n")) {
		if len(line) > width {
			t.Errorf("header line %d is %d bytes; want at most %d: %q", i+1, len(line), width, line)
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
