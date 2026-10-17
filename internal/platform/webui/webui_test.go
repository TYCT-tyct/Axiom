package webui

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const policy = "default-src 'self'; script-src 'self'"

// build is a frontend's embedded files as web/scripts/embed.ts lays them out. Each file's body names it, and the
// precompressed copies' bodies are markers, not real compression: the server sends them as they are.
func build() fstest.MapFS {
	file := func(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }
	return fstest.MapFS{
		"content-security-policy.txt":      file(policy + "\n"),
		"site/index.html":                  file("<site>"),
		"site/assets/index-AAAAAAAA.js":    file("site js"),
		"site/assets/index-AAAAAAAA.js.br": file("site js, brotli"),
		"site/assets/index-AAAAAAAA.js.gz": file("site js, gzip"),
		"site/assets/inter-BBBBBBBB.woff2": file("font"),
		"site/assets/loose.js":             file("a name without a hash"),
		"console/index.html":               file("<console>"),
		"console/assets/page-CCCCCCCC.js":  file("console js"),
		"admin/index.html":                 file("<admin>"),
		"admin/assets/admin-DDDDDDDD.js":   file("admin js"),
	}
}

func frontend(t *testing.T) *Frontend {
	t.Helper()
	f, err := New(build())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

type request struct {
	method, target string
	header         http.Header
}

// reply is what a test reads of a response: its status and headers.
type reply struct {
	StatusCode int
	Header     http.Header
}

func (r request) serve(t *testing.T, h http.Handler) (reply, string) {
	t.Helper()
	method := r.method
	if method == "" {
		method = http.MethodGet
	}
	req := httptest.NewRequestWithContext(t.Context(), method, r.target, nil)
	maps.Copy(req.Header, r.header)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return reply{rec.Code, rec.Header()}, rec.Body.String()
}

func navigate(target string) request {
	return request{target: target, header: http.Header{"Accept": {"text/html,application/xhtml+xml,*/*;q=0.8"}}}
}

// TestCacheControl: a name carrying its content's hash is cached for good, and everything else, the entry pages above
// all, is not stored, so a deployment is never hidden behind a cached page.
func TestCacheControl(t *testing.T) {
	cases := []struct {
		name string
		req  request
		want string
	}{
		{"the website's entry page", request{target: "/"}, "no-store"},
		{"the console's entry page", request{target: "/console/"}, "no-store"},
		{"an entry page asked for by name", request{target: "/index.html"}, "no-store"},
		{"a page the router answers", navigate("/console/keys"), "no-store"},
		{"a hashed script", request{target: "/assets/index-AAAAAAAA.js"}, "public, max-age=31536000, immutable"},
		{"a hashed font", request{target: "/assets/inter-BBBBBBBB.woff2"}, "public, max-age=31536000, immutable"},
		{"a hashed script of the console", request{target: "/console/assets/page-CCCCCCCC.js"}, "public, max-age=31536000, immutable"},
		{"a file under assets without a hash", request{target: "/assets/loose.js"}, "no-store"},
		{"a file that is gone", request{target: "/assets/index-ZZZZZZZZ.js"}, "no-store"},
	}
	h := frontend(t).Public()
	for _, c := range cases {
		resp, _ := c.req.serve(t, h)
		if got := resp.Header.Get("Cache-Control"); got != c.want {
			t.Errorf("%s: Cache-Control %q, want %q", c.name, got, c.want)
		}
	}
}

// TestEncoding: a client gets the variant it weights highest, brotli before gzip when it likes them alike, and the
// file as it is when it accepts neither or refuses them; the answer says it depends on the header.
func TestEncoding(t *testing.T) {
	cases := []struct {
		accept       []string
		wantEncoding string
		wantBody     string
	}{
		{nil, "", "site js"},
		{[]string{"identity"}, "", "site js"},
		{[]string{"br"}, "br", "site js, brotli"},
		{[]string{"gzip, deflate, br, zstd"}, "br", "site js, brotli"},
		{[]string{"gzip"}, "gzip", "site js, gzip"},
		{[]string{"*"}, "br", "site js, brotli"},
		{[]string{"BR;Q=0.5, GZIP;q=0.9"}, "gzip", "site js, gzip"},
		{[]string{"br;q=0, gzip"}, "gzip", "site js, gzip"},
		{[]string{"gzip;q=0, br;q=0"}, "", "site js"},
		{[]string{"*;q=0"}, "", "site js"},
		{[]string{"gzip;q=0, *"}, "br", "site js, brotli"},
		{[]string{"deflate", "gzip"}, "gzip", "site js, gzip"}, // two header lines are one list
		{[]string{"br;q=high, gzip"}, "gzip", "site js, gzip"}, // an unreadable weight says nothing about br
	}
	h := frontend(t).Public()
	for _, c := range cases {
		resp, body := request{target: "/assets/index-AAAAAAAA.js", header: http.Header{"Accept-Encoding": c.accept}}.serve(t, h)
		if resp.Header.Get("Content-Encoding") != c.wantEncoding || body != c.wantBody ||
			resp.Header.Get("Vary") != "Accept-Encoding" || resp.Header.Get("Content-Type") != "text/javascript; charset=utf-8" {
			t.Errorf("Accept-Encoding %q: got %q %q (Vary %q, %q), want %q %q", c.accept, resp.Header.Get("Content-Encoding"), body,
				resp.Header.Get("Vary"), resp.Header.Get("Content-Type"), c.wantEncoding, c.wantBody)
		}
	}

	// A file with no variants is sent as it is to a client that accepts everything, and does not vary.
	resp, body := request{target: "/assets/inter-BBBBBBBB.woff2", header: http.Header{"Accept-Encoding": {"br, gzip"}}}.serve(t, h)
	if resp.Header.Get("Content-Encoding") != "" || body != "font" || resp.Header.Get("Vary") != "" {
		t.Errorf("a font: %q %q (Vary %q)", resp.Header.Get("Content-Encoding"), body, resp.Header.Get("Vary"))
	}
	// A variant is no file of its own.
	if resp, _ := (request{target: "/assets/index-AAAAAAAA.js.br"}).serve(t, h); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the precompressed copy is served under its own name: %d", resp.StatusCode)
	}
}

