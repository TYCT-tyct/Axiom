package accounts_test

import (
	"testing"
	"time"

	"axiom/internal/accounts"
)

// TestTOTPMatchesRFC6238: codes from RFC 6238 Appendix B (SHA-1, the 20-byte ASCII secret "12345678901234567890"),
// whose six-digit codes are the last six digits of the eight the table lists, are accepted for their own step, the
// one before and the one after, and refused two steps away. 1111111109 and 1111111111 fall either side of a step
// boundary (steps 0x23523EC and 0x23523ED), which gives independent codes of two adjacent steps.
func TestTOTPMatchesRFC6238(t *testing.T) {
	t.Parallel()
	secret := []byte("12345678901234567890")
	for _, c := range []struct {
		name string
		now  int64 // seconds since 1970
		code string
		step int64 // 0 for refused
	}{
		{"T=59", 59, "287082", 1},
		{"T=1111111109", 1111111109, "081804", 0x23523EC},
		{"T=1111111111", 1111111111, "050471", 0x23523ED},
		{"T=1234567890", 1234567890, "005924", 0x273EF07},
		{"T=2000000000", 2000000000, "279037", 0x3F940AA},
		{"T=20000000000", 20000000000, "353130", 0x27BC86AA},
		{"the next step's code", 1111111109, "050471", 0x23523ED},
		{"the step before's code, first second of a step", 1111111110, "081804", 0x23523EC},
		{"the step before's code, last second of a step", 1111111139, "081804", 0x23523EC},
		{"two steps after, first second", 1111111140, "081804", 0},
		{"two steps before", 1111111111 - 60, "050471", 0},
		{"a wrong code", 1111111109, "081805", 0},
	} {
		step, ok := accounts.TOTPStep(secret, c.code, time.Unix(c.now, 0))
		if ok != (c.step != 0) || step != c.step {
			t.Errorf("%s: step %d, %v; want %d", c.name, step, ok, c.step)
		}
	}
}
