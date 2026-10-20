package mail

import (
	"bytes"
	netmail "net/mail"
	"strings"
	"unicode/utf8"
)

const (
	// foldAt is the column a header line is folded at: RFC 5322 recommends 78 characters, name and colon included.
	foldAt = 78
	// lineMax is the longest line a message may hold, its CRLF excluded; RFC 5322 section 2.1.1 makes that a MUST.
	lineMax = 998
	// pathMax is the longest envelope address SMTP allows, angle brackets included (RFC 5321 section 4.5.3.1.3).
	pathMax = 256

	// wordOpen and wordClose bracket an RFC 2047 encoded word, whose whole length may not pass 75 characters
	// (section 2). The text between them is Q encoded UTF-8: one character per byte that fits, three per byte that
	// does not.
	wordOpen  = "=?UTF-8?q?"
	wordClose = "?="

	maxWordContent = 75 - len(wordOpen) - len(wordClose)
	upperhex       = "0123456789ABCDEF"
)

// header is one field of a message's header section.
type header struct{ name, value string }

// writeHeaders writes the fields in order.
func writeHeaders(b *bytes.Buffer, headers []header) error {
	for _, h := range headers {
		if err := writeHeader(b, h.name, h.value); err != nil {
			return err
		}
	}
	return nil
}

// checkHeaders reports whether a header section can be written at all, keeping nothing it wrote. Both boundaries ask it
// — the configured sender at startup, the recipient and the subject at enqueue — so a value the send would refuse is
// refused before it is stored, and one renderer answers for all of them.
func checkHeaders(headers []header) error {
	return writeHeaders(new(bytes.Buffer), headers)
}

// writeHeader writes one field and folds it at its spaces: the line ends before a space and the next line begins with
// it, so unfolding — dropping the CRLF and keeping the space — gives the value back unchanged (RFC 5322 section 2.2.3).
// A first word that does not fit beside the field's name starts the next line, after the colon alone, with the same
// one space before it. A word no line can carry is refused rather than sent, because a line past 998 characters breaks
// a MUST NOT and a receiver may drop the message. The refusal names the header and never the value, which is what lets
// a job pass it on.
func writeHeader(b *bytes.Buffer, name, value string) error {
	b.WriteString(name)
	b.WriteByte(':')
	width := len(name) + 1
	for word := range strings.SplitSeq(value, " ") {
		if width+1+len(word) > foldAt {
			b.WriteString("\r\n")
			width = 0
		}
		b.WriteByte(' ')
		width += 1 + len(word)
		if width > lineMax {
			return invalid("the " + name + " header holds a word longer than one line can carry")
		}
		b.WriteString(word)
	}
	b.WriteString("\r\n")
	return nil
}

// textHeader renders a free-text value, such as a subject, as a header may carry it: as written when it is printable
// ASCII, and as encoded words otherwise, which a reader decodes back to the same text (RFC 2047 section 2).
func textHeader(value string) string {
	if isPrintableASCII(value) {
		return value
	}
	return strings.Join(encodeWords(value), " ")
}

// addressHeader renders one address as a header carries it: the display name, then the mailbox in angle brackets, which
// is the same mailbox the envelope is given, so both name the same recipient (RFC 5322 section 3.4).
func addressHeader(a netmail.Address) (string, error) {
	path, err := mailbox(a)
	if err != nil {
		return "", err
	}
	address := "<" + path + ">"
	switch {
	case a.Name == "":
		return address, nil
	case isPrintableASCII(a.Name):
		return quotePhrase(a.Name) + " " + address, nil
	default:
		return strings.Join(encodeWords(a.Name), " ") + " " + address, nil
	}
}

// quotePhrase writes a display name as a quoted-string, which reads as written: the quote and the backslash are escaped
// and every other printable ASCII character, spaces included, goes in as it is (RFC 5322 section 3.2.4).
func quotePhrase(name string) string {
	var b strings.Builder
	b.Grow(len(name) + 2)
	b.WriteByte('"')
	for i := 0; i < len(name); i++ {
		if c := name[i]; c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(name[i])
	}
	b.WriteByte('"')
	return b.String()
}

