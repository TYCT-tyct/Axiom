package observability

import "axiom/internal/platform/page"

// Cursor is a call position in the shared time/UUID wire format.
type Cursor = page.Cursor

// ErrBadCursor identifies a malformed page cursor.
var ErrBadCursor = page.ErrBadCursor

// ParseCursor reads a call position in the shared wire format.
func ParseCursor(s string) (Cursor, error) { return page.ParseCursor(s) }
