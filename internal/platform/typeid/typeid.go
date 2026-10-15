// Package typeid writes and reads the IDs the API shows outside: a UUID behind a prefix that names its kind, such as
// site_01h455vb4pex5vsknk084sn02q, as the TypeID specification version 0.3.0 defines them
// (https://github.com/jetify-com/typeid/tree/main/spec). Inside, an ID is a plain uuid.UUID. Only the HTTP boundary
// converts, and the module that owns an entity defines its Prefix (internal/accounts/README.md「ID」).
//
// Decode accepts one spelling per ID, the one Encode writes, so a string can be compared, cached or rate-limited as
// it arrived without two spellings of one ID slipping past.
package typeid

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"uuid"
)

const (
	// alphabet is Crockford's base32 in lower case: no i, l, o or u. Digit values are positions in it.
	alphabet     = "0123456789abcdefghjkmnpqrstvwxyz"
	suffixLen    = 26 // 130 bits, of which the first two are always zero
	maxPrefixLen = 63
	noDigit      = 0xff
)

// digits maps a byte to its value in alphabet, or noDigit.
var digits = func() (t [256]uint8) {
	for i := range t {
		t[i] = noDigit
	}
	for i := range len(alphabet) {
		t[alphabet[i]] = uint8(i)
	}
	return t
}()

// ErrInvalid is every reason Decode refuses a string. A caller answers all of them alike, as it answers an ID that
// names nothing: the reason would only tell a client how to probe, and the error repeats no input.
var ErrInvalid = errors.New("typeid: not an ID of this kind")

// Prefix is the kind of an ID: lower-case ASCII letters and underscores, up to 63, starting and ending with a
// letter. The zero value is the empty prefix, which the specification allows and which writes no separator; a module
// defines a non-empty one.
type Prefix struct {
	head string // the prefix and its separator, empty for the empty prefix
}

// ParsePrefix checks s against the specification's rule for a prefix.
func ParsePrefix(s string) (Prefix, error) {
	if s == "" {
		return Prefix{}, nil
	}
	letter := func(c byte) bool { return 'a' <= c && c <= 'z' }
	ok := len(s) <= maxPrefixLen && letter(s[0]) && letter(s[len(s)-1])
	for i := 0; ok && i < len(s); i++ {
		ok = letter(s[i]) || s[i] == '_'
	}
	if !ok {
		return Prefix{}, fmt.Errorf("typeid: prefix %q is not up to %d letters a-z and underscores, starting and ending with a letter", s, maxPrefixLen)
	}
	return Prefix{s + "_"}, nil
}

// MustPrefix is ParsePrefix for the constant a module declares, so that a bad one stops the program at start-up.
func MustPrefix(s string) Prefix {
	p, err := ParsePrefix(s)
	if err != nil {
		panic(err)
	}
	return p
}

// Encode writes id behind the prefix. Any UUID encodes, not only version 7.
func (p Prefix) Encode(id uuid.UUID) string {
	var buf [maxPrefixLen + 1 + suffixLen]byte
	n := copy(buf[:], p.head) + suffixLen
	hi, lo := binary.BigEndian.Uint64(id[:8]), binary.BigEndian.Uint64(id[8:])
	for i := n - 1; i >= n-suffixLen; i-- { // five bits at a time from the right, through the 128-bit pair
		buf[i] = alphabet[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(buf[:n])
}

// Decode reads an ID that carries exactly this prefix.
func (p Prefix) Decode(s string) (uuid.UUID, error) {
	suffix, ok := strings.CutPrefix(s, p.head)
	if !ok || len(suffix) != suffixLen {
		return uuid.UUID{}, ErrInvalid
	}
	var hi, lo uint64
	for i := range suffixLen {
		d := digits[suffix[i]]
		// The first digit stands for bits 125 to 129 and only 125 to 127 exist: above 7 the value is over 128 bits.
		if d == noDigit || i == 0 && d > 7 {
			return uuid.UUID{}, ErrInvalid
		}
		hi = hi<<5 | lo>>59
		lo = lo<<5 | uint64(d)
	}
	var id uuid.UUID
	binary.BigEndian.PutUint64(id[:8], hi)
	binary.BigEndian.PutUint64(id[8:], lo)
	return id, nil
}
