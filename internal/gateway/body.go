package gateway

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
)

// readBody reads the request body within MaxBody and charges it to the node's memory budget until the call is done.
// The length is charged before the first byte is read: a body of unknown length is charged at the most it may be and
// the difference is refunded when it ends, so no number of slow uploads holds more than the budget.
func (h *Handler) readBody(w http.ResponseWriter, r *http.Request, c *Call) ([]byte, error) {
	limit := h.Limits.cfg.MaxBody
	if r.ContentLength > limit {
		return nil, errTooLarge
	}
	charge := limit
	if r.ContentLength >= 0 {
		charge = r.ContentLength
	}
	if !h.Memory.Charge(charge) {
		return nil, ErrOverloaded
	}
	body, err := readAll(http.MaxBytesReader(w, r.Body, limit), r.ContentLength)
	if err != nil {
		h.Memory.Refund(charge)
		return nil, bodyRefusal(err)
	}
	h.Memory.Refund(charge - int64(len(body)))
	c.hold(func() { h.Memory.Refund(int64(len(body))) })
	return body, nil
}

// bodyRefusal says what a failed read of the body is answered with. A client that went away is not answered at all,
// which the caller sees by its context.
func bodyRefusal(err error) *Refusal {
	var tooBig *http.MaxBytesError
	var timeout net.Error
	switch {
	case errors.As(err, &tooBig):
		return errTooLarge
	case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return errBodyTimeout
	}
	return invalid("The request body could not be read.")
}

// readAll reads the body into a buffer of the length announced, when there is one, instead of growing it by copies.
func readAll(body io.Reader, length int64) ([]byte, error) {
	if length < 0 {
		return io.ReadAll(body)
	}
	buf := make([]byte, length)
	_, err := io.ReadFull(body, buf)
	return buf, err
}
