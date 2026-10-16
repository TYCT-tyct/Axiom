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
	"strings"
	"unsafe"

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

// readSize is the most one read asks for, and the free space ensured before it: about one TLS record, so a read
// rarely stops short for lack of room, and a caller that polls hears back after every 16 KiB at most.
const readSize = 16 << 10

// keepSize and calmEvents decide when buffers a large event grew are given back: only buffers past keepSize are, and
// only after calmEvents events in a row each used less than a quarter of them. Giving them back at the first small
// event would allocate them again for every large event of a stream that has several; never giving them back would
// leave a stream that had one large event holding twice its size for the rest of its life.
const (
	keepSize   = 64 << 10
	calmEvents = 16
)

var bom = []byte("\xef\xbb\xbf") // U+FEFF in UTF-8

// Reader reads events from an upstream stream. It is not safe for concurrent use.
type Reader struct {
	src   io.Reader
	limit int

	buf   []byte // bytes read from src; buf[start:] is not yet parsed
	start int
	scan  int // buf[start:start+scan] has no line ending, so a search resumes after it
	// The current event's data values: lines of them so far. The first stays in buf as lone, which saves copying the
	// common event of one data line; more() copies it into data, each value followed by LF, when buf is about to
	// move, and a second value does the same.
	lines int
	lone  []byte
	data  []byte
	size  int // bytes of the current event so far: its lines with their line endings
	calm  int // events in a row that used less than a quarter of the buffers
	eof   bool
	// readErr is a read failure held back until the bytes that came with it are parsed, so that events complete
	// before the failure are returned however the reads were split.
	readErr error
	err     error

	bomChecked bool
	charge     func(n int) error
	validator  jsonx.Validator
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
func (r *Reader) Next() ([]byte, error) { return r.call(-1) }

// Poll is Next reading from src at most once: when that read leaves the next event unfinished, it returns no data
// and no error, and Pending counts the bytes of the event that have arrived.
func (r *Reader) Poll() ([]byte, error) { return r.call(1) }

// Pending is the bytes read and not yet returned as an event: the lines of the unfinished event with their line
// endings, and what follows them.
func (r *Reader) Pending() int { return r.size + len(r.buf) - r.start }

// ChargeGrowth has charge called before a buffer of the reader grows, with the bytes the new array adds to what the
// reader holds; when it returns an error, the buffer does not grow and the reader fails with that error. After a
// large event the reader gives buffers back, calling charge with the bytes it no longer holds as a negative n; an
// error from that call fails the reader as well.
func (r *Reader) ChargeGrowth(charge func(n int) error) { r.charge = charge }

func (r *Reader) call(reads int) ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	data, err := r.next(reads)
	r.err = err
	return data, err
}

// next returns the next event, reading from src at most reads times, or as often as it takes when reads is negative.
func (r *Reader) next(reads int) ([]byte, error) {
	for {
		line, ok := r.line()
		switch {
		case !ok && reads == 0:
			return nil, nil
		case !ok:
			reads--
			if err := r.more(); err != nil {
				return nil, err
			}
		case r.size > r.limit:
			return nil, ErrTooLarge
		case len(line) > 0:
			if err := r.field(line); err != nil {
				return nil, err
			}
		case r.lines > 0:
			return r.dispatch()
		default: // a blank line ending an event without data, which is no event at all
			r.size = 0
		}
	}
}

// dispatch ends the current event at its blank line and returns its data.
func (r *Reader) dispatch() ([]byte, error) {
	var data []byte
	if len(r.data) == 0 { // the one value, still in buf; its room is not the caller's to append into
		data = slices.Clip(r.lone)
	} else {
		data = r.data[:len(r.data)-1] // the LF after the last value
		r.data = r.data[:0]
	}
	if 4*r.size > cap(r.buf)+cap(r.data) {
		r.calm = 0
	} else {
		r.calm++
	}
	r.lines, r.lone, r.size = 0, nil, 0
	if string(data) != Done {
		if err := r.validator.Validate(data); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err) //nolint:errorlint // see ErrInvalid
		}
	}
	return data, nil
}

// line returns the next complete line without its line ending, which is LF, CRLF or CR. A CR at the end of the
// buffer may be the first half of a CRLF, so its line waits for the next byte, unless none will come because the
// stream has ended or a read has failed; at the end of the stream, the last line ends there.
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
	case i+1 < len(rest) || r.eof || r.readErr != nil:
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
func (r *Reader) field(line []byte) (err error) {
	name, value, _ := bytes.Cut(line, []byte(":"))
	if string(name) == "data" {
		value = bytes.TrimPrefix(value, []byte(" "))
		if r.lines == 0 {
			r.lone = value
		} else if err = r.spill(); err == nil {
			err = r.appendData(value)
		}
		r.lines++
	}
	return err
}