// mailbox writes an address the way an SMTP envelope wants it: a local part of dot-atoms as it is, anything else as a
// quoted-string with the quote and the backslash escaped, and the domain unchanged (RFC 5321 section 4.1.2). net/smtp
// adds the angle brackets, in MAIL FROM and RCPT TO alike. Only printable ASCII fits: this node never offers SMTPUTF8,
// so an address whose local part or domain is not ASCII is refused here rather than mangled on the way.
//
// A refusal names the fault and not the address: the value belongs to one tenant, and this error reaches a caller, a log
// line and — through the job — a table no tenant policy covers.
func mailbox(a netmail.Address) (string, error) {
	at := strings.LastIndexByte(a.Address, '@')
	if at <= 0 || at == len(a.Address)-1 {
		return "", invalid("the address has no local part or no domain")
	}
	local, domain := a.Address[:at], a.Address[at+1:]
	if !isDotAtom(local) {
		quoted, ok := quoteLocal(local)
		if !ok {
			return "", invalid("the local part holds a character only SMTPUTF8 could carry")
		}
		local = quoted
	}
	if !isPrintableASCII(domain) {
		return "", invalid("the domain holds a character only SMTPUTF8 could carry")
	}
	if len(local)+len(domain)+3 > pathMax {
		return "", invalid("the address is longer than SMTP allows")
	}
	return local + "@" + domain, nil
}

// quoteLocal writes a local part as a quoted-string, and reports whether every byte of it fits one: a quoted-pair
// carries any printable ASCII character, the quote and the backslash through a backslash of their own.
func quoteLocal(local string) (string, bool) {
	var b strings.Builder
	b.Grow(len(local) + 2)
	b.WriteByte('"')
	for i := 0; i < len(local); i++ {
		c := local[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c >= ' ' && c <= '~':
			b.WriteByte(c)
		default:
			return "", false
		}
	}
	b.WriteByte('"')
	return b.String(), true
}

// isDotAtom reports whether s may stand in an SMTP local part without quotes: words of atext joined by single dots, with
// no dot at either end (RFC 5321 section 4.1.2).
func isDotAtom(s string) bool {
	if s == "" || s[0] == '.' || s[len(s)-1] == '.' || strings.Contains(s, "..") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c != '.' && !isAtext(c) {
			return false
		}
	}
	return true
}

// isAtext reports whether c may stand in an unquoted word of an address (RFC 5322 section 3.2.3, which RFC 5321
// adopts): the letters, the digits and a fixed set of punctuation.
func isAtext(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-/=?^_`{|}~", c) >= 0
}

// encodeWords splits s into RFC 2047 encoded-words a reader decodes back to s: each holds as much encoded text as the
// 75-character limit leaves room for, and no character is split across two words (section 5.3). The words are separated
// by one space, which is where the header folds and which a reader drops when it decodes.
func encodeWords(s string) []string {
	var (
		words []string
		word  strings.Builder
		n     int
	)
	word.WriteString(wordOpen)
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		text := s[i : i+size]
		i += size
		width := qLength(text)
		if n+width > maxWordContent {
			word.WriteString(wordClose)
			words = append(words, word.String())
			word.Reset()
			word.WriteString(wordOpen)
			n = 0
		}
		writeQString(&word, text)
		n += width
	}
	word.WriteString(wordClose)
	return append(words, word.String())
}

// qLength is how many characters the Q encoding of one character takes: one for a letter or a space, three for each
// byte that has to be quoted.
func qLength(text string) int {
	n := 0
	for i := 0; i < len(text); i++ {
		if c := text[i]; c == ' ' || isQText(c) {
			n++
		} else {
			n += 3
		}
	}
	return n
}

// writeQString writes one character as Q encoded text: a space becomes an underscore, and every byte that cannot stand
// for itself becomes =XX with the digits in upper case (RFC 2047 section 4.2).
func writeQString(b *strings.Builder, text string) {
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == ' ':
			b.WriteByte('_')
		case isQText(c):
			b.WriteByte(c)
		default:
			b.WriteByte('=')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&0x0f])
		}
	}
}

// isQText reports whether c may stand for itself in an encoded word: the printable characters except the three the
// encoding gives a meaning of their own.
func isQText(c byte) bool { return c >= '!' && c <= '~' && c != '=' && c != '?' && c != '_' }

// isPrintableASCII reports whether every byte of s is a space or a printable ASCII character, so a header can carry the
// value as it is.
func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < ' ' || c > '~' {
			return false
		}
	}
	return true
}