// TestHead: the answer to HEAD has the headers of the GET, the length of the encoded body included, and no body.
func TestHead(t *testing.T) {
	resp, body := request{method: http.MethodHead, target: "/assets/index-AAAAAAAA.js", header: http.Header{"Accept-Encoding": {"gzip"}}}.serve(t, frontend(t).Public())
	if resp.StatusCode != http.StatusOK || body != "" || resp.Header.Get("Content-Length") != "13" || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("HEAD: %d %q length %q encoding %q", resp.StatusCode, body, resp.Header.Get("Content-Length"), resp.Header.Get("Content-Encoding"))
	}
}

// TestSinglePageFallback: the entry page answers a browser's navigation to a path of the app's router, and nothing
// else that is unknown: a missing script or an unclaimed API path is a 404 a client can act on, not a page.
func TestSinglePageFallback(t *testing.T) {
	json := http.Header{"Accept": {"application/json"}}
	cases := []struct {
		name       string
		req        request
		wantStatus int
		wantBody   string
	}{
		{"the website's router", navigate("/pricing"), 200, "<site>"},
		{"a deep path of the website", navigate("/models/anthropic/claude/"), 200, "<site>"},
		{"the console's router", navigate("/console/keys"), 200, "<console>"},
		{"the console's root", navigate("/console/"), 200, "<console>"},
		{"a script of a build that is gone", navigate("/console/assets/page-ZZZZZZZZ.js"), 404, "Not Found\n"},
		{"a navigation to a file name", navigate("/missing.html"), 404, "Not Found\n"},
		{"a path the client does not want a page for", request{target: "/pricing", header: json}, 404, "Not Found\n"},
		{"a path with no Accept at all", request{target: "/pricing"}, 404, "Not Found\n"},
		{"an API path, whatever is accepted", navigate("/api/console/v1/nothing-here"), 404, "Not Found\n"},
		{"an API path of the model calls", navigate("/v1/chat/completions"), 404, "Not Found\n"},
		{"another method on an API path", request{method: http.MethodPut, target: "/api/console/v1/keys"}, 404, "Not Found\n"},
		{"a method pages do not take", request{method: http.MethodPost, target: "/"}, 405, "Method Not Allowed\n"},
	}
	h := frontend(t).Public()
	for _, c := range cases {
		resp, body := c.req.serve(t, h)
		if resp.StatusCode != c.wantStatus || body != c.wantBody {
			t.Errorf("%s: %d %q, want %d %q", c.name, resp.StatusCode, body, c.wantStatus, c.wantBody)
		}
	}
	resp, _ := request{method: http.MethodPost, target: "/"}.serve(t, h)
	if resp.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("Allow %q", resp.Header.Get("Allow"))
	}
}

