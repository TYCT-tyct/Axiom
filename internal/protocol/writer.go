package protocol

import "strconv"

// writer is a request body a conversion writes, and the only way the conversion's output grows: room draws the grown
// array from the budget before making it, the old array included, as both are alive during the copy, and gives the
// old one back after it. A draw that fails is kept in err and every later write is dropped, so nothing grows past the
// refusal; the conversion returns err. The body is written in place: a part that may turn out empty is written and
// cut back, never built in a buffer of its own.
type writer struct {
	buf []byte
	b   *budget
	err error
}

// newWriter is a writer whose first array holds size bytes.
func newWriter(size int, b *budget) *writer {
	w := &writer{b: b}
	w.room(size)
	return w
}

// room makes room for n more bytes and reports whether there is.
func (w *writer) room(n int) bool {
	if w.err != nil {
		return false
	}
	if cap(w.buf)-len(w.buf) >= n {
		return true
	}
	w.buf, w.err = growBytes(w.buf, n, w.b)
	return w.err == nil
}

func (w *writer) str(s string) *writer {
	if w.room(len(s)) {
		w.buf = append(w.buf, s...)
	}
	return w
}

func (w *writer) raw(v []byte) *writer {
	if w.room(len(v)) {
		w.buf = append(w.buf, v...)
	}
	return w
}

func (w *writer) byte(c byte) {
	if w.room(1) {
		w.buf = append(w.buf, c)
	}
}

// quoted writes s as a JSON string.
func (w *writer) quoted(s string) *writer {
	if w.room(quotedLength(s)) {
		w.buf = appendQuoted(w.buf, s)
	}
	return w
}

func (w *writer) int(n int64) *writer {
	if w.room(len("-9223372036854775808")) {
		w.buf = strconv.AppendInt(w.buf, n, 10)
	}
	return w
}

func (w *writer) bool(v bool) *writer {
	if w.room(len("false")) {
		w.buf = strconv.AppendBool(w.buf, v)
	}
	return w
}

// commaAfter writes a comma when the body holds something after mark.
func (w *writer) commaAfter(mark int) *writer {
	if len(w.buf) > mark {
		w.byte(',')
	}
	return w
}

// last is the last byte written.
func (w *writer) last() byte { return w.buf[len(w.buf)-1] }

// cut drops what was written after mark.
func (w *writer) cut(mark int) { w.buf = w.buf[:mark] }
