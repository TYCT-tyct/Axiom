package gateway

import (
	"strings"
	"testing"
	"uuid"
)

// TestIdentityIsOpaque: the identifier passed upstream in place of the client's value is stable for one account and
// value, differs between accounts, values and secrets, is the length Prepare takes, and shows nothing of the value.
func TestIdentityIsOpaque(t *testing.T) {
	t.Parallel()
	of := func(secret string, account uuid.UUID, v string) string {
		x := new(Executor)
		x.IdentitySecret = []byte(secret)
		return x.identity(account)(v)
	}
	a, b := uuid.NewV7(), uuid.NewV7()
	got := of("0123456789abcdef", a, "default")
	if got != of("0123456789abcdef", a, "default") {
		t.Error("the same account and value gave two identifiers")
	}
	for name, other := range map[string]string{
		"another account": of("0123456789abcdef", b, "default"),
		"another value":   of("0123456789abcdef", a, "other"),
		"another secret":  of("fedcba9876543210", a, "default"),
	} {
		if other == got {
			t.Errorf("%s gave the same identifier", name)
		}
	}
	if len(got) != 32 || strings.Contains(got, "default") {
		t.Errorf("identifier %q: want 32 characters that show nothing of the value", got)
	}
}
