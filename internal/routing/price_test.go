package routing

import (
	"testing"

	"axiom/internal/platform/alloctest"
)

// TestPriceParsingIsBounded: what an exponent asks for is never computed. A price whose exponent or number of digits
// would make a huge integer is refused by the bound check, which is string work; the big integer is made only from
// what that check accepted. The bytes a refused price allocates are measured in a child process, where no other
// goroutine of this test binary adds to the process's count: a million-digit integer would be hundreds of kilobytes.
func TestPriceParsingIsBounded(t *testing.T) {
	t.Parallel()
	huge := []string{"1e1000000", "1e-1000000", "1e999", "1e-999", "0.1e999999999"}
	t.Run("bounds", func(t *testing.T) { checkPriceBounds(t, huge) })
	t.Run("allocations", func(t *testing.T) { checkPriceAllocations(t, huge) })
}

// checkPriceBounds confirms that the huge exponents are refused by the bound check before any big integer is made.
func checkPriceBounds(t *testing.T, huge []string) {
	t.Helper()
	for _, number := range huge {
		if _, ok := readPrice([]byte(number), 9); ok {
			t.Errorf("%s passed the bound check", number)
		}
		if _, ok := PricePerMillion([]byte(number)); ok {
			t.Errorf("%s was accepted", number)
		}
	}
	for _, number := range []string{"1e3", "0e999", "0", "0.0000015"} {
		if _, ok := readPrice([]byte(number), 9); !ok {
			t.Errorf("%s was refused by the bound check", number)
		}
	}
}

// checkPriceAllocations holds each refused price to the bytes a short parse allocates.
func checkPriceAllocations(t *testing.T, huge []string) {
	t.Helper()
	var funcs []alloctest.Function
	for _, number := range huge {
		funcs = append(funcs, alloctest.Function{Name: number, F: func() { PricePerMillion([]byte(number)) }})
	}
	got := alloctest.Measure(t, funcs...)
	for _, number := range huge {
		if got[number].Bytes > 4<<10 {
			t.Errorf("%s: %v bytes allocated, want a few hundred", number, got[number].Bytes)
		}
	}
}
