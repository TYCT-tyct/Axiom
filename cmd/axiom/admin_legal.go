package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

const legalHistoryPageSize = 50

// ListLegalDocuments reads one page of every published version of a site's terms and privacy policy, with the text, so
// an administrator can see what users accepted. The caller's scope is the platform's, which the admin guard authorized.
func (a adminAPI) ListLegalDocuments(ctx context.Context, request adminapi.ListLegalDocumentsRequestObject) (adminapi.ListLegalDocumentsResponseObject, error) {
	site, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	after, hasAfter, err := legalHistoryCursor(request.Params.Cursor)
	if err != nil {
		return nil, err
	}
	var cursor *accounts.DocumentVersion
	if hasAfter {
		cursor = &after
	}
	limit := legalHistoryPageSize
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	var documents []accounts.LegalDocument
	var more bool
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		var err error
		documents, more, err = a.accounts.LegalDocumentHistory(ctx, tx, site, cursor, limit)
		return err
	}); err != nil {
		return nil, err
	}
	var next *string
	if more {
		last := documents[len(documents)-1]
		// Emit the cursor an old node parses. New nodes accept both this and kind:locale:version; the cleanup migration
		// switches the emitted form only after old nodes have exited.
		cursor := fmt.Sprintf("%s:%d", last.Kind, last.Version)
		next = &cursor
	}
	return adminapi.ListLegalDocuments200JSONResponse(adminapi.LegalDocumentHistory{
		Documents: adminLegalDocuments(documents), NextCursor: next,
	}), nil
}

// legalHistoryCursor reads a next_cursor as the (kind, version) position. New nodes also accept the transitional
// kind:locale:version form, and reduce it to the same position: the compatibility phase still keeps versions unique
// across locales, so an old node can continue from a cursor a new node emitted. The order is the query's, so a cursor
// after the last terms version continues with the privacy versions, and a malformed one is a 400.
func legalHistoryCursor(raw *adminapi.Cursor) (accounts.DocumentVersion, bool, error) {
	if raw == nil {
		return accounts.DocumentVersion{}, false, nil
	}
	parts := strings.Split(*raw, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return accounts.DocumentVersion{}, false, invalidField("/cursor", "is not a legal document cursor")
	}
	kind := parts[0]
	if kind != accounts.LegalTerms && kind != accounts.LegalPrivacy {
		return accounts.DocumentVersion{}, false, invalidField("/cursor", "is not a legal document cursor")
	}
	locale := ""
	versionText := parts[1]
	if len(parts) == 3 {
		locale = parts[1]
		versionText = parts[2]
		if locale != accounts.LegalLocaleZH && locale != accounts.LegalLocaleEN {
			return accounts.DocumentVersion{}, false, invalidField("/cursor", "is not a legal document cursor")
		}
	}
	version, err := strconv.ParseInt(versionText, 10, 32)
	if err != nil || version < 1 {
		return accounts.DocumentVersion{}, false, invalidField("/cursor", "is not a legal document cursor")
	}
	return accounts.DocumentVersion{Kind: kind, Locale: locale, Version: int(version)}, true, nil
}

// PublishLegalDocument adds a version of a kind. The text is the owner's, so it is stored as given; the audit entry
// records a digest and the length instead.
func (a adminAPI) PublishLegalDocument(ctx context.Context, request adminapi.PublishLegalDocumentRequestObject) (adminapi.PublishLegalDocumentResponseObject, error) {
	site, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	document, replayed, err := idempotently(ctx, a.db, "publishLegalDocument", request.Params.IdempotencyKey, []any{request.SiteId, request.Body},
		func(tx pg.Tx) (adminapi.LegalDocument, error) {
			locale := accounts.LegalLocaleZH
			if request.Body.Locale != nil {
				locale = string(*request.Body.Locale)
			}
			document, err := a.accounts.PublishLegalDocument(ctx, tx, actor(ctx), site, string(request.Body.Kind), locale, request.Body.Body, request.Body.Reason)
			if err != nil {
				if refusal, ok := errors.AsType[*accounts.LegalDocumentError](err); ok {
					return adminapi.LegalDocument{}, invalidField("/"+refusal.Field, refusal.Reason)
				}
				return adminapi.LegalDocument{}, err
			}
			return adminapi.LegalDocument{
				Kind: adminapi.LegalDocumentKind(document.Kind), Locale: legalLocalePtr(document.Locale), Version: document.Version,
				Body: document.Body, PublishedAt: document.PublishedAt,
			}, nil
		})
	if err != nil {
		return nil, err
	}
	return adminapi.PublishLegalDocument201JSONResponse{
		Body: document, Headers: adminapi.PublishLegalDocument201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func adminLegalDocuments(in []accounts.LegalDocument) []adminapi.LegalDocument {
	out := make([]adminapi.LegalDocument, len(in))
	for i, document := range in {
		out[i] = adminapi.LegalDocument{
			Kind: adminapi.LegalDocumentKind(document.Kind), Locale: legalLocalePtr(document.Locale), Version: document.Version,
			Body: document.Body, PublishedAt: document.PublishedAt,
		}
	}
	return out
}

func legalLocalePtr(locale string) *adminapi.LegalDocumentLocale {
	value := adminapi.LegalDocumentLocale(locale)
	return &value
}
