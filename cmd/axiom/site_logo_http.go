package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/consoleapi"
)

// logoServedFor is the caching of a content-hash logo URL: the bytes never change under the hash.
const logoServedFor = "public, max-age=31536000, immutable"

// logoResponses are a contract's response types for a stored logo, one per media type: both surfaces generate their own
// type for the same bytes, so each handler supplies only this table and shares the rest.
type logoResponses[R any] struct {
	svg, png, webp func(body io.Reader, length int64) R
}

var (
	consoleLogos = logoResponses[consoleapi.GetSiteLogoResponseObject]{svg: consoleSVG, png: consolePNG, webp: consoleWebP}
	adminLogos   = logoResponses[adminapi.GetAdminSiteLogoResponseObject]{svg: adminSVG, png: adminPNG, webp: adminWebP}
)

func consoleSVG(body io.Reader, length int64) consoleapi.GetSiteLogoResponseObject {
	return consoleapi.GetSiteLogo200ImagesvgXmlResponse{Body: body, Headers: consoleapi.GetSiteLogo200ResponseHeaders{CacheControl: logoServedFor}, ContentLength: length}
}

func consolePNG(body io.Reader, length int64) consoleapi.GetSiteLogoResponseObject {
	return consoleapi.GetSiteLogo200ImagepngResponse{Body: body, Headers: consoleapi.GetSiteLogo200ResponseHeaders{CacheControl: logoServedFor}, ContentLength: length}
}

func consoleWebP(body io.Reader, length int64) consoleapi.GetSiteLogoResponseObject {
	return consoleapi.GetSiteLogo200ImagewebpResponse{Body: body, Headers: consoleapi.GetSiteLogo200ResponseHeaders{CacheControl: logoServedFor}, ContentLength: length}
}

func adminSVG(body io.Reader, length int64) adminapi.GetAdminSiteLogoResponseObject {
	return adminapi.GetAdminSiteLogo200ImagesvgXmlResponse{Body: body, Headers: adminapi.GetAdminSiteLogo200ResponseHeaders{CacheControl: logoServedFor}, ContentLength: length}
}

func adminPNG(body io.Reader, length int64) adminapi.GetAdminSiteLogoResponseObject {
	return adminapi.GetAdminSiteLogo200ImagepngResponse{Body: body, Headers: adminapi.GetAdminSiteLogo200ResponseHeaders{CacheControl: logoServedFor}, ContentLength: length}
}

func adminWebP(body io.Reader, length int64) adminapi.GetAdminSiteLogoResponseObject {
	return adminapi.GetAdminSiteLogo200ImagewebpResponse{Body: body, Headers: adminapi.GetAdminSiteLogo200ResponseHeaders{CacheControl: logoServedFor}, ContentLength: length}
}

// siteLogoResponse answers a logo URL on either surface: the hash names a stored logo, and the logo's content type picks
// the contract's response type.
func siteLogoResponse[R any](ctx context.Context, svc *accounts.Service, site accounts.SiteID, text string, r logoResponses[R]) (R, error) {
	var zero R
	raw, err := hex.DecodeString(text)
	if err != nil || len(raw) != sha256.Size {
		return zero, invalidField("hash", "not a logo hash")
	}
	var hash [sha256.Size]byte
	copy(hash[:], raw)
	logo, err := svc.SiteLogoByHash(ctx, site, hash)
	if err != nil {
		return zero, err
	}
	body, length := logoBody(logo)
	switch logo.ContentType {
	case accounts.LogoSVG:
		return r.svg(body, length), nil
	case accounts.LogoPNG:
		return r.png(body, length), nil
	case accounts.LogoWebP:
		return r.webp(body, length), nil
	default:
		return zero, fmt.Errorf("a stored logo has the content type %q", logo.ContentType)
	}
}

// logoBody is the bytes at a logo URL and their length, for the generated response type of either surface.
func logoBody(logo *accounts.Logo) (io.Reader, int64) {
	return bytes.NewReader(logo.Bytes), int64(len(logo.Bytes))
}
