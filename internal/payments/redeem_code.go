package payments

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
	"unicode"
	"uuid"

	"axiom/internal/platform/typeid"
)

var (
	codePrefix       = typeid.MustPrefix("rdm")
	redemptionPrefix = typeid.MustPrefix("rdn")
)

// CodeID is a redeem code's ID.
type CodeID uuid.UUID

// TypeID writes the code's ID as the API does.
func (id CodeID) TypeID() string { return codePrefix.Encode(uuid.UUID(id)) }

// ParseCodeID reads a code's ID as the API writes it; a malformed one is ErrNotFound, like an unknown one.
func ParseCodeID(s string) (CodeID, error) {
	id, err := codePrefix.Decode(s)
	if err != nil {
		return CodeID{}, ErrNotFound
	}
	return CodeID(id), nil
}

// RedemptionID is the credit a redeemed code made, which is also the credit batch's ID.
type RedemptionID uuid.UUID

// TypeID writes the redemption's ID as the API does.
func (id RedemptionID) TypeID() string { return redemptionPrefix.Encode(uuid.UUID(id)) }

// RedeemCode is one code as the admin shows it. It holds no code: the text exists only in Issue's answer, at the moment
// it is made, and Hint is the four characters that let support find a code a user names.
type RedeemCode struct {
	ID         CodeID
	Site       uuid.UUID
	State      RedeemState
	Amount     int64 // billionths of a credit
	Note       string
	Hint       string
	ExpiresAt  *time.Time
	CreatedAt  time.Time
	CreatedBy  uuid.UUID
	RedeemedAt *time.Time
	DisabledAt *time.Time
	// Code is the plaintext, filled only by Issue and never read from the database.
	Code string
}

// normalizeCode is the canonical text of a code a user typed: upper case, without the dashes or whitespace the page
// shows. It refuses any character the alphabet does not have, or a length other than a code's, before any database work.
func normalizeCode(s string) (string, bool) {
	if len(s) > 4*redeemCodeLength {
		return "", false
	}
	b := make([]byte, 0, redeemCodeLength)
	for _, r := range s {
		switch {
		case unicode.IsSpace(r) || r == '-':
			continue
		case r >= 'a' && r <= 'z':
			r -= 'a' - 'A'
		}
		if !strings.ContainsRune(redeemAlphabet, r) {
			return "", false
		}
		if len(b) == redeemCodeLength {
			return "", false
		}
		b = append(b, byte(r)) //nolint:gosec // r is one of the alphabet's ASCII characters
	}
	return string(b), len(b) == redeemCodeLength
}

// displayCode is the code with the dashes the page shows, five characters a group.
func displayCode(canonical string) string {
	var b strings.Builder
	for i := 0; i < len(canonical); i += 5 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(canonical[i:min(i+5, len(canonical))])
	}
	return b.String()
}

// newCode makes one code's canonical text from the system's random source.
func newCode() (string, error) {
	var raw [redeemCodeLength]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("payments: random code: %w", err)
	}
	for i := range raw {
		raw[i] = redeemAlphabet[raw[i]%byte(len(redeemAlphabet))]
	}
	return string(raw[:]), nil
}

func hashOf(canonical string) []byte {
	sum := sha256.Sum256([]byte(canonical))
	return sum[:]
}

// codeIDOf is a code's TypeID, for an audit reason.
func codeIDOf(id uuid.UUID) string { return CodeID(id).TypeID() }

// ListCodes is one page of a site's codes, newest first, filtered by the state the page shows and by a note prefix. The
// cursor is the last row of the previous page; a further page exists exactly when cursor is not nil.
