package declared

import (
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

func TestReadBalanceKeepsAnOverdraft(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want adapter.Amount
		ok   bool
	}{
		{`"12.345"`, "12.345", true},
		{`-3`, "-3", true},
		{`"0"`, "0", true},
		{`"1e3"`, "", false},
		{`"abc"`, "", false},
		{`true`, "", false},
		{`null`, "", false},
	} {
		got, ok := readBalance(jsonx.Raw(tc.raw))
		if got != tc.want || ok != tc.ok {
			t.Errorf("readBalance(%s) = %q, %v; want %q, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}
