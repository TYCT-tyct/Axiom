package mail

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"mime"
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

// validate parses the recipient and refuses what must not reach a header or an empty message. A line break in a
// header would let the caller add headers of its own (RFC 5322 header injection).
func (m Message) validate() (*netmail.Address, error) {
	switch {
	case strings.TrimSpace(m.Subject) == "" || strings.ContainsAny(m.Subject, "\r\n"):
		return nil, invalid("the subject is empty or spans lines")
	case m.Text == "" || m.HTML == "":
		return nil, invalid("both a text and an HTML body are required")
	case strings.ContainsAny(m.To, "\r\n"):
		return nil, invalid("the recipient address spans lines")
	}
	to, err := netmail.ParseAddress(m.To)
	if err != nil {
		return nil, fmt.Errorf("%w: the recipient is not one address: %w", ErrInvalidMessage, err)
	}
	return to, nil
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

// bytes renders the message as SMTP DATA carries it: UTF-8 subjects become encoded words, both bodies are
// quoted-printable alternatives of one another, and every line ends in CRLF (RFC 5322 section 2.3).
func (c compose) bytes() []byte {
	var b bytes.Buffer
	writeHeader(&b, "From", c.from.String())
	writeHeader(&b, "To", c.to.String())
	writeHeader(&b, "Subject", mime.QEncoding.Encode("UTF-8", c.subject))
	writeHeader(&b, "Date", c.date.Format(time.RFC1123Z))
	writeHeader(&b, "Message-ID", c.messageID)
	writeHeader(&b, "MIME-Version", "1.0")
	writeHeader(&b, "Content-Type", `multipart/alternative; boundary="`+c.boundary+`"`)
	b.WriteString("\r\n")
	c.part(&b, "text/plain", c.text)
	c.part(&b, "text/html", c.html)
	b.WriteString("--")
	b.WriteString(c.boundary)
	b.WriteString("--\r\n")
	return b.Bytes()
}

// part writes one alternative. The body is followed by CRLF of its own so the boundary that ends it always starts a
// line, whether or not the caller's text ended with one.
func (c compose) part(b *bytes.Buffer, contentType, body string) {
	b.WriteString("--")
	b.WriteString(c.boundary)
	b.WriteString("\r\n")
	writeHeader(b, "Content-Type", contentType+"; charset=UTF-8")
	writeHeader(b, "Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	w := quotedprintable.NewWriter(b)
	_, _ = w.Write([]byte(body)) //nolint:errcheck // writing to a bytes.Buffer cannot fail
	_ = w.Close()                //nolint:errcheck // writing to a bytes.Buffer cannot fail
	b.WriteString("\r\n")
}

func writeHeader(b *bytes.Buffer, name, value string) {
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString("\r\n")
}

// newMessageID is unique per message and carries the sending domain: relays and receivers treat it as the message's
// identity, and read the sender's reputation from the domain.
func newMessageID(from string) string {
	domain := "localhost"
	if at := strings.LastIndexByte(from, '@'); at >= 0 && at < len(from)-1 {
		domain = from[at+1:]
	}
	return "<" + rand.Text() + "@" + domain + ">"
}
