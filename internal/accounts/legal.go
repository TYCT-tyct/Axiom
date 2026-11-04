package accounts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// The kinds of legal document a site publishes. They are the contract's enum and the table's check.
const (
	LegalTerms    = "terms"
	LegalPrivacy  = "privacy"
	LegalLocaleZH = "zh"
	LegalLocaleEN = "en"
)

// maxLegalBody is the most a document may hold, in characters: the table's bound and the contract's maxLength.
const maxLegalBody = 65536

// ErrConsentRequired is a registration or a console request before the current legal documents are accepted.
var ErrConsentRequired = errors.New("accounts: the current legal documents must be accepted")

// LegalDocument is one version of one of a site's legal documents. Its text never changes: a correction is a new
// version, so an acceptance keeps naming the text that was shown.
type LegalDocument struct {
	Kind        string
	Locale      string
	Version     int
	Body        string
	PublishedAt time.Time
}

// DocumentVersion names a document version, which is what a page accepts and an acceptance records.
type DocumentVersion struct {
	Kind    string
	Locale  string
	Version int
}

// LegalDocumentError is a document the admin API refuses; Field is the request member.
type LegalDocumentError struct{ Field, Reason string }

func (e *LegalDocumentError) Error() string { return "accounts: " + e.Field + " " + e.Reason }

// PublishLegalDocument adds the next version of kind and locale for the site and records it. During the rolling-release
// compatibility phase versions count per kind, not per locale: the legacy primary key (site, kind, version) still
// accepts writes from old nodes, and a later cleanup migration may give each locale its own stream. The site row is
// locked so two publications cannot choose the same version, and the audit entry carries a digest and the length
// rather than the text: a long document would exceed the entry's limit.
func (s *Service) PublishLegalDocument(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, kind, locale, body, reason string) (LegalDocument, error) {
	if kind != LegalTerms && kind != LegalPrivacy {
		return LegalDocument{}, &LegalDocumentError{"kind", "must be terms or privacy"}
	}
	if locale != LegalLocaleZH && locale != LegalLocaleEN {
		return LegalDocument{}, &LegalDocumentError{"locale", "must be zh or en"}
	}
	switch {
	case body == "":
		return LegalDocument{}, &LegalDocumentError{"body", "must not be empty"}
	case utf8.RuneCountInString(body) > maxLegalBody:
		return LegalDocument{}, &LegalDocumentError{"body", fmt.Sprintf("must be at most %d characters", maxLegalBody)}
	}
	locked, err := s.queries.LockSite(ctx, tx, uuid.UUID(site))
	if err != nil {
		return LegalDocument{}, fmt.Errorf("lock the site: %w", err)
	}
	if len(locked) == 0 {
		return LegalDocument{}, ErrNotFound
	}
	version, err := s.queries.NextLegalVersion(ctx, tx, NextLegalVersionParams{SiteID: uuid.UUID(site), Kind: kind})
	if err != nil {
		return LegalDocument{}, fmt.Errorf("read the next legal document version: %w", err)
	}
	publishedAt, err := s.queries.InsertLegalDocument(ctx, tx, InsertLegalDocumentParams{
		SiteID: uuid.UUID(site), Kind: kind, Locale: locale, Version: version, Body: body,
	})
	if err != nil {
		return LegalDocument{}, fmt.Errorf("insert the legal document: %w", err)
	}
	sum := sha256.Sum256([]byte(body))
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "site_legal_document.publish", Reason: reason,
		Target: audit.Target{Module: "accounts", Kind: "site", ID: site.TypeID(), Site: uuid.UUID(site)},
		Detail: map[string]any{
			"kind": kind, "locale": locale, "version": version,
			"characters": utf8.RuneCountInString(body), "sha256": fmt.Sprintf("%x", sum),
		},
	}); err != nil {
		return LegalDocument{}, err
	}
	return LegalDocument{Kind: kind, Locale: locale, Version: int(version), Body: body, PublishedAt: publishedAt.Time}, nil
}

