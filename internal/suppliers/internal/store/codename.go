package store

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// Codename is a name for a group that tells nothing of the supplier: a letter and eight characters from the alphabet of
// base 32, which a person can say aloud. The format is a decision of this code until the product names one. Both a
// person's first selection and the projection's automatic one use it, so a group gets the same kind of name either way.
func Codename() string {
	b := make([]byte, 5)
	rand.Read(b)                                                       // Since Go 1.24 crypto/rand.Read never returns an error; it crashes the program instead.
	return "g" + strings.ToLower(base32.StdEncoding.EncodeToString(b)) // five bytes are eight characters, unpadded
}
