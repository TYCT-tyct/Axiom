package accounts_test

import (
	"errors"
	"fmt"
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
// legalFlow is the publication-to-acceptance walk's database, service and site.
type legalFlow struct {
	t    *testing.T
	db   *pg.DB
	s    *accounts.Service
	site accounts.SiteID
	p    accounts.Principal
}

func newLegalFlow(t *testing.T) *legalFlow {
	t.Helper()
	db, _ := open(t)
	s := node(t, db, hashKeys(t, 1), nil)
	site := accounts.SiteID(s1)
	return &legalFlow{
		t: t, db: db, s: s, site: site,
		p: accounts.PrincipalForTest(site, accounts.AccountID(a1), accounts.UserID(u1)),
	}
}

func (f *legalFlow) publish(kind, locale, body string) accounts.LegalDocument {
	f.t.Helper()
	var document accounts.LegalDocument
	if err := f.db.Tx(f.t.Context(), platform, func(tx pg.Tx) error {
		var err error
		document, err = f.s.PublishLegalDocument(f.t.Context(), tx, system, f.site, kind, locale, body, "test")
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	return document
}

// checkPublishedVersions asserts versions count per kind and locale, and that the compatibility phase shares numbers
// across locales.
func (f *legalFlow) checkPublishedVersions() {
	f.t.Helper()
	terms1 := f.publish(accounts.LegalTerms, accounts.LegalLocaleZH, placeholderTerms)
	if terms1.Kind != accounts.LegalTerms || terms1.Locale != accounts.LegalLocaleZH || terms1.Version != 1 || terms1.PublishedAt.IsZero() {
		f.t.Fatalf("the first terms version: %+v", terms1)
	}
	privacy1 := f.publish(accounts.LegalPrivacy, accounts.LegalLocaleZH, placeholderPrivacy)
	if privacy1.Kind != accounts.LegalPrivacy || privacy1.Locale != accounts.LegalLocaleZH || privacy1.Version != 1 {
		f.t.Fatalf("the first privacy version: %+v", privacy1)
	}
	if terms2 := f.publish(accounts.LegalTerms, accounts.LegalLocaleZH, placeholderTerms+" Version two."); terms2.Version != 2 {
		f.t.Fatalf("the second terms version: %+v", terms2)
	}
	if termsEN := f.publish(accounts.LegalTerms, accounts.LegalLocaleEN, "English placeholder terms for tests."); termsEN.Version != 3 {
		f.t.Fatalf("the first English terms version: %+v", termsEN)
	}
}

// checkCurrentDocuments asserts each site sees its own newest documents.
func (f *legalFlow) checkCurrentDocuments() {
	f.t.Helper()
	current, err := f.s.CurrentLegalDocuments(f.t.Context(), f.site)
	if err != nil {
		f.t.Fatal(err)
	}
	if len(current) != 3 || current[0].Kind != accounts.LegalPrivacy || current[1].Locale != accounts.LegalLocaleEN ||
		current[1].Version != 3 || current[2].Version != 2 {
		f.t.Fatalf("current documents: %+v", current)
	}
	if none, err := f.s.CurrentLegalDocuments(f.t.Context(), accounts.SiteID(s2)); err != nil || len(none) != 0 {
		f.t.Fatalf("a site without documents: %+v, %v", none, err)
	}
}

// checkHistory asserts the full ordered history and that four documents are one page.
func (f *legalFlow) checkHistory() {
	f.t.Helper()
	var history []accounts.LegalDocument
	if err := f.db.Tx(f.t.Context(), platform, func(tx pg.Tx) error {
		var more bool
		var err error
		history, more, err = f.s.LegalDocumentHistory(f.t.Context(), tx, f.site, nil, 200)
		if err == nil && more {
			f.t.Error("a site with four documents has a next page")
		}
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	if len(history) != 4 {
		f.t.Fatalf("history: %+v", history)
	}
	var pairVersions []string
	for _, document := range history {
		pairVersions = append(pairVersions, document.Kind+":"+document.Locale+":"+itoa(document.Version))
	}
	if !slices.Equal(pairVersions, []string{"privacy:zh:1", "terms:en:3", "terms:zh:2", "terms:zh:1"}) {
		f.t.Fatalf("history: %v", pairVersions)
	}
}

// checkCurrentAndHistory asserts each site sees its own newest documents and the full ordered history.
func (f *legalFlow) checkCurrentAndHistory() {
	f.t.Helper()
	f.checkCurrentDocuments()
	f.checkHistory()
}

// checkPendingBeforeAcceptance asserts the two documents still pending.
func (f *legalFlow) pendingBeforeAcceptance() []accounts.DocumentVersion {
	f.t.Helper()
	pending, err := f.s.PendingLegalDocuments(f.t.Context(), f.p)
	if err != nil {
		f.t.Fatal(err)
	}
	if len(pending) != 2 || pending[0].Kind != accounts.LegalPrivacy || pending[1].Version != 3 ||
		pending[1].Locale != accounts.LegalLocaleEN {
		f.t.Fatalf("pending before accepting: %+v", pending)
	}
	return pending
}

// checkAcceptanceRefusals asserts a stale or partial acceptance is refused and nothing is written.
func (f *legalFlow) checkAcceptanceRefusals(pending []accounts.DocumentVersion) {
	f.t.Helper()
	if err := f.s.AcceptLegalDocuments(f.t.Context(), f.p, []accounts.DocumentVersion{
		{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleZH, Version: 2},
		{Kind: accounts.LegalPrivacy, Locale: accounts.LegalLocaleZH, Version: 1},
	}); !errors.Is(err, accounts.ErrConsentRequired) {
		f.t.Fatalf("accepting a lower locale version: %v, want ErrConsentRequired", err)
	}
	if err := f.s.AcceptLegalDocuments(f.t.Context(), f.p, pending[:1]); !errors.Is(err, accounts.ErrConsentRequired) {
		f.t.Fatalf("accepting half the documents: %v, want ErrConsentRequired", err)
	}
}

// checkAcceptanceRows asserts the whole acceptance writes one row per document and a retry leaves it alone.
func (f *legalFlow) checkAcceptanceRows(pending []accounts.DocumentVersion) {
	f.t.Helper()
	if err := f.s.AcceptLegalDocuments(f.t.Context(), f.p, pending); err != nil {
		f.t.Fatal(err)
	}
	if got := texts(f.t, f.db, platform, "SELECT kind || ' ' || locale || ' ' || version FROM accounts.legal_acceptances ORDER BY kind, locale"); !slices.Equal(got, []string{"privacy zh 1", "terms en 3"}) {
		f.t.Fatalf("acceptances: %v", got)
	}
	firstAccepted := texts(f.t, f.db, platform, "SELECT accepted_at::text FROM accounts.legal_acceptances WHERE kind = 'terms' AND locale = 'en' AND version = 3")[0]
	if err := f.s.AcceptLegalDocuments(f.t.Context(), f.p, pending); err != nil {
		f.t.Fatal(err)
	}
	if got := texts(f.t, f.db, platform, "SELECT accepted_at::text FROM accounts.legal_acceptances WHERE kind = 'terms' AND locale = 'en' AND version = 3"); len(got) != 1 || got[0] != firstAccepted {
		f.t.Errorf("a retried acceptance: %v, want one row at %s", got, firstAccepted)
	}
	if pending, err := f.s.PendingLegalDocuments(f.t.Context(), f.p); err != nil || len(pending) != 0 {
		f.t.Fatalf("pending after accepting: %+v, %v", pending, err)
	}
}

// checkAcceptance asserts stale, partial and extra acceptances are refused, the full one is idempotent, and each
// acceptance row is written once.
func (f *legalFlow) checkAcceptance() {
	f.t.Helper()
	pending := f.pendingBeforeAcceptance()
	f.checkAcceptanceRefusals(pending)
	f.checkAcceptanceRows(pending)
}

// checkNewVersion asserts a new version pends again, a superseded or extra kind is refused, and the new one is accepted.
func (f *legalFlow) checkNewVersion() {
	f.t.Helper()
	f.publish(accounts.LegalTerms, accounts.LegalLocaleEN, "English placeholder terms for tests. Version two.")
	pending, err := f.s.PendingLegalDocuments(f.t.Context(), f.p)
	if err != nil || len(pending) != 1 || pending[0].Version != 4 || pending[0].Locale != accounts.LegalLocaleEN {
		f.t.Fatalf("pending after a new version: %+v, %v", pending, err)
	}
	if err := f.s.AcceptLegalDocuments(f.t.Context(), f.p, []accounts.DocumentVersion{
		{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleZH, Version: 2},
	}); !errors.Is(err, accounts.ErrConsentRequired) {
		f.t.Errorf("accepting the superseded version: %v, want ErrConsentRequired", err)
	}
	if err := f.s.AcceptLegalDocuments(f.t.Context(), f.p, []accounts.DocumentVersion{
		{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleEN, Version: 4},
		{Kind: accounts.LegalPrivacy, Locale: accounts.LegalLocaleZH, Version: 1},
		{Kind: "marketing", Locale: accounts.LegalLocaleZH, Version: 1},
	}); !errors.Is(err, accounts.ErrConsentRequired) {
		f.t.Errorf("accepting an extra kind: %v, want ErrConsentRequired", err)
	}
	if err := f.s.AcceptLegalDocuments(f.t.Context(), f.p, []accounts.DocumentVersion{
		{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleEN, Version: 4},
		{Kind: accounts.LegalPrivacy, Locale: accounts.LegalLocaleZH, Version: 1},
	}); err != nil {
		f.t.Fatal(err)
	}
	if pending, err := f.s.PendingLegalDocuments(f.t.Context(), f.p); err != nil || len(pending) != 0 {
		f.t.Fatalf("pending after accepting the new version: %+v, %v", pending, err)
	}
}

// checkPublishRefusals asserts each malformed document names its field, and an unknown site is not found.
func (f *legalFlow) checkPublishRefusals() {
	f.t.Helper()
	for _, c := range []struct {
		name, kind, locale, body, field string
	}{
		{"an unknown kind", "marketing", accounts.LegalLocaleZH, "Text.", "kind"},
		{"an unknown locale", accounts.LegalTerms, "fr", "Text.", "locale"},
		{"an empty body", accounts.LegalTerms, accounts.LegalLocaleZH, "", "body"},
		{"a body past the limit", accounts.LegalTerms, accounts.LegalLocaleZH, strings.Repeat("x", 65537), "body"},
	} {
		err := f.db.Tx(f.t.Context(), platform, func(tx pg.Tx) error {
			_, err := f.s.PublishLegalDocument(f.t.Context(), tx, system, f.site, c.kind, c.locale, c.body, "test")
			return err
		})
		var refusal *accounts.LegalDocumentError
		if !errors.As(err, &refusal) || refusal.Field != c.field {
			f.t.Errorf("%s: %v, want LegalDocumentError on %s", c.name, err, c.field)
		}
	}
	err := f.db.Tx(f.t.Context(), platform, func(tx pg.Tx) error {
		_, err := f.s.PublishLegalDocument(f.t.Context(), tx, system, accounts.SiteID(uuid.NewV7()), accounts.LegalTerms,
			accounts.LegalLocaleZH, "Text.", "test")
		return err
	})
	if !errors.Is(err, accounts.ErrNotFound) {
		f.t.Errorf("publishing for an unknown site: %v, want ErrNotFound", err)
	}
}

// checkAudit asserts the publication entries count and carry the version, locale and a digest but not the body.
func (f *legalFlow) checkAudit() {
	f.t.Helper()
	if got := texts(f.t, f.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'site_legal_document.publish'"); !slices.Equal(got, []string{"5"}) {
		f.t.Errorf("publication audit entries: %v, want 5", got)
	}
	if got := texts(f.t, f.db, platform, `SELECT (detail->>'version') || ' ' || (detail->>'locale') || ' ' || (detail ? 'sha256')::text || ' ' || (detail ? 'body')::text
		FROM platform.audit_log WHERE action = 'site_legal_document.publish' ORDER BY id LIMIT 1`); !slices.Equal(got, []string{"1 zh true false"}) {
		f.t.Errorf("the first audit detail: %v", got)
	}
}

func TestLegalDocumentVersionsAndAcceptance(t *testing.T) {
	t.Parallel()
	f := newLegalFlow(t)
	f.checkPublishedVersions()
	f.checkCurrentAndHistory()
	f.checkAcceptance()
	f.checkNewVersion()
	f.checkPublishRefusals()
	f.checkAudit()
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
