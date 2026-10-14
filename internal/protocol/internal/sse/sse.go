// Package sse reads an upstream's server-sent event stream into complete events, following the WHATWG HTML
// "Server-sent events" parsing rules, and checks each event's data strictly before anyone looks at it.
//
// Only the data field matters: the three call formats name an event's kind in its data's "type" member, and some
// relays leave out the event field, so event names are ignored, as are id and retry (an upstream stream is never
// resumed) and comments.
package sse

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"

	"axiom/internal/platform/jsonx"
)

// Done is the data of the event that ends an OpenAI Chat stream. It is the only data that is not JSON; whether it
// may appear in a stream is for the caller, who knows the format, to judge.
const Done = "[DONE]"

var (
	// ErrTooLarge reports an event, or a line that does not end, longer than the reader's limit.
	ErrTooLarge = errors.New("sse: event exceeds the size limit")
	// ErrInvalid reports an event whose data is neither Done nor valid JSON. It does not wrap the JSON error, whose
	// io.ErrUnexpectedEOF for a truncated value would read as the stream ending early.
	ErrInvalid = errors.New("sse: invalid event data")
)

// minRead is the free space ensured before each read: about one TLS record, so a read rarely stops short for lack
// of room.
const minRead = 16 << 10

var bom = []byte("\xef\xbb\xbf") // U+FEFF in UTF-8

// Reader reads events from an upstream stream. It is not safe for concurrent use.
type Reader struct {
	src   io.Reader
	limit int

	buf   []byte // bytes read from src; buf[start:] is not yet parsed
	start int
	scan  int    // buf[start:start+scan] has no line ending, so a search resumes after it
	data  []byte // the current event's data values, each followed by LF
	size  int    // bytes of the current event so far: its lines with their line endings
	eof   bool
	err   error

	bomChecked bool
}

// NewReader returns a Reader that fails with ErrTooLarge once an event, counting every byte of its lines and line
// endings, comments included, exceeds limit bytes. Its buffers grow with the events actually read, up to a small
// multiple of the limit, so a generous limit costs nothing until an event needs it.
func NewReader(src io.Reader, limit int) *Reader {
	return &Reader{src: src, limit: limit}
}

// Next returns the data of the next event: the values of its data fields joined by "\n", either Done or one JSON
// value accepted by jsonx.Validate. The slice is valid until the next call. At the end of the stream it returns
// io.EOF, or io.ErrUnexpectedEOF if the stream ends inside an event that has data. After any error, every later
// call returns the same error.
func (r *Reader) Next() ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	data, err := r.next()
	r.err = err
	return data, err
}

func (r *Reader) next() ([]byte, error) {
	for {
		line, ok := r.line()
		switch {
		case !ok:
			if err := r.more(); err != nil {
				return nil, err
			}
		case r.size > r.limit:
			return nil, ErrTooLarge
		case len(line) > 0:
			r.field(line)
		case len(r.data) > 0:
			return r.dispatch()
		default: // a blank line ending an event without data, which is no event at all
			r.size = 0
		}
	}
}

// dispatch ends the current event at its blank line and returns its data.
func (r *Reader) dispatch() ([]byte, error) {
	data := r.data[:len(r.data)-1] // the LF after the last value
	r.data = r.data[:0]
	r.size = 0
	if string(data) != Done {
		if err := jsonx.Validate(data); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err) //nolint:errorlint // see ErrInvalid
		}
	}
	return data, nil
}

// line returns the next complete line without its line ending, which is LF, CRLF or CR. A CR at the end of the
// buffer may be the first half of a CRLF, so its line waits for the next byte; at the end of the stream, the last
// line ends there.
func (r *Reader) line() ([]byte, bool) {
	if !r.bomChecked && !r.skipBOM() {
		return nil, false
	}
	rest := r.buf[r.start:]
	i := bytes.IndexAny(rest[r.scan:], "\r\n")
	if i < 0 {
		if !r.eof || len(rest) == 0 {
			r.scan = len(rest)
			return nil, false
		}
		return r.take(len(rest), 0), true
	}
	i += r.scan
	switch {
	case rest[i] == '\n':
		return r.take(i, 1), true
	case i+1 < len(rest) && rest[i+1] == '\n':
		return r.take(i, 2), true
	case i+1 < len(rest) || r.eof:
		return r.take(i, 1), true
	}
	r.scan = i
	return nil, false
}

// take consumes a line of n bytes and its line ending of end bytes.
func (r *Reader) take(n, end int) []byte {
	line := r.buf[r.start : r.start+n]
	r.start += n + end
	r.size += n + end
	r.scan = 0
	return line
}

// skipBOM drops a byte order mark at the start of the stream. It reports false while too few bytes have arrived to
// tell.
func (r *Reader) skipBOM() bool {
	rest := r.buf[r.start:]
	if len(rest) < len(bom) && bytes.HasPrefix(bom, rest) && !r.eof {
		return false
	}
	if bytes.HasPrefix(rest, bom) {
		r.start += len(bom)
	}
	r.bomChecked = true
	return true
}

// field applies one non-blank line to the current event.
func (r *Reader) field(line []byte) {
	name, value, _ := bytes.Cut(line, []byte(":"))
	if string(name) == "data" {
		r.data = append(r.data, bytes.TrimPrefix(value, []byte(" "))...)
		r.data = append(r.data, '\n')
	}
}

// more reads from src when no complete line is buffered, or reports why the stream cannot go on.
func (r *Reader) more() error {
	pending := len(r.buf) - r.start
	switch {
	case r.size+pending > r.limit:
		return ErrTooLarge
	case r.eof && len(r.data) > 0:
		return io.ErrUnexpectedEOF
	case r.eof:
		return io.EOF
	}
	if r.start > 0 { // only then: moving a long unfinished line onto itself on every read would cost quadratic time
		r.buf = append(r.buf[:0], r.buf[r.start:]...)
		r.start = 0
	}
	r.buf = slices.Grow(r.buf, minRead)
	n, err := r.src.Read(r.buf[pending:cap(r.buf)])
	r.buf = r.buf[:pending+n]
	switch {
	case errors.Is(err, io.EOF):
		r.eof = true
	case err != nil:
		return fmt.Errorf("sse: read: %w", err)
	}
	return nil
}
