package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // reuse the strict streaming reader without materializing a request
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

var errTextFull = errors.New("input text counter is full")

// Text visits decoded string values of an already inspected request, never JSON keys or opaque payloads. It decodes
// at most byteLimit raw string bytes; remaining ordinary string bytes are returned for the four-byte fallback.
// The callback borrows a reused buffer and can stop the walk. Numbers and JSON framing are not model text.
func Text(body []byte, byteLimit int, text func([]byte) bool) (int64, error) {
	s := textReader{reader: reader{dec: jsontext.NewDecoder(bytes.NewBuffer(body))}, left: byteLimit, text: text}
	err := s.value()
	if errors.Is(err, errTextFull) {
		return 0, nil
	}
	if err == nil {
		_, err = s.dec.ReadToken()
		if errors.Is(err, io.EOF) {
			return s.fallback, nil
		}
	}
	if err == nil {
		err = errors.New("data after input text")
	}
	return 0, fmt.Errorf("%w: input text: %s", ErrInvalid, bodyFault(err))
}

type textReader struct {
	reader
	left     int
	text     func([]byte) bool
	fallback int64
	prefix   []byte
}

func (s *textReader) value() error {
	switch s.dec.PeekKind() {
	case '{':
		return s.eachMember(s.memberValue)
	case '[':
		return s.eachElement(s.value)
	case '"':
		return s.stringValue()
	}
	return s.token()
}

func (s *textReader) memberValue() error {
	// These are opaque data, not language. Excluding them also preserves the image/file estimates that account for
	// media separately. Match decoded keys, so escaping a key cannot turn a payload into chargeable text.
	if s.dec.PeekKind() == '"' && opaqueTextMember(s.name) {
		_, err := s.raw()
		return err
	}
	return s.value()
}

func opaqueTextMember(name []byte) bool {
	switch string(name) {
	case "data", "file_data", "image_url", "url", "signature", "encrypted_content":
		return true
	}
	return false
}

func (s *textReader) stringValue() error {
	v, err := s.raw()
	if err != nil {
		return err
	}
	end := stringPrefix(v, s.left)
	s.left -= end - 1
	s.fallback += int64(len(v) - 1 - end)
	if end == 1 {
		return nil
	}
	if end < len(v)-1 {
		s.prefix = append(s.prefix[:0], v[:end]...)
		s.prefix = append(s.prefix, '"')
		v = s.prefix
	}
	piece, err := s.unquote(v)
	if err != nil {
		return err
	}
	if !s.text(piece) {
		return errTextFull
	}
	return nil
}

// stringPrefix never splits UTF-8, a JSON escape, or a surrogate pair. Work and scratch space stay within the byte
// budget even when one string is much larger; its undecoded suffix keeps the old byte estimate.
func stringPrefix(v []byte, limit int) int {
	if len(v)-2 <= limit {
		return len(v) - 1
	}
	i := 1
	for i < len(v)-1 {
		_, size := utf8.DecodeRune(v[i:])
		if v[i] == '\\' {
			size = escapeSize(v[i:])
		}
		if i-1+size > limit {
			break
		}
		i += size
	}
	return i
}

func escapeSize(v []byte) int {
	if v[1] != 'u' {
		return 2
	}
	if (v[2] == 'd' || v[2] == 'D') && strings.ContainsRune("89aAbB", rune(v[3])) {
		return 12
	}
	return 6
}
