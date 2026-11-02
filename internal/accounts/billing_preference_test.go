package accounts_test

import (
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestBillingPreferenceFollowsTheSiteUntilTheAccountChooses: the admission snapshot reads the account's choice, then
// the site's default, then the code default, without another query in the gateway.
//
//nolint:cyclop // one admission-to-console journey: each step is a different source of the same preference.
func TestBillingPreferenceFollowsTheSiteUntilTheAccountChooses(t *testing.T) {
	t.Parallel()
	db, database, s, key := callSetup(t)
	c, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key)
	if err != nil {
		t.Fatal(err)
	}
	admit := func() string {
		t.Helper()
		var got accounts.Admission
		if err := s.Admit(t.Context(), c, accounts.AdmitRequest{Domain: apiDomain, ClientIP: client},
			func(_ pg.Tx, a accounts.Admission) error { got = a; return nil }); err != nil {
			t.Fatal(err)
		}
		return got.BillingPreference()
	}
	if got := admit(); got != accounts.BillingSubscriptionFirst {
		t.Errorf("with nothing set = %q, want the code default", got)
	}
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(
		`INSERT INTO accounts.site_billing_preferences (site_id, preference) VALUES (:s1, 'wallet_first')`)); err != nil {
		t.Fatal(err)
	}
	if got := admit(); got != accounts.BillingWalletFirst {
		t.Errorf("with the site set = %q, want wallet_first", got)
	}
	read := func() accounts.BillingPreference {
		t.Helper()
		var p accounts.BillingPreference
		if err := db.ReadTx(t.Context(), pg.AccountScope(a1), func(tx pg.Tx) error {
			var err error
			p, err = accounts.GetBillingPreference(t.Context(), tx, accounts.AccountID(a1))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := read(); p.Preference != accounts.BillingWalletFirst || p.Own || p.SiteDefault != accounts.BillingWalletFirst {
		t.Errorf("console read with the site set = %+v", p)
	}
	if err := db.Tx(t.Context(), pg.AccountScope(a1), func(tx pg.Tx) error {
		own := accounts.BillingWalletOnly
		return accounts.SetBillingPreference(t.Context(), tx, accounts.AccountID(a1), &own)
	}); err != nil {
		t.Fatal(err)
	}
	if got := admit(); got != accounts.BillingWalletOnly {
		t.Errorf("with the account set = %q, want wallet_only", got)
	}
	if err := db.Tx(t.Context(), pg.AccountScope(a1), func(tx pg.Tx) error {
		return accounts.SetBillingPreference(t.Context(), tx, accounts.AccountID(a1), nil)
	}); err != nil {
		t.Fatal(err)
	}
	if got := admit(); got != accounts.BillingWalletFirst {
		t.Errorf("after clearing the account choice = %q, want the site default", got)
	}
}