// CurrentLegalDocuments reads the latest version of each kind in each locale for the site, which is what a page may
// show and a registration must accept. The text is public, so the read runs in the site's scope.
func (s *Service) CurrentLegalDocuments(ctx context.Context, site SiteID) ([]LegalDocument, error) {
	var documents []LegalDocument
	err := s.db.ReadTx(ctx, pg.SiteScope(uuid.UUID(site)), func(tx pg.Tx) error {
		var err error
		documents, err = s.currentLegalDocuments(ctx, tx, site)
		return err
	})
	if err != nil {
		return nil, err
	}
	return documents, nil
}

func (s *Service) currentLegalDocuments(ctx context.Context, tx pg.Tx, site SiteID) ([]LegalDocument, error) {
	rows, err := s.queries.CurrentLegalDocuments(ctx, tx, uuid.UUID(site))
	if err != nil {
		return nil, fmt.Errorf("read the current legal documents: %w", err)
	}
	documents := make([]LegalDocument, len(rows))
	for i, row := range rows {
		documents[i] = LegalDocument{Kind: row.Kind, Locale: row.Locale, Version: int(row.Version), Body: row.Body, PublishedAt: row.PublishedAt.Time}
	}
	return documents, nil
}

// LegalDocumentHistory reads one page of every kind's and locale's versions, newest first within a kind, after the
// legacy (kind, version) cursor position. A locale in a new cursor is accepted by the handler but reduced to this same
// position while old nodes remain. It asks for one row more than the page so the caller knows whether a next page
// exists. The caller's transaction holds the platform scope.
func (s *Service) LegalDocumentHistory(ctx context.Context, tx pg.Tx, site SiteID, after *DocumentVersion, limit int) ([]LegalDocument, bool, error) {
	params := LegalDocumentHistoryParams{SiteID: uuid.UUID(site), PageSize: int32(limit + 1)} // #nosec G115 -- the admin contract bounds the page size at 200
	if after != nil {
		params.CursorKind, params.CursorVersion = after.Kind, int32(after.Version) // #nosec G115 -- the cursor comes from a published version
	}
	rows, err := s.queries.LegalDocumentHistory(ctx, tx, params)
	if err != nil {
		return nil, false, fmt.Errorf("read the legal document history: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	documents := make([]LegalDocument, len(rows))
	for i, row := range rows {
		documents[i] = LegalDocument{Kind: row.Kind, Locale: row.Locale, Version: int(row.Version), Body: row.Body, PublishedAt: row.PublishedAt.Time}
	}
	return documents, more, nil
}

// PendingLegalDocuments is what the signed-in user has not accepted at a current version. A kind is satisfied when a
// current document of that kind in any locale is accepted; when it is not, the kind's newest current document is
// returned, one per kind, so the list can itself be accepted. The page chooses the locale it shows from the full
// current list. An empty result means every kind is accepted, and a site with no documents always has an empty one.
func (s *Service) PendingLegalDocuments(ctx context.Context, p Principal) ([]DocumentVersion, error) {
	var pending []DocumentVersion
	err := s.db.ReadTx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		pending, err = s.pendingLegalDocuments(ctx, tx, p.site, p.user)
		return err
	})
	if err != nil {
		return nil, err
	}
	return pending, nil
}

