package accounts_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

const (
	// placeholderTerms and placeholderPrivacy are fixed test text, never real terms.
	placeholderTerms   = "Placeholder terms of service for tests: this is not a real agreement."
	placeholderPrivacy = "Placeholder privacy policy for tests: this is not a real policy."
)

// TestLegalDocumentVersionsAndAcceptance: publishing adds a version per kind and locale, the current one is the latest
// of each pair, and an acceptance names exactly one current version per kind and keeps its first time. A stale or
// extra version is refused, and the audit entry carries the version and a digest rather than the text.
//
//nolint:cyclop,gocognit // one publication-to-acceptance flow: each step is the mechanism under test.
func TestLegalDocumentVersionsAndAcceptance(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	s := node(t, db, hashKeys(t, 1), nil)
	site, account, user := accounts.SiteID(s1), accounts.AccountID(a1), accounts.UserID(u1)
	publish := func(kind, locale, body string) accounts.LegalDocument {
		t.Helper()
		var document accounts.LegalDocument
		if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			var err error
			document, err = s.PublishLegalDocument(t.Context(), tx, system, site, kind, locale, body, "test")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return document
	}

	terms1 := publish(accounts.LegalTerms, accounts.LegalLocaleZH, placeholderTerms)
	if terms1.Kind != accounts.LegalTerms || terms1.Locale != accounts.LegalLocaleZH || terms1.Version != 1 || terms1.PublishedAt.IsZero() {
		t.Fatalf("the first terms version: %+v", terms1)
	}
	privacy1 := publish(accounts.LegalPrivacy, accounts.LegalLocaleZH, placeholderPrivacy)
	if privacy1.Kind != accounts.LegalPrivacy || privacy1.Locale != accounts.LegalLocaleZH || privacy1.Version != 1 {
		t.Fatalf("the first privacy version: %+v", privacy1)
	}
	terms2 := publish(accounts.LegalTerms, accounts.LegalLocaleZH, placeholderTerms+" Version two.")
	if terms2.Version != 2 { // versions count per kind and locale, not per site
		t.Fatalf("the second terms version: %+v", terms2)
	}
	termsEN := publish(accounts.LegalTerms, accounts.LegalLocaleEN, "English placeholder terms for tests.")
	if termsEN.Version != 1 { // the English stream starts at 1 independently
		t.Fatalf("the first English terms version: %+v", termsEN)
	}

	current, err := s.CurrentLegalDocuments(t.Context(), site)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 3 || current[0].Kind != accounts.LegalPrivacy || current[1].Locale != accounts.LegalLocaleEN || current[2].Version != 2 {
		t.Fatalf("current documents: %+v", current)
	}
	if none, err := s.CurrentLegalDocuments(t.Context(), accounts.SiteID(s2)); err != nil || len(none) != 0 {
		t.Fatalf("a site without documents: %+v, %v", none, err)
	}
	var history []accounts.LegalDocument
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		var more bool
		var err error
		history, more, err = s.LegalDocumentHistory(t.Context(), tx, site, nil, 200)
		if err == nil && more {
			t.Error("a site with four documents has a next page")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 {
		t.Fatalf("history: %+v", history)
	}
	var pairVersions []string
	for _, document := range history {
		pairVersions = append(pairVersions, document.Kind+":"+document.Locale+":"+itoa(document.Version))
	}
	if !slices.Equal(pairVersions, []string{"privacy:zh:1", "terms:en:1", "terms:zh:2", "terms:zh:1"}) {
		t.Fatalf("history: %v", pairVersions)
	}

	p := accounts.PrincipalForTest(site, account, user)
	pending, err := s.PendingLegalDocuments(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || pending[0].Kind != accounts.LegalPrivacy || pending[1].Version != 2 {
		t.Fatalf("pending before accepting: %+v", pending)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, pending[:1]); !errors.Is(err, accounts.ErrConsentRequired) {
		t.Fatalf("accepting half the documents: %v, want ErrConsentRequired", err)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, pending); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, db, platform, "SELECT kind || ' ' || locale || ' ' || version FROM accounts.legal_acceptances ORDER BY kind, locale"); !slices.Equal(got, []string{"privacy zh 1", "terms zh 2"}) {
		t.Fatalf("acceptances: %v", got)
	}
	firstAccepted := texts(t, db, platform, "SELECT accepted_at::text FROM accounts.legal_acceptances WHERE kind = 'terms' AND locale = 'zh'")[0]
	if err := s.AcceptLegalDocuments(t.Context(), p, pending); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, db, platform, "SELECT accepted_at::text FROM accounts.legal_acceptances WHERE kind = 'terms' AND locale = 'zh'"); len(got) != 1 || got[0] != firstAccepted {
		t.Errorf("a retried acceptance: %v, want one row at %s", got, firstAccepted)
	}
	if pending, err := s.PendingLegalDocuments(t.Context(), p); err != nil || len(pending) != 0 {
		t.Fatalf("pending after accepting: %+v, %v", pending, err)
	}

	publish(accounts.LegalTerms, accounts.LegalLocaleZH, placeholderTerms+" Version three.")
	pending, err = s.PendingLegalDocuments(t.Context(), p)
	if err != nil || len(pending) != 1 || pending[0].Version != 3 || pending[0].Locale != accounts.LegalLocaleZH {
		t.Fatalf("pending after a new version: %+v, %v", pending, err)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleZH, Version: 2}}); !errors.Is(err, accounts.ErrConsentRequired) {
		t.Errorf("accepting the superseded version: %v, want ErrConsentRequired", err)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleZH, Version: 3}, {Kind: accounts.LegalPrivacy, Locale: accounts.LegalLocaleZH, Version: 1}, {Kind: "marketing", Locale: accounts.LegalLocaleZH, Version: 1}}); !errors.Is(err, accounts.ErrConsentRequired) {
		t.Errorf("accepting an extra kind: %v, want ErrConsentRequired", err)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleZH, Version: 3}, {Kind: accounts.LegalPrivacy, Locale: accounts.LegalLocaleZH, Version: 1}}); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.PendingLegalDocuments(t.Context(), p); err != nil || len(pending) != 0 {
		t.Fatalf("pending after accepting the new version: %+v, %v", pending, err)
	}

	for _, c := range []struct {
		name, kind, locale, body, field string
	}{
		{"an unknown kind", "marketing", accounts.LegalLocaleZH, "Text.", "kind"},
		{"an unknown locale", accounts.LegalTerms, "fr", "Text.", "locale"},
		{"an empty body", accounts.LegalTerms, accounts.LegalLocaleZH, "", "body"},
		{"a body past the limit", accounts.LegalTerms, accounts.LegalLocaleZH, strings.Repeat("x", 65537), "body"},
	} {
		err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			_, err := s.PublishLegalDocument(t.Context(), tx, system, site, c.kind, c.locale, c.body, "test")
			return err
		})
		var refusal *accounts.LegalDocumentError
		if !errors.As(err, &refusal) || refusal.Field != c.field {
			t.Errorf("%s: %v, want LegalDocumentError on %s", c.name, err, c.field)
		}
	}
	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := s.PublishLegalDocument(t.Context(), tx, system, accounts.SiteID(uuid.NewV7()), accounts.LegalTerms, accounts.LegalLocaleZH, "Text.", "test")
		return err
	})
	if !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("publishing for an unknown site: %v, want ErrNotFound", err)
	}

	if got := texts(t, db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'site_legal_document.publish'"); !slices.Equal(got, []string{"5"}) {
		t.Errorf("publication audit entries: %v, want 5", got)
	}
	if got := texts(t, db, platform, `SELECT (detail->>'version') || ' ' || (detail->>'locale') || ' ' || (detail ? 'sha256')::text || ' ' || (detail ? 'body')::text
		FROM platform.audit_log WHERE action = 'site_legal_document.publish' ORDER BY id LIMIT 1`); !slices.Equal(got, []string{"1 zh true false"}) {
		t.Errorf("the first audit detail: %v", got)
	}
}
