package main

import (
	"context"
	"errors"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// ListLegalDocuments reads every published version of a site's terms and privacy policy, with the text, so an
// administrator can see what users accepted. The caller's scope is the platform's, which the admin guard authorized.
func (a adminAPI) ListLegalDocuments(ctx context.Context, request adminapi.ListLegalDocumentsRequestObject) (adminapi.ListLegalDocumentsResponseObject, error) {
	site, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	var documents []accounts.LegalDocument
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		var err error
		documents, err = a.accounts.LegalDocumentHistory(ctx, tx, site)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.ListLegalDocuments200JSONResponse(adminapi.LegalDocumentHistory{Documents: adminLegalDocuments(documents)}), nil
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
