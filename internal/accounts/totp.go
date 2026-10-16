package accounts

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 6238 TOTP, which authenticator apps implement, is HMAC-SHA1; its strength here is the HMAC's
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"time"
)

// TOTP as RFC 6238 defines it and authenticator apps implement it: HMAC-SHA1, 30-second steps, 6 digits, a 20-byte
// secret. A code is accepted for the step before and after the current one, for clock drift, and each step only once.
const (
	totpPeriod = 30
	totpDigits = 1_000_000
	totpDrift  = 1 // steps either side
)

var totpBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPSecret is a new random secret.
func newTOTPSecret() []byte {
	secret := make([]byte, 20)
	_, _ = rand.Read(secret) // crypto/rand.Read never fails
	return secret
}

// totpCode is the code of step.
func totpCode(secret []byte, step int64) string {
	mac := hmac.New(sha1.New, secret)
	mac.Write(binary.BigEndian.AppendUint64(nil, uint64(step))) // #nosec G115 -- a step since 1970 is positive
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%totpDigits)
}

// totpStep is the step code is the code of, within the drift around now, or false.
func totpStep(secret []byte, code string, now time.Time) (int64, bool) {
	current := now.Unix() / totpPeriod
	for step := current - totpDrift; step <= current+totpDrift; step++ {
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// totpURI is what an authenticator app scans to add the secret for account.
func totpURI(secret []byte, account string) string {
	return "otpauth://totp/" + url.PathEscape("Axiom:"+account) + "?" + url.Values{
		"secret": {totpBase32.EncodeToString(secret)}, "issuer": {"Axiom"},
	}.Encode()
}
