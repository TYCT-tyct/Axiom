package accounts

import (
	"crypto/sha256"
	"math/bits"
	"strconv"
	"strings"
)

// Open registration's human check is a proof of work solved by the page: the server keeps no challenge, because the
// site, the address and the nonce are enough to recompute it, and a nonce that works for one address does not work for
// another. The prefix names the scheme, so a later version can change the work without accepting the old answers.
const proofPrefix = "axiom-registration-v1"

// proofWorked reports whether nonce proves the work for this site and normalized email: the SHA-256 of the three,
// with at least bits leading zero bits.
func proofWorked(site SiteID, email, nonce string, bitsWanted int) bool {
	if len(nonce) == 0 || len(nonce) > 20 || bitsWanted < 1 || bitsWanted > 64 {
		return false
	}
	if _, err := strconv.ParseUint(nonce, 10, 64); err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(proofPrefix + "\n" + site.TypeID() + "\n" + email + "\n" + nonce))
	zero := 0
	for _, b := range sum {
		if b == 0 {
			zero += 8
			if zero >= bitsWanted {
				return true
			}
			continue
		}
		return zero+bits.LeadingZeros8(b) >= bitsWanted
	}
	return zero >= bitsWanted
}

// domainOf returns the lower-case domain of an already normalized address.
func domainOf(email string) string {
	_, domain, ok := strings.CutLast(email, "@")
	if !ok {
		return ""
	}
	return domain
}