// TestConsoleMount: /console without its slash goes to the console, keeping the query.
func TestConsoleMount(t *testing.T) {
	for target, want := range map[string]string{"/console": "/console/", "/console?next=%2Fkeys": "/console/?next=%2Fkeys"} {
		resp, _ := request{target: target}.serve(t, frontend(t).Public())
		if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != want {
			t.Errorf("%s: %d to %q, want %q", target, resp.StatusCode, resp.Header.Get("Location"), want)
		}
	}
}

// TestAdminIsOnItsOwn: the public handler holds none of the admin's files, whatever path asks for them, and the admin
// handler serves them.
func TestAdminIsOnItsOwn(t *testing.T) {
	f := frontend(t)
	for _, target := range []string{"/admin/", "/admin/index.html", "/admin/assets/admin-DDDDDDDD.js", "/assets/admin-DDDDDDDD.js", "/console/assets/admin-DDDDDDDD.js", "/../admin/index.html", "/console/../admin/index.html"} {
		for _, req := range []request{{target: target}, navigate(target)} {
			if _, body := req.serve(t, f.Public()); body == "<admin>" || body == "admin js" {
				t.Errorf("%s on the public handler: %q", target, body)
			}
		}
	}
	for target, want := range map[string]string{"/": "<admin>", "/assets/admin-DDDDDDDD.js": "admin js"} {
		if _, body := (request{target: target}).serve(t, f.Admin()); body != want {
			t.Errorf("%s on the admin handler: %q, want %q", target, body, want)
		}
	}
	if _, body := navigate("/users").serve(t, f.Admin()); body != "<admin>" {
		t.Errorf("the admin's router: %q", body)
	}
}

// TestSecurityHeaders: the policy the build brought and the other headers are on every answer, the errors too.
func TestSecurityHeaders(t *testing.T) {
	want := map[string]string{
		"Content-Security-Policy": policy,
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
	}
	f := frontend(t)
	// The redirect and the 405 are the routing's own answers, not a page's, and carry the headers as well.
	for _, h := range []http.Handler{f.Public(), f.Admin()} {
		for _, req := range []request{{target: "/"}, {target: "/assets/index-ZZZZZZZZ.js"}, {target: "/api/x"}, {method: http.MethodPost, target: "/"}, {target: "/console"}} {
			resp, _ := req.serve(t, h)
			for name, value := range want {
				if resp.Header.Get(name) != value {
					t.Errorf("%s %s: %s %q, want %q", req.method, req.target, name, resp.Header.Get(name), value)
				}
			}
		}
	}
}

// TestNewRefusesAnIncompleteBuild: a tree without the build, or with a file the server cannot type, fails at start and
// says what to do.
func TestNewRefusesAnIncompleteBuild(t *testing.T) {
	without := func(name string) fstest.MapFS {
		files := build()
		delete(files, name)
		return files
	}
	withStray := build()
	withStray["console/assets/data-EEEEEEEE.bin"] = &fstest.MapFile{Data: []byte("?")}
	cases := []struct {
		name  string
		files fstest.MapFS
		want  string
	}{
		{"nothing embedded", fstest.MapFS{"NOT_BUILT.txt": {Data: []byte("x")}}, "run `just web-embed`"},
		{"no policy", without("content-security-policy.txt"), "run `just web-embed`"},
		{"an app lacks its entry page", without("admin/index.html"), "admin: no index.html"},
		{"a file of a type the server does not know", withStray, `no content type for ".bin"`},
	}
	for _, c := range cases {
		if _, err := New(c.files); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want one saying %q", c.name, err, c.want)
		}
	}
}
