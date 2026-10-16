package accounts

import (
	"crypto/rand"
	"hash/crc32"
	"math/big"
	"regexp"
	"strings"
)

// An API key is the site's prefix, 43 base62 characters of 32 random bytes, and 6 base62 characters of the CRC32 of
// what comes before them (README.md「格式与哈希」). The checksum lets a mistyped or made-up key be refused without
// hashing it or asking the database; it is not a secret and proves nothing.
const (
	base62      = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	randomLen   = 43 // 62^43 > 2^256 > 62^42
	checksumLen = 6  // 62^6 > 2^32
)

// sitePrefixPattern is a site's key prefix: up to 16 lower-case letters, digits and hyphens, ending with a hyphen.
// Base62 has no hyphen, so a key's prefix is everything up to its last one.
var sitePrefixPattern = regexp.MustCompile(`^[a-z0-9-]{1,15}-$`)

// newKeyText writes a new key behind prefix, which the site's row guarantees is valid.
func newKeyText(prefix string) string {
	var random [32]byte
	_, _ = rand.Read(random[:]) // crypto/rand.Read never fails
	digits := []byte(strings.Repeat("0", randomLen))
	n, base, digit := new(big.Int).SetBytes(random[:]), big.NewInt(62), new(big.Int)
	for i := randomLen - 1; n.Sign() > 0; i-- {
		n.DivMod(n, base, digit)
		digits[i] = base62[digit.Int64()]
	}
	body := prefix + string(digits)
	return body + checksum(body)
}

// wellFormedKey reports whether raw has a key's shape and a checksum that matches.
func wellFormedKey(raw string) bool {
	cut := strings.LastIndexByte(raw, '-') + 1
	if !sitePrefixPattern.MatchString(raw[:cut]) || len(raw)-cut != randomLen+checksumLen {
		return false
	}
	for i := cut; i < len(raw); i++ {
		if strings.IndexByte(base62, raw[i]) < 0 {
			return false
		}
	}
	body := raw[:len(raw)-checksumLen]
	return raw[len(body):] == checksum(body)
}

// checksum is the CRC32 of body in base62, left-padded with zeros.
func checksum(body string) string {
	var digits [checksumLen]byte
	v := crc32.ChecksumIEEE([]byte(body))
	for i := checksumLen - 1; i >= 0; i-- {
		digits[i] = base62[v%62]
		v /= 62
	}
	return string(digits[:])
}
