package accounts

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Passwords are hashed with argon2id at OWASP's baseline (19 MiB, 2 passes, 1 lane), the parameters written into the
// PHC string so that raising them later leaves stored hashes readable.
const (
	argonMemory  = 19 * 1024 // KiB
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16
	minPassword  = 12 // characters
	maxPassword  = 256
)

var phc = base64.RawStdEncoding

// hashPassword is password's argon2id PHC string with a new salt.
func hashPassword(password string) string {
	salt := make([]byte, saltLen)
	_, _ = rand.Read(salt) // crypto/rand.Read never fails
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		phc.EncodeToString(salt), phc.EncodeToString(key))
}

// dummyHash is checked against when the email names no user, so a missing user costs one hash like a wrong password.
var dummyHash = hashPassword("not anyone's password")

// passwordMatches reports whether password hashes to hash, under the parameters hash names.
func passwordMatches(hash, password string) bool {
	var version, memory, time int
	var threads uint8
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	if _, err := fmt.Sscanf(parts[2]+"$"+parts[3], "v=%d$m=%d,t=%d,p=%d", &version, &memory, &time, &threads); err != nil {
		return false
	}
	salt, err1 := phc.DecodeString(parts[4])
	key, err2 := phc.DecodeString(parts[5])
	if err1 != nil || err2 != nil || version != argon2.Version {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(time), uint32(memory), threads, uint32(len(key))) // #nosec G115 -- from our own PHC strings
	return subtle.ConstantTimeCompare(got, key) == 1
}

// passwordAllowed reports whether password is long enough to keep and short enough to hash.
func passwordAllowed(password string) bool {
	n := len([]rune(password))
	return n >= minPassword && n <= maxPassword
}
