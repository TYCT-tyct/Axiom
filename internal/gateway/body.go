package gateway

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
)

// minBody is the first buffer of a body whose length is not announced.
const minBody = 4 << 10

// readBody reads the body within MaxBody and charges it to the node's memory budget until the call is done. Whatever is
// allocated for it is charged before it is allocated, so no number of slow or large uploads holds more than the budget:
// an announced length is charged whole before the first byte is read; an unannounced body is charged as its buffer
// grows, which a body that stops short never needs more of than it sent.
func (h *Handler) readBody(w http.ResponseWriter, r *http.Request, c *Call) ([]byte, error) {
	limit := h.Limits.cfg.MaxBody
	if r.ContentLength > limit {
		return nil, errTooLarge
	}
	src := http.MaxBytesReader(w, r.Body, limit)
	read := h.readUnannounced
	if r.ContentLength >= 0 {
		read = h.readAnnounced
	}
	body, held, err := read(src, r.ContentLength, limit)
	if err != nil {
		return nil, bodyRefusal(err)
	}
	c.hold(func() { h.Memory.Refund(held) })
	return body, nil
}

// readAnnounced reads a body of the length announced into a buffer of that length. It returns the memory it holds.
func (h *Handler) readAnnounced(src io.Reader, length, _ int64) ([]byte, int64, error) {
	if !h.Memory.Charge(length) {
		return nil, 0, ErrOverloaded
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(src, buf); err != nil {
		h.Memory.Refund(length)
		return nil, 0, err
	}
	return buf, length, nil
}

// readUnannounced reads a body of unknown length into one buffer that doubles. The new buffer is charged before it is
// made and the old one's charge is given back once it is copied, so the most held is the two together; io.ReadAll
// keeps every chunk until the end and then makes a whole copy, about twice the body, that nothing had charged. The
// buffer is kept as it ends, up to twice the body, and that is what stays charged. It returns the memory it holds.
func (h *Handler) readUnannounced(src io.Reader, _, limit int64) ([]byte, int64, error) {
	var buf []byte
	var held int64
	for {
		if len(buf) == cap(buf) {
			// One byte past the limit lets the reader report a body that is larger.
			next := min(max(2*held, minBody), limit+1)
			if !h.Memory.Charge(next) {
				h.Memory.Refund(held)
				return nil, 0, ErrOverloaded
			}
			buf = append(make([]byte, 0, next), buf...)
			h.Memory.Refund(held)
			held = next
		}
		n, err := src.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if errors.Is(err, io.EOF) {
			return buf, held, nil
		}
		if err != nil {
			h.Memory.Refund(held)
			return nil, 0, err
		}
	}
}

// bodyRefusal says what a failed read of the body is answered with. A client that went away is not answered at all,
// which the caller sees by its context.
func bodyRefusal(err error) *Refusal {
	var tooBig *http.MaxBytesError
	var timeout net.Error
	var refusal *Refusal
	switch {
	case errors.As(err, &refusal):
		return refusal
	case errors.As(err, &tooBig):
		return errTooLarge
	case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return errBodyTimeout
	}
	return invalid("The request body could not be read.")
}
