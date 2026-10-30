package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in upstream.go: an answer's string is unescaped in place, not decoded and encoded again
	"unicode/utf8"
)

// Capture is an answer's text as a relay read it, for a caller that must judge what the model said: a verification
// probe compares it with a known answer (docs/domains/quality.md「真伪检测」). It is not part of a gateway call, which
// sets none and keeps nothing; the text is the content strings of the upstream's events, unquoted, including reasoning
// and tool input, up to Max bytes.
type Capture struct {
	Text []byte
	Max  int
}

// add appends the text of v, a JSON string or nil, up to Max: the runs between escapes as they are, each escape as the
// character it stands for, as addShape tallies them.
func (c *Capture) add(v jsontext.Value) {
	if c.Max <= 0 || len(v) < 2 || len(c.Text) >= c.Max {
		return
	}
	v = v[1 : len(v)-1]
	var buf [utf8.UTFMax]byte
	for len(v) > 0 {
		i := bytes.IndexByte(v, '\\')
		if i < 0 {
			c.appendText(v)
			return
		}
		c.appendText(v[:i])
		if len(c.Text) >= c.Max || len(v)-i < 2 {
			return
		}
		r, n := unescape(v[i:])
		if n == 0 || len(v)-i < n {
			return
		}
		c.appendText(utf8.AppendRune(buf[:0], r))
		v = v[i+n:]
	}
}

// appendText appends what fits of b.
func (c *Capture) appendText(b []byte) {
	if room := c.Max - len(c.Text); room > 0 {
		c.Text = append(c.Text, b[:min(len(b), room)]...)
	}
}
