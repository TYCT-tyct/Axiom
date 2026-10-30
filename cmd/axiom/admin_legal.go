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
		cursor := fmt.Sprintf("%s:%d", documents[len(documents)-1].Kind, documents[len(documents)-1].Version)
		next = &cursor
	}
	return adminapi.ListLegalDocuments200JSONResponse(adminapi.LegalDocumentHistory{
		Documents: adminLegalDocuments(documents), NextCursor: next,
	}), nil
}

// legalHistoryCursor reads a next_cursor as the (kind, version) it names. The kind order is the query's, so a cursor
// after the last terms version continues with the privacy versions, and a malformed one is a 400.
func legalHistoryCursor(raw *adminapi.Cursor) (accounts.DocumentVersion, bool, error) {
	if raw == nil {
		return accounts.DocumentVersion{}, false, nil
	}
	kind, versionText, ok := strings.Cut(*raw, ":")
	if !ok || (kind != accounts.LegalTerms && kind != accounts.LegalPrivacy) {
		return accounts.DocumentVersion{}, false, invalidField("/cursor", "is not a legal document cursor")
	}
	version, err := strconv.Atoi(versionText)
	if err != nil || version < 1 {
		return accounts.DocumentVersion{}, false, invalidField("/cursor", "is not a legal document cursor")
	}
	return accounts.DocumentVersion{Kind: kind, Version: version}, true, nil
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
			document, err := a.accounts.PublishLegalDocument(ctx, tx, actor(ctx), site, string(request.Body.Kind), request.Body.Body, request.Body.Reason)
			if err != nil {
				if refusal, ok := errors.AsType[*accounts.LegalDocumentError](err); ok {
					return adminapi.LegalDocument{}, invalidField("/"+refusal.Field, refusal.Reason)
				}
				return adminapi.LegalDocument{}, err
			}
			return adminapi.LegalDocument{
				Kind: adminapi.LegalDocumentKind(document.Kind), Version: document.Version,
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
			Kind: adminapi.LegalDocumentKind(document.Kind), Version: document.Version,
			Body: document.Body, PublishedAt: document.PublishedAt,
		}
	}
	return out
}
