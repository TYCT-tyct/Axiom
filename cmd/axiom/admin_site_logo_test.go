package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
)

type logoUpload struct {
	logo *adminapi.SiteLogo
	body string
	key  string
	path string
}

// uploadLogo sends one sanitised SVG and returns what the server answered and what it sent.
func uploadLogo(t *testing.T, f adminFixture, s session) logoUpload {
	t.Helper()
	path := "/sites/" + f.siteID() + "/logo"
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><script>alert(1)</script><path fill="#123" d="M0 0h24v24H0z"/></svg>`)
	body := `{"logo":{"content_type":"image/svg+xml","data_base64":"` + base64.StdEncoding.EncodeToString(svg) + `"},"reason":"brand"}`
	key := newKey()
	rec := f.change(s, http.MethodPut, path, body, key)
	expectAdmin(t, "updateSiteLogo", rec, http.StatusOK, "")
	var view adminapi.SiteLogoView
	decode(t, rec, &view)
	if view.Logo == nil {
		t.Fatal("the upload answered no logo")
	}
	return logoUpload{logo: view.Logo, body: body, key: key, path: path}
}

// TestSiteLogoEmpty: a site without a logo answers with no logo, not a 404.
func TestSiteLogoEmpty(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	if view := f.getLogo(s, "/sites/"+f.siteID()+"/logo"); view.Logo != nil {
		t.Fatalf("a site without a logo: %+v", view.Logo)
	}
}

// TestSiteLogoUpload: an operator uploads an SVG, the snapshot names its hash, and the change is audited.
func TestSiteLogoUpload(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()

	up := uploadLogo(t, f, s)
	logo := up.logo
	if logo.ContentType != adminapi.SiteLogoContentType("image/svg+xml") || logo.Width != 24 || logo.Height != 24 ||
		logo.Size == 0 || len(logo.Hash) != 64 || logo.DataUrl != nil {
		t.Fatalf("the uploaded logo: %+v", logo)
	}
	siteID, err := accounts.ParseSiteID(f.siteID())
	if err != nil {
		t.Fatal(err)
	}
	siteNow, ok := f.svc.Site(siteID)
	if !ok || siteNow.LogoHash != logo.Hash {
		t.Errorf("the snapshot's logo hash is %q, want %q", siteNow.LogoHash, logo.Hash)
	}
	f.expectAudit("site_logo.update", map[string]string{
		"reason":                           "brand",
		"detail->'before'->>'present'":     "false",
		"detail->'after'->>'present'":      "true",
		"detail->'after'->>'hash'":         logo.Hash,
		"detail->'after'->>'content_type'": "image/svg+xml",
		"detail->'after'->>'size'":         strconv.Itoa(logo.Size),
	})
}

// TestSiteLogoPreview: the settings page reads the sanitised bytes back as a data URL.
func TestSiteLogoPreview(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	up := uploadLogo(t, f, s)

	got := f.getLogo(s, up.path)
	if got.Logo == nil || got.Logo.Hash != up.logo.Hash || got.Logo.DataUrl == nil {
		t.Fatalf("getSiteLogoAdmin after the upload: %+v", got)
	}
	prefix := "data:image/svg+xml;base64,"
	if !strings.HasPrefix(*got.Logo.DataUrl, prefix) {
		t.Fatalf("the preview is %q", (*got.Logo.DataUrl)[:min(30, len(*got.Logo.DataUrl))])
	}
	preview, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(*got.Logo.DataUrl, prefix))
	if err != nil || !bytes.Contains(preview, []byte("<svg")) || bytes.Contains(preview, []byte("script")) {
		t.Errorf("the preview is not the sanitised SVG: %v %q", err, preview)
	}
}

// TestSiteLogoReplay: the same request under the same key is replayed, not done again; another body under the key is
// refused.
func TestSiteLogoReplay(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	up := uploadLogo(t, f, s)

	rec := f.change(s, http.MethodPut, up.path, up.body, up.key)
	expectAdmin(t, "the same upload again", rec, http.StatusOK, "")
	var replayed adminapi.SiteLogoView
	decode(t, rec, &replayed)
	if replayed.Logo == nil || replayed.Logo.Hash != up.logo.Hash || rec.Header().Get("Idempotency-Replayed") != "true" ||
		f.auditCount("site_logo.update") != "1" {
		t.Errorf("the replay replayed=%q audits=%s: %+v", rec.Header().Get("Idempotency-Replayed"), f.auditCount("site_logo.update"), replayed)
	}
	other := `{"logo":{"content_type":"image/svg+xml","data_base64":"` + base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)) + `"},"reason":"brand"}`
	expectAdmin(t, "the key with another body", f.change(s, http.MethodPut, up.path, other, up.key), http.StatusConflict, codeIdempotencyReuse)
}

// TestSiteLogoBadUpload: a refused upload names the logo, is not audited, and leaves the site as it was.
func TestSiteLogoBadUpload(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	path := "/sites/" + f.siteID() + "/logo"

	bad := f.change(s, http.MethodPut, path, `{"logo":{"content_type":"image/png","data_base64":"aGVsbG8="},"reason":"brand"}`, newKey())
	expectAdmin(t, "a file that is not a PNG", bad, http.StatusBadRequest, codeInvalidRequest)
	if fields := errorDetailOf(t, bad).Fields; fields == nil || len(*fields) != 1 || (*fields)[0].Pointer != "/logo" {
		t.Errorf("a refused logo's fields: %v", fields)
	}
	if f.auditCount("site_logo.update") != "0" {
		t.Error("a refused upload was audited")
	}
	if view := f.getLogo(s, path); view.Logo != nil {
		t.Errorf("a refused upload left a logo: %+v", view.Logo)
	}
}

// TestSiteLogoClear: clearing removes the current logo; the old URL still serves what it named, and both changes are
// audited.
func TestSiteLogoClear(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	up := uploadLogo(t, f, s)

	rec := f.change(s, http.MethodPut, up.path, `{"reason":"remove"}`, newKey())
	expectAdmin(t, "clear the logo", rec, http.StatusOK, "")
	var cleared adminapi.SiteLogoView
	decode(t, rec, &cleared)
	if cleared.Logo != nil {
		t.Errorf("a cleared logo: %+v", cleared.Logo)
	}
	if got := f.getLogo(s, up.path); got.Logo != nil {
		t.Errorf("getSiteLogoAdmin after clearing: %+v", got.Logo)
	}
	siteID, err := accounts.ParseSiteID(f.siteID())
	if err != nil {
		t.Fatal(err)
	}
	if siteNow, _ := f.svc.Site(siteID); siteNow.LogoHash != "" {
		t.Errorf("the snapshot still names %q", siteNow.LogoHash)
	}
	raw, err := hex.DecodeString(up.logo.Hash)
	if err != nil {
		t.Fatal(err)
	}
	var hash [32]byte
	copy(hash[:], raw)
	old, err := f.svc.SiteLogoByHash(t.Context(), siteID, hash)
	if err != nil || old == nil || !bytes.Contains(old.Bytes, []byte("<svg")) {
		t.Errorf("the old logo after clearing: %v %+v", err, old)
	}
	if f.auditCount("site_logo.update") != "2" {
		t.Errorf("%s audit entries, want 2", f.auditCount("site_logo.update"))
	}
}

// TestAdminSiteBrand: the admin host tells its site's name and logo without a session, and serves the logo at the URL it
// gives with the same immutable caching as the console.
func TestAdminSiteBrand(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	before := f.do(http.MethodGet, "/site", "")
	expectAdmin(t, "getAdminSite before a logo", before, http.StatusOK, "")
	var brand adminapi.AdminSite
	decode(t, before, &brand)
	if brand.Name == "" || brand.LogoUrl != nil {
		t.Fatalf("the admin site before a logo: %+v", brand)
	}

	up := uploadLogo(t, f, f.owner())
	rec := f.do(http.MethodGet, "/site", "")
	expectAdmin(t, "getAdminSite", rec, http.StatusOK, "")
	decode(t, rec, &brand)
	want := "/api/admin/v1/site/logo/" + up.logo.Hash
	if brand.Name == "" || brand.LogoUrl == nil || *brand.LogoUrl != want {
		t.Fatalf("the admin site's brand is %+v, want logo %q", brand, want)
	}

	logo := f.do(http.MethodGet, "/site/logo/"+up.logo.Hash, "")
	if logo.Code != http.StatusOK || logo.Header().Get("Content-Type") != "image/svg+xml" ||
		logo.Header().Get("Cache-Control") != logoServedFor || !bytes.Contains(logo.Body.Bytes(), []byte("<svg")) {
		t.Errorf("the public logo: %d %q %q %q", logo.Code, logo.Header().Get("Content-Type"),
			logo.Header().Get("Cache-Control"), logo.Body.String())
	}
	expectAdmin(t, "a hash the site never served", f.do(http.MethodGet, "/site/logo/"+strings.Repeat("0", 64), ""),
		http.StatusNotFound, codeNotFound)
}

// TestSiteLogoRefusals: a site that is not there, or no reason, is answered as the contract says.
func TestSiteLogoRefusals(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	for _, id := range []string{"site_00000000000000000000000000", "site_zzzzzzzzzzzzzzzzzzzzzzzzzz", "acct_00000000000000000000000000"} {
		expectAdmin(t, "getSiteLogoAdmin of "+id, f.do(http.MethodGet, "/sites/"+id+"/logo", "", as(s)), http.StatusNotFound, codeNotFound)
		expectAdmin(t, "updateSiteLogo of "+id, f.change(s, http.MethodPut, "/sites/"+id+"/logo", `{"reason":"r"}`, newKey()), http.StatusNotFound, codeNotFound)
	}
	expectAdmin(t, "no reason", f.change(s, http.MethodPut, "/sites/"+f.siteID()+"/logo", `{}`, newKey()), http.StatusBadRequest, codeInvalidRequest)
	expectAdmin(t, "an empty file", f.change(s, http.MethodPut, "/sites/"+f.siteID()+"/logo",
		`{"logo":{"content_type":"image/png","data_base64":""},"reason":"r"}`, newKey()), http.StatusBadRequest, codeInvalidRequest)
}

func (f adminFixture) getLogo(s session, path string) adminapi.SiteLogoView {
	f.t.Helper()
	rec := f.do(http.MethodGet, path, "", as(s))
	expectAdmin(f.t, "getSiteLogoAdmin", rec, http.StatusOK, "")
	var view adminapi.SiteLogoView
	decode(f.t, rec, &view)
	return view
}
