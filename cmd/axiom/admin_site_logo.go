package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// siteLogoView is a logo as the admin API writes it. preview adds the bytes as a data URL for the settings page; a
// change leaves it out, because the page reads the logo again and an idempotency key need not store the image twice.
func siteLogoView(logo *accounts.Logo, preview bool) adminapi.SiteLogoView {
	if logo == nil {
		return adminapi.SiteLogoView{}
	}
	body := adminapi.SiteLogo{
		ContentType: adminapi.SiteLogoContentType(logo.ContentType), Hash: logo.HashHex(),
		Size: len(logo.Bytes), Width: logo.Width, Height: logo.Height,
	}
	if preview {
		data := "data:" + logo.ContentType + ";base64," + base64.StdEncoding.EncodeToString(logo.Bytes)
		body.DataUrl = &data
	}
	return adminapi.SiteLogoView{Logo: &body}
}

// logoProblem is an upload the module refused as a 400 naming the logo.
func logoProblem(err error) error {
	if e, ok := errors.AsType[*accounts.SettingError](err); ok {
		return invalidField("/logo", e.Reason)
	}
	return err
}

// GetSiteLogoAdmin tells the site's current logo, with its bytes as a data URL for the settings page's preview.
func (a adminAPI) GetSiteLogoAdmin(ctx context.Context, request adminapi.GetSiteLogoAdminRequestObject) (adminapi.GetSiteLogoAdminResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	var logo *accounts.Logo
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		logo, err = a.accounts.CurrentSiteLogo(ctx, tx, id)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetSiteLogoAdmin200JSONResponse(siteLogoView(logo, true)), nil
}

// UpdateSiteLogo replaces the site's logo, or clears it when the request leaves logo out. The change is idempotent and
// audited; this node reloads the sites at once, the others within their reload interval.
func (a adminAPI) UpdateSiteLogo(ctx context.Context, request adminapi.UpdateSiteLogoRequestObject) (adminapi.UpdateSiteLogoResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	var contentType string
	var data []byte
	if request.Body.Logo != nil {
		contentType = string(request.Body.Logo.ContentType)
		data = request.Body.Logo.DataBase64
		if data == nil {
			data = []byte{}
		}
	}
	view, replayed, err := idempotently(ctx, a.db, "updateSiteLogo", request.Params.IdempotencyKey, []any{request.SiteId, request.Body},
		func(tx pg.Tx) (adminapi.SiteLogoView, error) {
			logo, err := a.accounts.SetSiteLogo(ctx, tx, actor(ctx), id, contentType, data, request.Body.Reason)
			if err != nil {
				return adminapi.SiteLogoView{}, logoProblem(err)
			}
			return siteLogoView(logo, false), nil
		})
	if err != nil {
		return nil, err
	}
	if !replayed {
		if err := a.accounts.Refresh(context.WithoutCancel(ctx)); err != nil {
			a.logger.WarnContext(ctx, "site logo saved but this node did not reload it", "site", request.SiteId, "err", err)
		}
	}
	return adminapi.UpdateSiteLogo200JSONResponse{
		Body: view, Headers: adminapi.UpdateSiteLogo200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

// GetAdminSite names the site whose admin host this request reached, so the app can show its brand before sign-in.
func (adminAPI) GetAdminSite(ctx context.Context, _ adminapi.GetAdminSiteRequestObject) (adminapi.GetAdminSiteResponseObject, error) {
	site := callOf(ctx).site
	body := adminapi.AdminSite{Name: site.Name}
	if site.LogoHash != "" {
		path := "/api/admin/v1/site/logo/" + site.LogoHash
		body.LogoUrl = &path
	}
	return adminapi.GetAdminSite200JSONResponse(body), nil
}

// GetAdminSiteLogo serves a logo by its content hash on the admin host, with the same immutable caching as the console's
// copy. The admin app shows the same brand before sign-in, and its host is already the site's admin domain.
//
//nolint:dupl // the console and admin contracts generate separate response types for the same bytes
func (a adminAPI) GetAdminSiteLogo(ctx context.Context, request adminapi.GetAdminSiteLogoRequestObject) (adminapi.GetAdminSiteLogoResponseObject, error) {
	raw, err := hex.DecodeString(request.Hash)
	if err != nil || len(raw) != sha256.Size {
		return nil, invalidField("hash", "not a logo hash")
	}
	var hash [sha256.Size]byte
	copy(hash[:], raw)
	logo, err := a.accounts.SiteLogoByHash(ctx, callOf(ctx).site.ID, hash)
	if err != nil {
		return nil, err
	}
	headers := adminapi.GetAdminSiteLogo200ResponseHeaders{CacheControl: logoServedFor}
	body, length := logoBody(logo)
	switch logo.ContentType {
	case accounts.LogoSVG:
		return adminapi.GetAdminSiteLogo200ImagesvgXmlResponse{Body: body, Headers: headers, ContentLength: length}, nil
	case accounts.LogoPNG:
		return adminapi.GetAdminSiteLogo200ImagepngResponse{Body: body, Headers: headers, ContentLength: length}, nil
	case accounts.LogoWebP:
		return adminapi.GetAdminSiteLogo200ImagewebpResponse{Body: body, Headers: headers, ContentLength: length}, nil
	default:
		return nil, fmt.Errorf("a stored logo has the content type %q", logo.ContentType)
	}
}
