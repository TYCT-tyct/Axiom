package main

import (
	"context"

	"axiom/internal/accounts"
	"axiom/internal/platform/consoleapi"
)

// GetLegalDocuments shows the site's current terms of service and privacy policy, which the registration and the
// consent pages display before anyone accepts them.
func (c consoleAPI) GetLegalDocuments(ctx context.Context, _ consoleapi.GetLegalDocumentsRequestObject) (consoleapi.GetLegalDocumentsResponseObject, error) {
	documents, err := c.accounts.CurrentLegalDocuments(ctx, callOf(ctx).site.ID)
	if err != nil {
		return nil, err
	}
	return consoleapi.GetLegalDocuments200JSONResponse(consoleapi.LegalDocuments{Documents: legalDocuments(documents)}), nil
}

// AcceptLegalDocuments records the signed-in user's acceptance of the versions the page showed.
func (c consoleAPI) AcceptLegalDocuments(ctx context.Context, request consoleapi.AcceptLegalDocumentsRequestObject) (consoleapi.AcceptLegalDocumentsResponseObject, error) {
	if err := c.accounts.AcceptLegalDocuments(ctx, callOf(ctx).principal, legalVersions(&request.Body.Consent)); err != nil {
		return nil, err
	}
	return consoleapi.AcceptLegalDocuments204Response{}, nil
}

// legalVersions converts the contract's versions to the module's. A nil request is no acceptance, which a site with
// documents refuses as consent_required.
func legalVersions(in *[]consoleapi.LegalDocumentVersion) []accounts.DocumentVersion {
	if in == nil {
		return nil
	}
	out := make([]accounts.DocumentVersion, len(*in))
	for i, document := range *in {
		out[i] = accounts.DocumentVersion{Kind: string(document.Kind), Version: document.Version}
	}
	return out
}

// legalVersionList converts the module's versions to the contract's, as a non-nil list so an empty one is sent as [].
func legalVersionList(in []accounts.DocumentVersion) []consoleapi.LegalDocumentVersion {
	out := make([]consoleapi.LegalDocumentVersion, len(in))
	for i, document := range in {
		out[i] = consoleapi.LegalDocumentVersion{Kind: consoleapi.LegalDocumentKind(document.Kind), Version: document.Version}
	}
	return out
}

func legalDocuments(in []accounts.LegalDocument) []consoleapi.LegalDocument {
	out := make([]consoleapi.LegalDocument, len(in))
	for i, document := range in {
		out[i] = consoleapi.LegalDocument{
			Kind: consoleapi.LegalDocumentKind(document.Kind), Version: document.Version,
			Body: document.Body, PublishedAt: document.PublishedAt,
		}
	}
	return out
}
