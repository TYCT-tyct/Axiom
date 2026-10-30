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

// TestLegalDocumentVersionsAndAcceptance: publishing adds a version per kind, the current one is the latest, and an
// acceptance names exactly the current versions and keeps its first time. A stale or extra version is refused, and
// the audit entry carries the version and a digest rather than the text.
//
//nolint:cyclop,gocognit // one publication-to-acceptance flow: each step is the mechanism under test.
func TestLegalDocumentVersionsAndAcceptance(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	s := node(t, db, hashKeys(t, 1), nil)
	site, account, user := accounts.SiteID(s1), accounts.AccountID(a1), accounts.UserID(u1)
	publish := func(kind, body string) accounts.LegalDocument {
		t.Helper()
		var document accounts.LegalDocument
		if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			var err error
			document, err = s.PublishLegalDocument(t.Context(), tx, system, site, kind, body, "test")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return document
	}

	terms1 := publish(accounts.LegalTerms, placeholderTerms)
	if terms1.Kind != accounts.LegalTerms || terms1.Version != 1 || terms1.PublishedAt.IsZero() {
		t.Fatalf("the first terms version: %+v", terms1)
	}
	privacy1 := publish(accounts.LegalPrivacy, placeholderPrivacy)
	if privacy1.Kind != accounts.LegalPrivacy || privacy1.Version != 1 {
		t.Fatalf("the first privacy version: %+v", privacy1)
	}
	terms2 := publish(accounts.LegalTerms, placeholderTerms+" Version two.")
	if terms2.Version != 2 { // versions count per kind, not per site
		t.Fatalf("the second terms version: %+v", terms2)
	}

	current, err := s.CurrentLegalDocuments(t.Context(), site)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 2 || current[0].Kind != accounts.LegalPrivacy || current[1].Version != 2 {
		t.Fatalf("current documents: %+v", current)
	}
	if none, err := s.CurrentLegalDocuments(t.Context(), accounts.SiteID(s2)); err != nil || len(none) != 0 {
		t.Fatalf("a site without documents: %+v, %v", none, err)
	}
	var history []accounts.LegalDocument
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		var err error
		history, err = s.LegalDocumentHistory(t.Context(), tx, site)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var versions []int
	for _, document := range history {
		versions = append(versions, document.Version)
	}
	if len(history) != 3 || !slices.Equal(versions, []int{1, 2, 1}) {
		t.Fatalf("history: %+v", history)
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
	if got := texts(t, db, platform, "SELECT kind || ' ' || version FROM accounts.legal_acceptances ORDER BY kind"); !slices.Equal(got, []string{"privacy 1", "terms 2"}) {
		t.Fatalf("acceptances: %v", got)
	}
	firstAccepted := texts(t, db, platform, "SELECT accepted_at::text FROM accounts.legal_acceptances WHERE kind = 'terms'")[0]
	if err := s.AcceptLegalDocuments(t.Context(), p, pending); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, db, platform, "SELECT accepted_at::text FROM accounts.legal_acceptances WHERE kind = 'terms'"); len(got) != 1 || got[0] != firstAccepted {
		t.Errorf("a retried acceptance: %v, want one row at %s", got, firstAccepted)
	}
	if pending, err := s.PendingLegalDocuments(t.Context(), p); err != nil || len(pending) != 0 {
		t.Fatalf("pending after accepting: %+v, %v", pending, err)
	}

	publish(accounts.LegalTerms, placeholderTerms+" Version three.")
	pending, err = s.PendingLegalDocuments(t.Context(), p)
	if err != nil || len(pending) != 1 || pending[0].Version != 3 {
		t.Fatalf("pending after a new version: %+v, %v", pending, err)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{{Kind: accounts.LegalTerms, Version: 2}}); !errors.Is(err, accounts.ErrConsentRequired) {
		t.Errorf("accepting the superseded version: %v, want ErrConsentRequired", err)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{{Kind: accounts.LegalTerms, Version: 3}, {Kind: accounts.LegalPrivacy, Version: 1}, {Kind: "marketing", Version: 1}}); !errors.Is(err, accounts.ErrConsentRequired) {
		t.Errorf("accepting an extra kind: %v, want ErrConsentRequired", err)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{{Kind: accounts.LegalTerms, Version: 3}, {Kind: accounts.LegalPrivacy, Version: 1}}); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.PendingLegalDocuments(t.Context(), p); err != nil || len(pending) != 0 {
		t.Fatalf("pending after accepting the new version: %+v, %v", pending, err)
	}

	for _, c := range []struct {
		name, kind, body, field string
	}{
		{"an unknown kind", "marketing", "Text.", "kind"},
		{"an empty body", accounts.LegalTerms, "", "body"},
		{"a body past the limit", accounts.LegalTerms, strings.Repeat("x", 65537), "body"},
	} {
		err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			_, err := s.PublishLegalDocument(t.Context(), tx, system, site, c.kind, c.body, "test")
			return err
		})
		var refusal *accounts.LegalDocumentError
		if !errors.As(err, &refusal) || refusal.Field != c.field {
			t.Errorf("%s: %v, want LegalDocumentError on %s", c.name, err, c.field)
		}
	}
	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := s.PublishLegalDocument(t.Context(), tx, system, accounts.SiteID(uuid.NewV7()), accounts.LegalTerms, "Text.", "test")
		return err
	})
	if !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("publishing for an unknown site: %v, want ErrNotFound", err)
	}

	if got := texts(t, db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'site_legal_document.publish'"); !slices.Equal(got, []string{"4"}) {
		t.Errorf("publication audit entries: %v, want 4", got)
	}
	if got := texts(t, db, platform, `SELECT (detail->>'version') || ' ' || (detail ? 'sha256')::text || ' ' || (detail ? 'body')::text
		FROM platform.audit_log WHERE action = 'site_legal_document.publish' ORDER BY id LIMIT 1`); !slices.Equal(got, []string{"1 true false"}) {
		t.Errorf("the first audit detail: %v", got)
	}
}
