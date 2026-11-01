package main

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/platform/consoleapi"
)

// TestSiteLogoIsPublicAndLongCached: getSite names the logo of the site the Host belongs to, the URL carries its hash
// and answers without a session with a year-long immutable cache, and clearing the logo leaves the old URL working.
func TestSiteLogoIsPublicAndLongCached(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	site := f.siteID()
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><path fill="#123" d="M0 0h24v24H0z"/></svg>`)
	body := `{"logo":{"content_type":"image/svg+xml","data_base64":"` + base64.StdEncoding.EncodeToString(svg) + `"},"reason":"brand"}`
	rec := f.change(s, http.MethodPut, "/sites/"+site+"/logo", body, newKey())
	expectAdmin(t, "updateSiteLogo", rec, http.StatusOK, "")

	c := startNode(f.commandLine, f.clock)
	var got consoleapi.Site
	decode(t, c.do(http.MethodGet, "/site", ""), &got)
	if got.LogoUrl == nil || !strings.HasPrefix(*got.LogoUrl, "/api/console/v1/site/logo/") {
		t.Fatalf("the site's logo URL: %v", got.LogoUrl)
	}
	hash := strings.TrimPrefix(*got.LogoUrl, "/api/console/v1/site/logo/")

	logo := c.do(http.MethodGet, "/site/logo/"+hash, "")
	if logo.Code != http.StatusOK {
		t.Fatalf("the logo: %d %s", logo.Code, logo.Body)
	}
	if logo.Header().Get("Content-Type") != "image/svg+xml" ||
		logo.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		logo.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("the logo's headers: %v", logo.Header())
	}
	if !bytes.Contains(logo.Body.Bytes(), []byte("<svg")) || int64(logo.Body.Len()) != logo.Result().ContentLength {
		t.Errorf("the logo's body (%d bytes, length %d) is not the SVG", logo.Body.Len(), logo.Result().ContentLength)
	}
	expect(t, "a hash the site never served", c.do(http.MethodGet, "/site/logo/"+strings.Repeat("0", 64), ""), http.StatusNotFound, consoleapi.ErrorCodeNotFound)
	expect(t, "the logo at another site's admin host", c.do(http.MethodGet, "/site/logo/"+hash, "", atHost(adminHost)), http.StatusNotFound, consoleapi.ErrorCodeNotFound)

	// Clearing the site's logo changes what getSite says; the old URL still names the bytes it named.
	rec = f.change(s, http.MethodPut, "/sites/"+site+"/logo", `{"reason":"remove"}`, newKey())
	expectAdmin(t, "clear the logo", rec, http.StatusOK, "")
	c2 := startNode(f.commandLine, f.clock)
	var cleared consoleapi.Site
	decode(t, c2.do(http.MethodGet, "/site", ""), &cleared)
	if cleared.LogoUrl != nil {
		t.Errorf("getSite after clearing: %v", *cleared.LogoUrl)
	}
	expect(t, "the old logo URL after clearing", c2.do(http.MethodGet, "/site/logo/"+hash, ""), http.StatusOK, "")
}
