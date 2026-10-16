package catalog

import "testing"

func TestPlainDecimal(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"3e-06": "0.000003", "0.000003": "0.000003", "3E-6": "0.000003", "1.25e-07": "0.000000125", "2.8e-08": "0.000000028",
		"1e-05": "0.00001", "0.0": "0", "0": "0", "0.50": "0.5", "10": "10", "1e3": "1000", "1.5e1": "15", "0.0000002574": "0.0000002574",
		"1e-15": "0.000000000000001", "100.5e-2": "1.005", "00.5": "0.5", "1e+2": "100",
		"1.000000000000000000e-5": "0.00001", // trailing zeros are not decimals
	} {
		got, err := plainDecimal(in)
		if err != nil || got != want {
			t.Errorf("plainDecimal(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "-1", "-1e-6", "+1", "1e-31", "1e31", "1e", "e5", "1e-16", "0.0000000000000001", "1.", ".5", "NaN", "Infinity", "0x10", "1 ", "1_0", "1e1.5",
	} {
		if got, err := plainDecimal(in); err == nil {
			t.Errorf("plainDecimal(%q) = %q; want an error", in, got)
		}
	}
}

// FuzzPlainDecimal: whatever is accepted is plain digits that read back as the same value when the exponent is applied.
func FuzzPlainDecimal(f *testing.F) {
	for _, s := range []string{"3e-06", "1.25E-7", "0.5", "7e5", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := plainDecimal(s)
		if err != nil {
			return
		}
		again, err := plainDecimal(got)
		if err != nil || again != got {
			t.Fatalf("plainDecimal(%q) = %q, which is not stable: %q, %v", s, got, again, err)
		}
	})
}
