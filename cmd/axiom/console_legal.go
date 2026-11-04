package main

import (
	"context"

	"axiom/internal/accounts"
	"axiom/internal/platform/consoleapi"
)

// GetLegalDocuments shows the site's current terms of service and privacy policy, which the registration and the
// consent pages display before anyone accepts them.
func (c consoleAPI) GetLegalDocuments(ctx context.Context, request consoleapi.GetLegalDocumentsRequestObject) (consoleapi.GetLegalDocumentsResponseObject, error) {
	documents, err := c.accounts.CurrentLegalDocuments(ctx, callOf(ctx).site.ID)
	if err != nil {
		return nil, err
	}
	all := request.Params.Locales != nil && *request.Params.Locales == consoleapi.GetLegalDocumentsParamsLocales("all")
	if !all {
		return consoleapi.GetLegalDocuments200JSONResponse(consoleapi.LegalDocuments{Documents: legacyLegalDocuments(documents)}), nil
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
// documents refuses as consent_required. A missing locale stays empty: it is an old page's request, and the accounts
// layer resolves it from the current kind and version.
func legalVersions(in *[]consoleapi.LegalDocumentVersion) []accounts.DocumentVersion {
	if in == nil {
		return nil
	}
	out := make([]accounts.DocumentVersion, len(*in))
	for i, document := range *in {
		locale := ""
		if document.Locale != nil {
			locale = string(*document.Locale)
		}
		out[i] = accounts.DocumentVersion{Kind: string(document.Kind), Locale: locale, Version: document.Version}
	}
	return out
}

// legalVersionList converts the module's versions to the contract's, as a non-nil list so an empty one is sent as [].
func legalVersionList(in []accounts.DocumentVersion) []consoleapi.LegalDocumentVersion {
	out := make([]consoleapi.LegalDocumentVersion, len(in))
	for i, document := range in {
		out[i] = consoleapi.LegalDocumentVersion{
			Kind: consoleapi.LegalDocumentKind(document.Kind), Locale: consoleLegalLocalePtr(document.Locale), Version: document.Version,
		}
	}
	return out
}

// legacyLegalDocuments keeps the pre-locale response an old page expects: one current document per kind, highest
// version first, and no locale member. A new page asks for `locales=all` and gets every published locale.
func legacyLegalDocuments(in []accounts.LegalDocument) []consoleapi.LegalDocument {
	chosen := make(map[string]accounts.LegalDocument, 2)
	for _, document := range in {
		if previous, ok := chosen[document.Kind]; !ok || document.Version > previous.Version {
			chosen[document.Kind] = document
		}
	}
	out := make([]consoleapi.LegalDocument, 0, len(chosen))
	for _, kind := range []string{accounts.LegalPrivacy, accounts.LegalTerms} {
		document, ok := chosen[kind]
		if !ok {
			continue
		}
		out = append(out, consoleapi.LegalDocument{
			Kind: consoleapi.LegalDocumentKind(document.Kind), Version: document.Version,
			Body: document.Body, PublishedAt: document.PublishedAt,
		})
	}
	return out
}

func legalDocuments(in []accounts.LegalDocument) []consoleapi.LegalDocument {
	out := make([]consoleapi.LegalDocument, len(in))
	for i, document := range in {
		out[i] = consoleapi.LegalDocument{
			Kind: consoleapi.LegalDocumentKind(document.Kind), Locale: consoleLegalLocalePtr(document.Locale), Version: document.Version,
			Body: document.Body, PublishedAt: document.PublishedAt,
		}
	}
	return out
}

func consoleLegalLocalePtr(locale string) *consoleapi.LegalDocumentLocale {
	value := consoleapi.LegalDocumentLocale(locale)
	return &value
}