func (s *Service) pendingLegalDocuments(ctx context.Context, tx pg.Tx, site SiteID, user UserID) ([]DocumentVersion, error) {
	current, err := s.currentLegalDocuments(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	accepted, err := s.acceptedLegalVersions(ctx, tx, site, user)
	if err != nil {
		return nil, err
	}
	satisfied := make(map[string]bool, len(current))
	newest := make(map[string]LegalDocument, len(current))
	var kinds []string
	for _, document := range current {
		if accepted[DocumentVersion{Kind: document.Kind, Locale: document.Locale, Version: document.Version}] {
			satisfied[document.Kind] = true
		}
		chosen, seen := newest[document.Kind]
		if !seen {
			kinds = append(kinds, document.Kind)
		}
		if !seen || document.Version > chosen.Version {
			newest[document.Kind] = document
		}
	}
	var pending []DocumentVersion
	for _, kind := range kinds {
		if document := newest[kind]; !satisfied[kind] {
			pending = append(pending, DocumentVersion{Kind: document.Kind, Locale: document.Locale, Version: document.Version})
		}
	}
	return pending, nil
}

func (s *Service) acceptedLegalVersions(ctx context.Context, tx pg.Tx, site SiteID, user UserID) (map[DocumentVersion]bool, error) {
	rows, err := s.queries.AcceptedLegalVersions(ctx, tx, AcceptedLegalVersionsParams{SiteID: uuid.UUID(site), UserID: uuid.UUID(user)})
	if err != nil {
		return nil, fmt.Errorf("read the accepted legal versions: %w", err)
	}
	accepted := make(map[DocumentVersion]bool, len(rows))
	for _, row := range rows {
		accepted[DocumentVersion{Kind: row.Kind, Locale: row.Locale, Version: int(row.Version)}] = true
	}
	return accepted, nil
}

// AcceptLegalDocuments records the user's acceptance of the current version of each kind. A request from a new page
// names the locale; an old page omits it, and the version alone resolves to the one current document because the
// compatibility phase still keeps versions unique across locales. A missing, stale or duplicate kind is
// ErrConsentRequired, so a page sends exactly one current document per kind, as getLegalDocuments showed it; accepting
// the same version again is a no-op. The site and user come from the session, never from the request.
func (s *Service) AcceptLegalDocuments(ctx context.Context, p Principal, accepted []DocumentVersion) error {
	return s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		documents, err := s.currentVersionsAccepted(ctx, tx, p.site, accepted)
		if err != nil {
			return err
		}
		return insertLegalAcceptances(ctx, tx, s.queries, p.site, p.account, p.user, documents)
	})
}

// currentVersionsAccepted checks that accepted names exactly one current document per kind, and returns that
// canonical set. A site with no documents requires no acceptance, and extra versions are refused: they mean the page
// was built from other documents than the ones now in force.
func (s *Service) currentVersionsAccepted(ctx context.Context, tx pg.Tx, site SiteID, accepted []DocumentVersion) ([]DocumentVersion, error) {
	current, err := s.currentLegalDocuments(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	given := make(map[string]DocumentVersion, len(accepted))
	kinds := make(map[string]bool)
	for _, document := range current {
		kinds[document.Kind] = true
	}
	for _, document := range accepted {
		if _, twice := given[document.Kind]; twice {
			return nil, ErrConsentRequired
		}
		given[document.Kind] = document
	}
	if len(given) != len(kinds) {
		return nil, ErrConsentRequired
	}
	documents := make([]DocumentVersion, 0, len(current))
	for _, document := range current {
		chosen, ok := given[document.Kind]
		if !ok || chosen.Version != document.Version {
			continue
		}
		// An old page sends no locale. The version resolves it while old nodes keep versions unique across locales; if a
		// later cleanup allows two current rows at one version, the old request becomes ambiguous and stays refused.
		if chosen.Locale == "" || chosen.Locale == document.Locale {
			documents = append(documents, DocumentVersion{Kind: document.Kind, Locale: document.Locale, Version: document.Version})
		}
	}
	if len(documents) != len(given) {
		return nil, ErrConsentRequired
	}
	return documents, nil
}

// insertLegalAcceptances writes one row per document. The rows are not deleted or changed later: they are the record
// of what the person accepted and when.
func insertLegalAcceptances(ctx context.Context, tx pg.Tx, q *Queries, site SiteID, account AccountID, user UserID, documents []DocumentVersion) error {
	for _, document := range documents {
		if err := q.InsertLegalAcceptance(ctx, tx, InsertLegalAcceptanceParams{
			ID: uuid.NewV7(), SiteID: uuid.UUID(site), AccountID: uuid.UUID(account), UserID: uuid.UUID(user),
			Kind: document.Kind, Locale: document.Locale, Version: int32(document.Version), // #nosec G115 -- the version comes from the table, which checks it is at least 1
		}); err != nil {
			return fmt.Errorf("record a legal document acceptance: %w", err)
		}
	}
	return nil
}
