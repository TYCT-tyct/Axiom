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
	LegalTerms   = "terms"
	LegalPrivacy = "privacy"
)

// maxLegalBody is the most a document may hold, in characters: the table's bound and the contract's maxLength.
const maxLegalBody = 65536

// ErrConsentRequired is a registration or a console request before the current legal documents are accepted.
var ErrConsentRequired = errors.New("accounts: the current legal documents must be accepted")

// LegalDocument is one version of one of a site's legal documents. Its text never changes: a correction is a new
// version, so an acceptance keeps naming the text that was shown.
type LegalDocument struct {
	Kind        string
	Version     int
	Body        string
	PublishedAt time.Time
}

// DocumentVersion names a document version, which is what a page accepts and an acceptance records.
type DocumentVersion struct {
	Kind    string
	Version int
}

// LegalDocumentError is a document the admin API refuses; Field is the request member.
type LegalDocumentError struct{ Field, Reason string }

func (e *LegalDocumentError) Error() string { return "accounts: " + e.Field + " " + e.Reason }

// PublishLegalDocument adds the next version of kind for the site and records it. The site row is locked so two
// publications cannot choose the same version, and the audit entry carries a digest and the length rather than the
// text: a long document would exceed the entry's limit.
func (s *Service) PublishLegalDocument(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, kind, body, reason string) (LegalDocument, error) {
	if kind != LegalTerms && kind != LegalPrivacy {
		return LegalDocument{}, &LegalDocumentError{"kind", "must be terms or privacy"}
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
		SiteID: uuid.UUID(site), Kind: kind, Version: version, Body: body,
	})
	if err != nil {
		return LegalDocument{}, fmt.Errorf("insert the legal document: %w", err)
	}
	sum := sha256.Sum256([]byte(body))
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "site_legal_document.publish", Reason: reason,
		Target: audit.Target{Module: "accounts", Kind: "site", ID: site.TypeID(), Site: uuid.UUID(site)},
		Detail: map[string]any{
			"kind": kind, "version": version,
			"characters": utf8.RuneCountInString(body), "sha256": fmt.Sprintf("%x", sum),
		},
	}); err != nil {
		return LegalDocument{}, err
	}
	return LegalDocument{Kind: kind, Version: int(version), Body: body, PublishedAt: publishedAt.Time}, nil
}

// CurrentLegalDocuments reads the latest version of each kind for the site, which is what a page shows and a
// registration must accept. The text is public, so the read runs in the site's scope.
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
		documents[i] = LegalDocument{Kind: row.Kind, Version: int(row.Version), Body: row.Body, PublishedAt: row.PublishedAt.Time}
	}
	return documents, nil
}

// LegalDocumentHistory reads one page of every kind's versions, newest first within a kind, after the cursor position.
// It asks for one row more than the page so the caller knows whether a next page exists. The caller's transaction holds
// the platform scope.
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
		documents[i] = LegalDocument{Kind: row.Kind, Version: int(row.Version), Body: row.Body, PublishedAt: row.PublishedAt.Time}
	}
	return documents, more, nil
}

// PendingLegalDocuments is what the signed-in user has not accepted at its current version. An empty result means
// every current document is accepted, and a site with no documents always has an empty one.
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
	var pending []DocumentVersion
	for _, document := range current {
		if accepted[document.Kind] < document.Version {
			pending = append(pending, DocumentVersion{Kind: document.Kind, Version: document.Version})
		}
	}
	return pending, nil
}

func (s *Service) acceptedLegalVersions(ctx context.Context, tx pg.Tx, site SiteID, user UserID) (map[string]int, error) {
	rows, err := s.queries.AcceptedLegalVersions(ctx, tx, AcceptedLegalVersionsParams{SiteID: uuid.UUID(site), UserID: uuid.UUID(user)})
	if err != nil {
		return nil, fmt.Errorf("read the accepted legal versions: %w", err)
	}
	accepted := make(map[string]int, len(rows))
	for _, row := range rows {
		accepted[row.Kind] = int(row.Version)
	}
	return accepted, nil
}

// AcceptLegalDocuments records the user's acceptance of every current document at the version the request names. A
// missing, stale or extra version is ErrConsentRequired, so a page must send exactly what getLegalDocuments showed;
// accepting the same version again is a no-op. The site and user come from the session, never from the request.
func (s *Service) AcceptLegalDocuments(ctx context.Context, p Principal, accepted []DocumentVersion) error {
	return s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		documents, err := s.currentVersionsAccepted(ctx, tx, p.site, accepted)
		if err != nil {
			return err
		}
		return insertLegalAcceptances(ctx, tx, s.queries, p.site, p.account, p.user, documents)
	})
}

// currentVersionsAccepted checks that accepted names every current document at its current version, and returns that
// canonical set. A site with no documents requires no acceptance, and extra versions are refused: they mean the page
// was built from other documents than the ones now in force.
func (s *Service) currentVersionsAccepted(ctx context.Context, tx pg.Tx, site SiteID, accepted []DocumentVersion) ([]DocumentVersion, error) {
	current, err := s.currentLegalDocuments(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	given := make(map[string]int, len(accepted))
	for _, document := range accepted {
		if _, twice := given[document.Kind]; twice {
			return nil, ErrConsentRequired
		}
		given[document.Kind] = document.Version
	}
	if len(given) != len(current) {
		return nil, ErrConsentRequired
	}
	documents := make([]DocumentVersion, len(current))
	for i, document := range current {
		if given[document.Kind] != document.Version {
			return nil, ErrConsentRequired
		}
		documents[i] = DocumentVersion{Kind: document.Kind, Version: document.Version}
	}
	return documents, nil
}

// insertLegalAcceptances writes one row per document. The rows are not deleted or changed later: they are the record
// of what the person accepted and when.
func insertLegalAcceptances(ctx context.Context, tx pg.Tx, q *Queries, site SiteID, account AccountID, user UserID, documents []DocumentVersion) error {
	for _, document := range documents {
		if err := q.InsertLegalAcceptance(ctx, tx, InsertLegalAcceptanceParams{
			ID: uuid.NewV7(), SiteID: uuid.UUID(site), AccountID: uuid.UUID(account), UserID: uuid.UUID(user),
			Kind: document.Kind, Version: int32(document.Version), // #nosec G115 -- the version comes from the table, which checks it is at least 1
		}); err != nil {
			return fmt.Errorf("record a legal document acceptance: %w", err)
		}
	}
	return nil
}
