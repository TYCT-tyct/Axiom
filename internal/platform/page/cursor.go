// Package page defines the shared time/UUID position of ordered database pages.
package page

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"
	"uuid"
)

// ErrBadCursor identifies text that cannot represent a valid database page position.
var ErrBadCursor = errors.New("page: malformed cursor")

// Database timestamps keep microseconds; retaining that precision prevents gaps between pages.
const cursorLen = 8 + 16

// String is the cursor as the console passes it back: opaque text, 32 characters.
func (c Cursor) String() string {
	var b [cursorLen]byte
	binary.BigEndian.PutUint64(b[:8], uint64(c.At.UnixMicro())) // #nosec G115 -- the bits are kept, and read back by ParseCursor
	copy(b[8:], c.ID[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// ParseCursor reads the stable time/UUID encoding. Forging a position does not widen the authorized scope.
func ParseCursor(s string) (Cursor, error) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(b) != cursorLen {
		return Cursor{}, ErrBadCursor
	}
	cursor := Cursor{
		At: time.UnixMicro(int64(binary.BigEndian.Uint64(b[:8]))).UTC(), // #nosec G115 -- the bits String kept
		ID: uuid.UUID(b[8:]),
	}
	if cursor.At.Year() < 1 || cursor.At.Year() > 9999 {
		return Cursor{}, ErrBadCursor
	}
	return cursor, nil
}

// Cursor is the last (time, UUID) tuple returned by an ordered page.
type Cursor struct {
	At time.Time
	ID uuid.UUID
}

// UpperBound chooses the tighter exclusive bound, including an empty ID to exclude every row at until.
func UpperBound(until time.Time, after *Cursor) Cursor {
	if after != nil && after.At.Before(until) {
		return *after
	}
	return Cursor{At: until}
}