// spill copies the event's first data value, if that is all it has and it is still in buf, into data.
func (r *Reader) spill() error {
	if r.lines != 1 || len(r.data) > 0 {
		return nil
	}
	err := r.appendData(r.lone)
	r.lone = nil
	return err
}

// appendData adds value and an LF to data.
func (r *Reader) appendData(value []byte) (err error) {
	if r.data, err = Grow(r.data, len(value)+1, r.charge); err == nil {
		r.data = append(append(r.data, value...), '\n')
	}
	return err
}

// more reads from src when no complete line is buffered, or reports why the stream cannot go on.
func (r *Reader) more() error {
	pending := len(r.buf) - r.start
	switch {
	case r.size+pending > r.limit:
		return ErrTooLarge
	case r.readErr != nil:
		return r.readErr
	case r.eof && r.lines > 0:
		return io.ErrUnexpectedEOF
	case r.eof:
		return io.EOF
	}
	if err := r.repack(pending); err != nil {
		return err
	}
	var err error
	if r.buf, err = Grow(r.buf, readSize, r.charge); err != nil {
		return err
	}
	n, err := r.src.Read(r.buf[pending : pending+readSize])
	r.buf = r.buf[:pending+n]
	switch {
	case errors.Is(err, io.EOF):
		r.eof = true
	case err != nil:
		r.readErr = fmt.Errorf("sse: read: %w", err)
	}
	return nil
}

// repack moves the pending bytes to the front of buf, which is about to be read into, after copying out of it what
// the current event still needs, and gives back the buffers a large event left behind once calmEvents small ones have
// followed it.
func (r *Reader) repack(pending int) error {
	if err := r.spill(); err != nil {
		return err
	}
	if r.calm >= calmEvents && cap(r.buf)+cap(r.data) > keepSize {
		if err := r.shrink(pending); err != nil {
			return err
		}
	}
	if r.start > 0 { // only then: moving a long unfinished line onto itself on every read would cost quadratic time
		r.buf = append(r.buf[:0], r.buf[r.start:]...)
		r.start = 0
	}
	return nil
}

// shrink drops data unless an event is using it and moves the pending bytes to a buffer that fits them and one read,
// then tells charge what it gave back.
func (r *Reader) shrink(pending int) error {
	given := 0
	if len(r.data) == 0 {
		given, r.data = cap(r.data), nil
	}
	if want := pending + readSize; cap(r.buf) > want {
		given += cap(r.buf) - want
		r.buf = append(make([]byte, 0, want), r.buf[r.start:]...)
		r.start = 0
	}
	r.calm = 0
	if r.charge == nil || given == 0 {
		return nil
	}
	return r.charge(-given)
}

// Grow returns b with room for n more elements. A new array is at least twice as large, so appending costs amortized
// constant time; charge, unless nil, is first asked for the bytes it adds, and an error from it is returned instead.
func Grow[T any](b []T, n int, charge func(n int) error) ([]T, error) {
	if cap(b)-len(b) >= n {
		return b, nil
	}
	size := max(2*cap(b), len(b)+n)
	if charge != nil {
		if err := charge((size - cap(b)) * int(unsafe.Sizeof(*new(T)))); err != nil {
			return b, err
		}
	}
	return append(make([]T, 0, size), b...), nil
}

// AppendEvent appends to dst an event whose data is data: an event line naming it, unless name is empty or holds
// anything but letters, digits, '_' and '.', which could change the framing; a data line per line of data; a blank
// line.
func AppendEvent(dst []byte, name string, data []byte) []byte {
	if name != "" && strings.Trim(name, eventNameChars) == "" {
		dst = append(append(append(dst, "event: "...), name...), '\n')
	}
	for line := range bytes.SplitSeq(data, []byte("\n")) { // data joins its data lines with LF
		dst = append(append(append(dst, "data: "...), line...), '\n')
	}
	return append(dst, '\n')
}

// EventSize is the most AppendEvent appends for an event named name whose data is data.
func EventSize(name string, data []byte) int {
	return len("event: \n") + len(name) + len(data) + len("data: \n")*(1+bytes.Count(data, []byte("\n"))) + len("\n")
}

const eventNameChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_."
