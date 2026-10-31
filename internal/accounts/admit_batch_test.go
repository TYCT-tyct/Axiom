package accounts_test

import (
	"errors"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// TestAdmitBatchChecksEveryCallAndKeepsTheRest: one refused call does not roll back the others, because a refusal
// writes nothing; the admitted calls are handed to fn in one transaction of their account.
func TestAdmitBatchChecksEveryCallAndKeepsTheRest(t *testing.T) {
	t.Parallel()
	_, _, s, key := callSetup(t)
	c, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key)
	if err != nil {
		t.Fatal(err)
	}
	calls := []accounts.AdmitCall{
		{Caller: c, Request: accounts.AdmitRequest{Domain: apiDomain, ClientIP: client}},
		{Caller: c, Request: accounts.AdmitRequest{Domain: "not." + apiDomain, ClientIP: client}},
		{Caller: c, Request: accounts.AdmitRequest{Domain: apiDomain, ClientIP: client}},
	}
	var admissions []accounts.Admission
	var refusals []error
	err = s.AdmitBatch(t.Context(), calls, func(_ pg.Tx, as []accounts.Admission, rs []error) error {
		admissions, refusals = as, rs
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if refusals[0] != nil || refusals[2] != nil {
		t.Errorf("valid calls refused: %v, %v", refusals[0], refusals[2])
	}
	if !errors.Is(refusals[1], accounts.ErrUnknownKey) {
		t.Errorf("the call on an unknown domain: %v, want ErrUnknownKey", refusals[1])
	}
	tier0, tier2 := admissions[0].Tier(), admissions[2].Tier()
	preset0, ok0 := admissions[0].Preset()
	preset2, ok2 := admissions[2].Preset()
	if tier0 != tier2 || preset0 != preset2 || ok0 != ok2 {
		t.Errorf("the admitted calls read different terms: %+v and %+v", admissions[0], admissions[2])
	}
}
