package egress

import (
	"errors"
	"fmt"
	"io"
	"math"
)

// ErrBodyTooLarge distinguishes an oversized response from an upstream read failure.
var ErrBodyTooLarge = errors.New("egress: response body exceeds limit")

// ReadBody probes one byte beyond the limit so a truncated payload cannot appear complete.
// The caller still owns closing the response body, including on failure.
func ReadBody(body io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 || maxBytes == math.MaxInt64 {
		return nil, errors.New("egress read response body: invalid byte limit")
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("egress read response body: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrBodyTooLarge
	}
	return data, nil
}
