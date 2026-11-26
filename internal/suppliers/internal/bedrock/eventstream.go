package bedrock

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"

	"axiom/internal/platform/jsonx"
)

// Bedrock streams an answer in AWS's event-stream framing
// (https://docs.aws.amazon.com/AmazonS3/latest/API/RESTSelectObjectAppendix.html, the same encoding): each message is
// its total length, its headers' length and a CRC32 of those eight bytes, then the headers, the payload and a CRC32 of
// all before it. A chunk event's payload is {"bytes": base64 of one of Anthropic's stream events}; an exception
// message names its type in a header and ends the stream. sse turns them into Anthropic's SSE as they are read, one
// message at a time, so nothing is held back.

const (
	// prelude is the total length, the headers' length and their CRC.
	prelude = 12
	// maxMessage bounds a message: an event of Anthropic's is at most a few KiB, a tool's input delta included.
	maxMessage = 1 << 20
)

var (
	errFraming = errors.New("bedrock: the event stream is not framed as one")
	// errStreamFailed ends a stream Bedrock stopped with an exception, after the error event that says so.
	errStreamFailed = io.EOF
)

// sse reads an event stream as Anthropic's SSE.
type sse struct {
	src     io.ReadCloser
	r       *bufio.Reader
	message []byte       // the message being read, kept for the next
	out     bytes.Buffer // SSE not read yet
	err     error
}

func newSSE(src io.ReadCloser) *sse { return &sse{src: src, r: bufio.NewReader(src)} }

// Read implements io.Reader.
func (s *sse) Read(p []byte) (int, error) {
	for s.out.Len() == 0 && s.err == nil {
		s.err = s.next()
	}
	if s.out.Len() > 0 {
		return s.out.Read(p)
	}
	return 0, s.err
}

// Close implements io.Closer.
func (s *sse) Close() error { return s.src.Close() }

// next reads one message and writes what it says. The end of the stream between messages is io.EOF; anywhere else it,
// a length out of bounds or a CRC that does not match is a framing error, which the gateway reads as a broken stream.
func (s *sse) next() error {
	var head [prelude]byte
	if _, err := io.ReadFull(s.r, head[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return io.EOF
		}
		return errFraming
	}
	total, headers := binary.BigEndian.Uint32(head[0:4]), binary.BigEndian.Uint32(head[4:8])
	if crc32.ChecksumIEEE(head[:8]) != binary.BigEndian.Uint32(head[8:12]) || total < prelude+4 || total > maxMessage || headers > total-prelude-4 {
		return errFraming
	}
	if cap(s.message) < int(total) {
		s.message = make([]byte, total)
	}
	m := s.message[:total]
	copy(m, head[:])
	if _, err := io.ReadFull(s.r, m[prelude:]); err != nil {
		return errFraming
	}
	if crc32.ChecksumIEEE(m[:total-4]) != binary.BigEndian.Uint32(m[total-4:]) {
		return errFraming
	}
	h, ok := readHeaders(m[prelude : prelude+headers])
	if !ok {
		return errFraming
	}
	payload := m[prelude+headers : total-4]
	switch {
	case h.messageType == "event" && h.eventType == "chunk":
		return s.chunk(payload)
	case h.messageType == "exception" || h.messageType == "error":
		s.failed(h.exceptionType)
		return errStreamFailed
	}
	return nil // an event of another type carries no event of Anthropic's
}

// chunk writes the Anthropic event a chunk carries as one SSE event named by its type. A line break inside the JSON,
// which Bedrock does not send, would still be its own data line, as SSE joins them.
func (s *sse) chunk(payload []byte) error {
	var c struct {
		Bytes []byte `json:"bytes"`
	}
	if jsonx.Unmarshal(payload, &c) != nil {
		return errFraming
	}
	var event struct {
		Type string `json:"type"`
	}
	if jsonx.Unmarshal(c.Bytes, &event) != nil || event.Type == "" {
		return errFraming
	}
	s.out.WriteString("event: " + event.Type + "\n")
	for line := range bytes.Lines(c.Bytes) {
		s.out.WriteString("data: ")
		s.out.Write(bytes.TrimRight(line, "\r\n"))
		s.out.WriteByte('\n')
	}
	s.out.WriteByte('\n')
	return nil
}

// failed writes Anthropic's error event for an exception Bedrock ended the stream with. Only the exception's type is
// said: its message is Bedrock's words, which can quote the request.
func (s *sse) failed(exception string) {
	kind := "api_error"
	switch exception {
	case "throttlingException", "serviceUnavailableException":
		kind = "overloaded_error"
	case "validationException":
		kind = "invalid_request_error"
	}
	s.out.WriteString(`event: error` + "\n" + `data: {"type":"error","error":{"type":"` + kind + `","message":"Bedrock ended the stream: ` + safe(exception) + `"}}` + "\n\n")
}

// safe is an exception's type as it may go in a JSON string: letters only.
func safe(s string) string {
	for i := range len(s) {
		if c := s[i]; !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z') {
			return "exception"
		}
	}
	return s
}

// header names of a message that say what it is.
type messageHeaders struct {
	messageType, eventType, exceptionType string
}

// readHeaders reads a message's headers: each a name's length (1 byte) and name, a type (1 byte) and a value whose
// length the type fixes or, for bytes and strings, a 2-byte length gives. Only the string headers that say what the
// message is are kept.
func readHeaders(b []byte) (messageHeaders, bool) {
	var h messageHeaders
	for len(b) > 0 {
		n := int(b[0])
		if len(b) < 1+n+1 {
			return h, false
		}
		name, kind := string(b[1:1+n]), b[1+n]
		b = b[2+n:]
		size, ok := valueSize(kind, b)
		if !ok || len(b) < size {
			return h, false
		}
		value := b[:size]
		b = b[size:]
		if kind != 7 {
			continue
		}
		switch text := string(value[2:]); name {
		case ":message-type":
			h.messageType = text
		case ":event-type":
			h.eventType = text
		case ":exception-type":
			h.exceptionType = text
		}
	}
	return h, true
}

// valueSize is how many bytes a header value of the type takes, its length prefix included.
func valueSize(kind byte, b []byte) (int, bool) {
	switch kind {
	case 0, 1: // true, false
		return 0, true
	case 2: // byte
		return 1, true
	case 3: // short
		return 2, true
	case 4: // integer
		return 4, true
	case 5, 8: // long, timestamp
		return 8, true
	case 6, 7: // bytes, string
		if len(b) < 2 {
			return 0, false
		}
		return 2 + int(binary.BigEndian.Uint16(b)), true
	case 9: // uuid
		return 16, true
	}
	return 0, false
}
