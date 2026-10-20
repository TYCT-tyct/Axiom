package mail

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"
	"time"
)

// Message is one email as the caller writes it: a recipient, a subject, and the same content as plain text and as
// HTML. Both bodies are required — the text is what a reader that shows no HTML gets, and the HTML carries the links —
// and neither is a template: account flows decide what they say, this package only delivers it.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// invalid wraps a fault in what the caller built, so a job can tell a permanent fault from a transient one.
func invalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidMessage, reason)
}

// validate parses the recipient and refuses what must not reach a header or an empty message. A line break in a header
// would let the caller add headers of its own (RFC 5322 header injection), and the other control characters have no
// place in a header at all. The last check is the one the send makes: a recipient or subject no header line can carry is
// refused here, before the message is stored, rather than by the job that would find it later.
//
// No refusal quotes the value it refuses. This error reaches the caller, a log line and — from a job — a table no tenant
// policy covers, and the recipient, the subject and the body belong to one tenant.
func (m Message) validate() (*netmail.Address, error) {
	switch {
	case strings.TrimSpace(m.Subject) == "" || hasControl(m.Subject):
		return nil, invalid("the subject is empty or spans lines")
	case m.Text == "" || m.HTML == "":
		return nil, invalid("both a text and an HTML body are required")
	case hasControl(m.To):
		return nil, invalid("the recipient address spans lines")
	}
	// The parser's own message can quote the address it refused, so the reason is spelled here instead.
	to, err := netmail.ParseAddress(m.To)
	if err != nil {
		return nil, invalid("the recipient is not one address")
	}
	recipient, err := addressHeader(*to)
	if err != nil {
		return nil, err
	}
	if err := checkHeaders([]header{{"To", recipient}, {"Subject", textHeader(m.Subject)}}); err != nil {
		return nil, err
	}
	return to, nil
}

// hasControl reports whether s holds a character that cannot stand in a header, a line break above all: the value would
// end the field and let the rest be read as fields of their own.
func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < ' ' && c != '\t' || c == 0x7f {
			return true
		}
	}
	return false
}

// compose is one message with the values the sender generates. They are fields rather than defaults so tests can
// compare a whole message against a golden copy and the result does not depend on the clock.
type compose struct {
	from, to   netmail.Address
	subject    string
	text, html string
	date       time.Time
	messageID  string
	boundary   string
}

// bytes renders the message as SMTP DATA carries it: both bodies are quoted-printable alternatives of one another and
// every line ends in CRLF (RFC 5322 section 2.3). A value no header line can carry is refused here, before anything is
// sent.
func (c compose) bytes() ([]byte, error) {
	from, err := addressHeader(c.from)
	if err != nil {
		return nil, err
	}
	to, err := addressHeader(c.to)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := writeHeaders(&b, []header{
		{"From", from},
		{"To", to},
		{"Subject", textHeader(c.subject)},
		{"Date", c.date.Format(time.RFC1123Z)},
		{"Message-ID", c.messageID},
		{"MIME-Version", "1.0"},
		{"Content-Type", `multipart/alternative; boundary="` + c.boundary + `"`},
	}); err != nil {
		return nil, err
	}
	b.WriteString("\r\n")
	c.part(&b, "text/plain", c.text)
	c.part(&b, "text/html", c.html)
	b.WriteString("--")
	b.WriteString(c.boundary)
	b.WriteString("--\r\n")
	return b.Bytes(), nil
}

// part writes one alternative. The body is followed by CRLF of its own so the boundary that ends it always starts a
// line, whether or not the caller's text ended with one.
func (c compose) part(b *bytes.Buffer, contentType, body string) {
	b.WriteString("--")
	b.WriteString(c.boundary)
	b.WriteString("\r\n")
	// Both fields are fixed and short, so neither can pass the line limit.
	_ = writeHeaders(b, []header{ //nolint:errcheck // as the comment above says
		{"Content-Type", contentType + "; charset=UTF-8"},
		{"Content-Transfer-Encoding", "quoted-printable"},
	})
	b.WriteString("\r\n")
	w := quotedprintable.NewWriter(b)
	_, _ = w.Write([]byte(body)) //nolint:errcheck // writing to a bytes.Buffer cannot fail
	_ = w.Close()                //nolint:errcheck // writing to a bytes.Buffer cannot fail
	b.WriteString("\r\n")
}

// newMessageID is unique per message and carries the sending domain: relays and receivers treat it as the message's
// identity, and read the sender's reputation from the domain. The address is the one ReadConfig accepted, so it has a
// domain.
func newMessageID(from string) string {
	return "<" + rand.Text() + "@" + from[strings.LastIndexByte(from, '@')+1:] + ">"
}
