package accounts_test

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

const (
	// placeholderTerms and placeholderPrivacy are fixed test text, never real terms.
	placeholderTerms   = "Placeholder terms of service for tests: this is not a real agreement."
	placeholderPrivacy = "Placeholder privacy policy for tests: this is not a real policy."
)

// TestLegalDocumentVersionsAndAcceptance: publishing labels each document with a locale, the current list is the latest
// of each kind and locale, and an acceptance names exactly one current version per kind and keeps its first time. During
// the rolling-release compatibility phase version numbers are shared across locales, so the English terms after two
// Chinese terms are version 3; a later cleanup can split the streams. A stale or extra version is refused, and the audit
// entry carries the version and a digest rather than the text.
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
	if termsEN.Version != 3 { // the compatibility phase shares version numbers across locales
		t.Fatalf("the first English terms version: %+v", termsEN)
	}

	current, err := s.CurrentLegalDocuments(t.Context(), site)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 3 || current[0].Kind != accounts.LegalPrivacy || current[1].Locale != accounts.LegalLocaleEN || current[1].Version != 3 || current[2].Version != 2 {
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
	if !slices.Equal(pairVersions, []string{"privacy:zh:1", "terms:en:3", "terms:zh:2", "terms:zh:1"}) {
		t.Fatalf("history: %v", pairVersions)
	}

	p := accounts.PrincipalForTest(site, account, user)
	pending, err := s.PendingLegalDocuments(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || pending[0].Kind != accounts.LegalPrivacy || pending[1].Version != 3 || pending[1].Locale != accounts.LegalLocaleEN {
		t.Fatalf("pending before accepting: %+v", pending)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{
		{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleZH, Version: 2},
		{Kind: accounts.LegalPrivacy, Locale: accounts.LegalLocaleZH, Version: 1},
	}); !errors.Is(err, accounts.ErrConsentRequired) {
		t.Fatalf("accepting a lower locale version: %v, want ErrConsentRequired", err)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, pending[:1]); !errors.Is(err, accounts.ErrConsentRequired) {
		t.Fatalf("accepting half the documents: %v, want ErrConsentRequired", err)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, pending); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, db, platform, "SELECT kind || ' ' || locale || ' ' || version FROM accounts.legal_acceptances ORDER BY kind, locale"); !slices.Equal(got, []string{"privacy zh 1", "terms en 3"}) {
		t.Fatalf("acceptances: %v", got)
	}
	firstAccepted := texts(t, db, platform, "SELECT accepted_at::text FROM accounts.legal_acceptances WHERE kind = 'terms' AND locale = 'en' AND version = 3")[0]
	if err := s.AcceptLegalDocuments(t.Context(), p, pending); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, db, platform, "SELECT accepted_at::text FROM accounts.legal_acceptances WHERE kind = 'terms' AND locale = 'en' AND version = 3"); len(got) != 1 || got[0] != firstAccepted {
		t.Errorf("a retried acceptance: %v, want one row at %s", got, firstAccepted)
	}
	if pending, err := s.PendingLegalDocuments(t.Context(), p); err != nil || len(pending) != 0 {
		t.Fatalf("pending after accepting: %+v, %v", pending, err)
	}

	publish(accounts.LegalTerms, accounts.LegalLocaleEN, "English placeholder terms for tests. Version two.")
	pending, err = s.PendingLegalDocuments(t.Context(), p)
	if err != nil || len(pending) != 1 || pending[0].Version != 4 || pending[0].Locale != accounts.LegalLocaleEN {
		t.Fatalf("pending after a new version: %+v, %v", pending, err)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleZH, Version: 2}}); !errors.Is(err, accounts.ErrConsentRequired) {
		t.Errorf("accepting the superseded version: %v, want ErrConsentRequired", err)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleEN, Version: 4}, {Kind: accounts.LegalPrivacy, Locale: accounts.LegalLocaleZH, Version: 1}, {Kind: "marketing", Locale: accounts.LegalLocaleZH, Version: 1}}); !errors.Is(err, accounts.ErrConsentRequired) {
		t.Errorf("accepting an extra kind: %v, want ErrConsentRequired", err)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleEN, Version: 4}, {Kind: accounts.LegalPrivacy, Locale: accounts.LegalLocaleZH, Version: 1}}); err != nil {
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

// TestLegalOldNodeWritesAfterLocaleExpansion feeds the schema the statements an old node still runs. The old document
// insert omits locale, and the old acceptance insert omits locale and targets (user_id, kind, version); both must keep
// working while nodes of both versions run. The trigger resolves the omitted acceptance locale from the document, so
// an old page accepting English records English rather than a zh acceptance of an English version.
func TestLegalOldNodeWritesAfterLocaleExpansion(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	acceptance := uuid.NewV7().String()
	statements := []string{
		`INSERT INTO accounts.legal_documents (site_id, kind, version, body)
			VALUES (:s1, 'terms', 1, 'Legacy Chinese terms.')`,
		`INSERT INTO accounts.legal_documents (site_id, kind, locale, version, body)
			VALUES (:s1, 'terms', 'en', 2, 'English terms.')`,
		`INSERT INTO accounts.legal_acceptances (id, site_id, account_id, user_id, kind, version)
			VALUES (:x, :s1, :a1, :u1, 'terms', 1)
			ON CONFLICT (user_id, kind, version) DO NOTHING`,
		`INSERT INTO accounts.legal_acceptances (id, site_id, account_id, user_id, kind, version)
			VALUES (:acceptance, :s1, :a1, :u1, 'terms', 2)
			ON CONFLICT (user_id, kind, version) DO NOTHING`,
	}
	oldAcceptance := strings.ReplaceAll(statements[3], ":acceptance", "'"+acceptance+"'")
	for _, statement := range statements[:2] {
		if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(statement)); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	runAsMember := func(statement string) error {
		return db.Tx(t.Context(), pg.AccountScope(a1), func(tx pg.Tx) error {
			_, err := tx.Exec(t.Context(), ids.Replace(statement))
			return err
		})
	}
	for _, statement := range []string{statements[2], oldAcceptance} {
		if err := runAsMember(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	// A retry of the old acceptance changes nothing, as it did before the locale expansion.
	if err := runAsMember(oldAcceptance); err != nil {
		t.Fatalf("retry the old acceptance: %v", err)
	}
	if rows := texts(t, db, platform,
		`SELECT kind || ' ' || locale || ' ' || version FROM accounts.legal_acceptances ORDER BY version`); !slices.Equal(rows, []string{"terms zh 1", "terms en 2"}) {
		t.Fatalf("acceptances: %v", rows)
	}
	// The legacy primary key still refuses two locales at one version. That is the boundary that lets old nodes keep
	// their (site, kind, version) writes during the compatibility phase instead of silently creating such a pair.
	if err := pgtest.Run(t.Context(), database.Superuser,
		`INSERT INTO accounts.legal_documents (site_id, kind, locale, version, body)
			VALUES (:s1, 'terms', 'en', 1, 'A second locale at the legacy version.')`); err == nil {
		t.Fatal("a second locale at the legacy version was accepted")
	}
}

// TestLegalCompatibilityAcceptanceAcrossNodes checks the two directions that a rolling release can route between an
// old and a new node. The compatibility phase has one current version per kind, the newest across locales: accepting
// that version on either node satisfies the other, and accepting a lower version in the page's language does not.
func TestLegalCompatibilityAcceptanceAcrossNodes(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	s := node(t, db, hashKeys(t, 1), nil)
	site, account := accounts.SiteID(s1), accounts.AccountID(a1)
	publish := func(site accounts.SiteID, kind, locale, body string) {
		t.Helper()
		if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			_, err := s.PublishLegalDocument(t.Context(), tx, system, site, kind, locale, body, "test")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	publish(site, accounts.LegalTerms, accounts.LegalLocaleZH, placeholderTerms)
	publish(site, accounts.LegalTerms, accounts.LegalLocaleEN, "English placeholder terms for tests.")
	publish(site, accounts.LegalPrivacy, accounts.LegalLocaleZH, placeholderPrivacy)

	p := accounts.PrincipalForTest(site, account, accounts.UserID(u1))
	// The old page omits locale: the new node resolves the one row at each version, then the database stores its
	// actual locale. This is the old-response-to-new-node direction.
	if err := s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{
		{Kind: accounts.LegalTerms, Version: 2},
		{Kind: accounts.LegalPrivacy, Version: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, db, platform, "SELECT kind || ' ' || locale || ' ' || version FROM accounts.legal_acceptances WHERE user_id = '"+u1.String()+"' ORDER BY kind"); !slices.Equal(got, []string{"privacy zh 1", "terms en 2"}) {
		t.Fatalf("acceptances after omitted locales: %v", got)
	}
	// This is the old node's current/accepted comparison: max(version) per kind, not per locale.
	if got := texts(t, db, platform, fmt.Sprintf(`
		SELECT count(*)::text
		FROM (SELECT d.kind, max(d.version) AS version
			FROM accounts.legal_documents d
			WHERE d.site_id = '%s'
			GROUP BY d.kind) current
		WHERE NOT EXISTS (
			SELECT 1 FROM accounts.legal_acceptances a
			WHERE a.site_id = '%s' AND a.user_id = '%s'
				AND a.kind = current.kind AND a.version = current.version
		)`, s1, s1, u1)); !slices.Equal(got, []string{"0"}) {
		t.Fatalf("the old node still sees %v kind(s) pending after the new acceptance", got)
	}
	if err := s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{
		{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleZH, Version: 1},
		{Kind: accounts.LegalPrivacy, Locale: accounts.LegalLocaleZH, Version: 1},
	}); !errors.Is(err, accounts.ErrConsentRequired) {
		t.Fatalf("accepting the lower Chinese terms: %v, want ErrConsentRequired", err)
	}

	// The other direction uses the second seeded site, because its user has no acceptance yet. An old node accepts
	// the newest versions without a locale, and the new node sees no pending.
	secondSite, secondAccount := accounts.SiteID(s2), accounts.AccountID(a2)
	publish(secondSite, accounts.LegalTerms, accounts.LegalLocaleEN, "Second site terms for tests.")
	publish(secondSite, accounts.LegalPrivacy, accounts.LegalLocaleZH, placeholderPrivacy)
	runAsMember := func(account accounts.AccountID, statement string) error {
		return db.Tx(t.Context(), pg.AccountScope(uuid.UUID(account)), func(tx pg.Tx) error {
			_, err := tx.Exec(t.Context(), ids.Replace(statement))
			return err
		})
	}
	for _, statement := range []string{
		`INSERT INTO accounts.legal_acceptances (id, site_id, account_id, user_id, kind, version)
			VALUES (:x, :s2, :a2, :u2, 'terms', 1)
			ON CONFLICT (user_id, kind, version) DO NOTHING`,
		`INSERT INTO accounts.legal_acceptances (id, site_id, account_id, user_id, kind, version)
			VALUES (:y, :s2, :a2, :u2, 'privacy', 1)
			ON CONFLICT (user_id, kind, version) DO NOTHING`,
	} {
		if err := runAsMember(secondAccount, statement); err != nil {
			t.Fatal(err)
		}
	}
	if pending, err := s.PendingLegalDocuments(t.Context(), accounts.PrincipalForTest(secondSite, secondAccount, accounts.UserID(u2))); err != nil || len(pending) != 0 {
		t.Fatalf("pending after the old acceptance: %+v, %v", pending, err)
	}
}

// TestLegalSessionPendingUsesNewestVersion checks the session query, not just the pending list: accepting a lower
// version in one locale must not make an authenticated session look consented while the newest version of that kind is
// still unaccepted. Accepting the newest version then clears the flag.
func TestLegalSessionPendingUsesNewestVersion(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	publish := func(kind, locale, body string) {
		t.Helper()
		if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			_, err := x.s.PublishLegalDocument(t.Context(), tx, system, accounts.SiteID(s1), kind, locale, body, "test")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	publish(accounts.LegalTerms, accounts.LegalLocaleZH, placeholderTerms)
	publish(accounts.LegalTerms, accounts.LegalLocaleEN, "English placeholder terms for tests.")
	publish(accounts.LegalPrivacy, accounts.LegalLocaleZH, placeholderPrivacy)

	account := uuid.MustParse(texts(t, x.db, platform, "SELECT account_id::text FROM accounts.users WHERE id = '"+uuid.UUID(x.user).String()+"'")[0])
	issued := x.signIn(t, console)
	p, err := x.principal(t, console, issued)
	if err != nil {
		t.Fatal(err)
	}
	if !p.ConsentPending() {
		t.Fatal("a session with no acceptance is not pending")
	}
	// These are both current rows in their own locales, but terms v1 is lower than the shared newest v2. The session
	// must still be pending, exactly as an old node's max(version) check would be.
	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO accounts.legal_acceptances (id, site_id, account_id, user_id, kind, version)
			VALUES (:x, :s1, '%s', '%s', 'terms', 1)`, account, uuid.UUID(x.user)),
		fmt.Sprintf(`INSERT INTO accounts.legal_acceptances (id, site_id, account_id, user_id, kind, version)
			VALUES (:y, :s1, '%s', '%s', 'privacy', 1)`, account, uuid.UUID(x.user)),
	} {
		if err := x.db.Tx(t.Context(), pg.AccountScope(account), func(tx pg.Tx) error {
			_, err := tx.Exec(t.Context(), ids.Replace(statement))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	p, err = x.principal(t, console, issued)
	if err != nil {
		t.Fatal(err)
	}
	if !p.ConsentPending() {
		t.Fatal("accepting the lower Chinese terms cleared the session's pending flag")
	}
	if err := x.s.AcceptLegalDocuments(t.Context(), p, []accounts.DocumentVersion{
		{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleEN, Version: 2},
		{Kind: accounts.LegalPrivacy, Locale: accounts.LegalLocaleZH, Version: 1},
	}); err != nil {
		t.Fatal(err)
	}
	p, err = x.principal(t, console, issued)
	if err != nil {
		t.Fatal(err)
	}
	if p.ConsentPending() {
		t.Fatal("the session is still pending after accepting the newest versions")
	}
}

// TestLegalLocaleMigrationDownKeepsOldWrites runs the migration's Down section after a Chinese and an English
// document at different versions, then repeats the statements an old node runs. The expansion never created two rows
// at one (site, kind, version), so removing the locale columns leaves the legacy primary key and unique constraint
// valid; documents and acceptances survive, and rollback is not a one-way door.
func TestLegalLocaleMigrationDownKeepsOldWrites(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	run := func(statement string) {
		t.Helper()
		if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(statement)); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	run(`INSERT INTO accounts.legal_documents (site_id, kind, locale, version, body)
		VALUES (:s1, 'terms', 'zh', 1, 'Chinese terms.')`)
	run(`INSERT INTO accounts.legal_documents (site_id, kind, locale, version, body)
		VALUES (:s1, 'terms', 'en', 2, 'English terms.')`)
	run(`INSERT INTO accounts.legal_acceptances (id, site_id, account_id, user_id, kind, locale, version)
		VALUES (:x, :s1, :a1, :u1, 'terms', 'en', 2)`)

	file, err := os.ReadFile("migrations/00032_legal_locales.sql")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(file), "-- +goose Down")
	if len(parts) != 2 {
		t.Fatal("the migration has no Down section")
	}
	for statement := range strings.SplitSeq(parts[1], ";") {
		if strings.TrimSpace(statement) != "" {
			run(statement)
		}
	}

	run(`INSERT INTO accounts.legal_documents (site_id, kind, version, body)
		VALUES (:s1, 'terms', 3, 'Old node terms.')`)
	run(`INSERT INTO accounts.legal_acceptances (id, site_id, account_id, user_id, kind, version)
		VALUES (:y, :s1, :a1, :u1, 'terms', 3)
		ON CONFLICT (user_id, kind, version) DO NOTHING`)
	if got := texts(t, db, platform, "SELECT kind || ' ' || version FROM accounts.legal_documents ORDER BY version"); !slices.Equal(got, []string{"terms 1", "terms 2", "terms 3"}) {
		t.Fatalf("documents after rollback: %v", got)
	}
	if got := texts(t, db, platform, "SELECT kind || ' ' || version FROM accounts.legal_acceptances ORDER BY version"); !slices.Equal(got, []string{"terms 2", "terms 3"}) {
		t.Fatalf("acceptances after rollback: %v", got)
	}
}
