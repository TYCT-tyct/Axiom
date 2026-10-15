package streamcheck

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"axiom/internal/platform/jsonx"
)

// Sample holds one sample's files to the convention in internal/protocol/README.md「录制样本」: meta.json names a
// source and a date, request.json is strict JSON, and check, the checker of the sample's format, accepts the stream.
// The samples test and the recorder both call it, so a recording that is written always passes the samples test.
func Sample(check func(io.Reader) error, request, stream, meta []byte) error {
	var m struct {
		Source string `json:"source"`
		Date   string `json:"date"`
	}
	if err := jsonx.Unmarshal(meta, &m); err != nil {
		return fmt.Errorf("meta.json: %w", err)
	}
	if m.Source == "" {
		return fmt.Errorf("meta.json: %w: no source", ErrInvalid)
	}
	if _, err := time.Parse(time.DateOnly, m.Date); err != nil {
		return fmt.Errorf("meta.json date: %w", err)
	}
	if err := jsonx.Validate(request); err != nil {
		return fmt.Errorf("request.json: %w", err)
	}
	if err := check(bytes.NewReader(stream)); err != nil {
		return fmt.Errorf("upstream.sse: %w", err)
	}
	return nil
}
