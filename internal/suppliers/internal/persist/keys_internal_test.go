package persist

import (
	"testing"
	"uuid"

	"axiom/internal/suppliers/internal/adapter"
)

func TestKeyNameIsShortAndStable(t *testing.T) {
	t.Parallel()
	id := uuid.MustParse("01a19ee3-346c-7816-a476-ab54348feb67")
	name := keyName(id, "g")
	if len(name) != 23 || name[:6] != "axiom-" {
		t.Fatalf("keyName = %q, want the axiom- marker and 23 characters", name)
	}
	if again := keyName(id, "g"); again != name {
		t.Fatalf("keyName changed: %q then %q", name, again)
	}
	if other := keyName(id, "h"); other == name {
		t.Fatalf("two groups of one supplier share the key name %q", name)
	}
}

func TestCredentialStatusMapsTheKeyStatus(t *testing.T) {
	t.Parallel()
	cases := map[adapter.KeyStatus]string{
		adapter.KeyActive:         "active",
		adapter.KeyDisabled:       "upstream_disabled",
		adapter.KeyQuotaExhausted: "quota_exhausted",
		adapter.KeyExpired:        "expired",
	}
	for status, want := range cases {
		if got := credentialStatus(status); got != want {
			t.Errorf("credentialStatus(%v) = %q, want %q", status, got, want)
		}
	}
}
