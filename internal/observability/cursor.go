package observability

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"
	"uuid"
)

// ErrBadCursor is text that is no cursor a page of ListOwn gave out.
var ErrBadCursor = errors.New("observability: malformed cursor")

// cursorLen is a cursor's bytes: the call's start in microseconds, the resolution the column keeps, and its ID.
const cursorLen = 8 + 16

// String is the cursor as the console passes it back: opaque text, 32 characters.
func (c Cursor) String() string {
	var b [cursorLen]byte
	binary.BigEndian.PutUint64(b[:8], uint64(c.At.UnixMicro())) // #nosec G115 -- the bits are kept, and read back by ParseCursor
	copy(b[8:], c.ID[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// ParseCursor reads what Cursor.String wrote, or returns ErrBadCursor. A cursor only marks a place in one account's
// calls: whoever forges one reads what the scope already lets them read, a page that starts elsewhere.
func ParseCursor(s string) (Cursor, error) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(b) != cursorLen {
		return Cursor{}, ErrBadCursor
	}
	return Cursor{
		At: time.UnixMicro(int64(binary.BigEndian.Uint64(b[:8]))).UTC(), // #nosec G115 -- the bits String kept
		ID: uuid.UUID(b[8:]),
	}, nil
}
