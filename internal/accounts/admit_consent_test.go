package accounts_test

import (
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestAdmissionCarriesTheOwnersConsent: the admission reads the newest version of each legal document the account's
// owner accepted, in the same transaction as the rest of the key's row, for the call log; none accepted reads as
// zero, and a version published but not yet accepted is not taken for consent.
func TestAdmissionCarriesTheOwnersConsent(t *testing.T) {
	t.Parallel()
	_, database, s, key := callSetup(t)
	c, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key)
	if err != nil {
		t.Fatal(err)
	}
	admit := func() (accounts.Consent, accounts.Consent) {
		t.Helper()
		var one, batch accounts.Consent
		if err := s.Admit(t.Context(), c, accounts.AdmitRequest{Domain: apiDomain, ClientIP: client},
			func(_ pg.Tx, a accounts.Admission) error { one = a.Consent(); return nil }); err != nil {
			t.Fatal(err)
		}
		if err := s.AdmitBatch(t.Context(), []accounts.AdmitCall{{Caller: c, Request: accounts.AdmitRequest{Domain: apiDomain, ClientIP: client}}},
			func(_ pg.Tx, a []accounts.Admission, _ []error) error { batch = a[0].Consent(); return nil }); err != nil {
			t.Fatal(err)
		}
		return one, batch
	}
	run := func(statements ...string) {
		t.Helper()
		for _, statement := range statements {
			if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(statement)); err != nil {
				t.Fatalf("%s: %v", statement, err)
			}
		}
	}
	terms := func(locale string, version int) accounts.DocumentVersion {
		return accounts.DocumentVersion{Kind: accounts.LegalTerms, Locale: locale, Version: version}
	}
	for _, step := range []struct {
		name       string
		statements []string
		want       accounts.Consent
	}{
		{"nothing published", nil, accounts.Consent{}},
		{"published, not accepted", []string{
			`INSERT INTO accounts.legal_documents (site_id, kind, locale, version, body) VALUES (:s1, 'terms', 'zh', 1, 'T1'), (:s1, 'privacy', 'en', 1, 'P1')`,
		}, accounts.Consent{}},
		{"both accepted", []string{
			`INSERT INTO accounts.legal_acceptances (id, site_id, account_id, user_id, kind, locale, version) VALUES
				(gen_random_uuid(), :s1, :a1, :u1, 'terms', 'zh', 1), (gen_random_uuid(), :s1, :a1, :u1, 'privacy', 'en', 1)`,
		}, accounts.Consent{Terms: terms("zh", 1), Privacy: accounts.DocumentVersion{Kind: accounts.LegalPrivacy, Locale: "en", Version: 1}}},
		{"a new version not yet accepted", []string{
			`INSERT INTO accounts.legal_documents (site_id, kind, locale, version, body) VALUES (:s1, 'terms', 'en', 2, 'T2')`,
		}, accounts.Consent{Terms: terms("zh", 1), Privacy: accounts.DocumentVersion{Kind: accounts.LegalPrivacy, Locale: "en", Version: 1}}},
		{"the new version accepted", []string{
			`INSERT INTO accounts.legal_acceptances (id, site_id, account_id, user_id, kind, locale, version) VALUES (gen_random_uuid(), :s1, :a1, :u1, 'terms', 'en', 2)`,
		}, accounts.Consent{Terms: terms("en", 2), Privacy: accounts.DocumentVersion{Kind: accounts.LegalPrivacy, Locale: "en", Version: 1}}},
	} {
		run(step.statements...)
		one, batch := admit()
		if one != step.want || batch != step.want {
			t.Errorf("%s: admitted with %+v, batch %+v; want %+v", step.name, one, batch, step.want)
		}
	}
}
