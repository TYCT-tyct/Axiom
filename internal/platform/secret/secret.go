// Package secret keeps credential plaintext out of ordinary output and binds encrypted values to their storage location.
package secret

import (
	"fmt"
	"io"
	"log/slog"
	"strconv"
)

const redacted = "[REDACTED]"

// Secret keeps plaintext behind a closure because fmt's invalid-verb diagnostics can bypass methods
// and recursively print even unexported struct fields. The closure makes Secret deliberately not comparable: it cannot
// be a map key, structs holding it lose ==, and reflect.DeepEqual reports false for non-zero values; compare Reveal()
// results, and give go-cmp a cmp.Comparer.
type Secret struct {
	reveal func() string
}

// New puts plaintext behind the output boundary immediately, including before encryption.
func New(plaintext string) Secret { return Secret{reveal: func() string { return plaintext }} }

// Reveal makes crossing the output boundary explicit at the call site.
func (s Secret) Reveal() string {
	if s.reveal == nil {
		return ""
	}
	return s.reveal()
}

func (Secret) String() string { return redacted }

// GoString covers Go syntax output, which does not use String.
func (Secret) GoString() string { return redacted }

// Format ignores verbs and flags so alternate representations cannot disclose plaintext.
func (Secret) Format(state fmt.State, _ rune) {
	// fmt.State writes never fail; ignoring flags also prevents precision from hiding the redaction marker.
	_, _ = io.WriteString(state, redacted) //nolint:errcheck // fmt's output buffer cannot fail.
}

// LogValue keeps structured handlers from inspecting the underlying value.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON keeps both JSON APIs from inspecting the underlying value.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(redacted)), nil }

// MarshalText also protects serializers that do not use JSON methods.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
