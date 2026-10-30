// Package webui serves the frontend's three apps from the files embedded in the program (web/README.md): the website
// at / and the console at /console/ for the public listener, the admin for the admin listener alone. It decides the
// caching, compression, security headers and single-page-app fallback of every response (README.md).
package webui

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Layout of the embedded files, as web/scripts/embed.ts writes it.
const (
	policyFile = "content-security-policy.txt"
	entry      = "index.html"
	// ConsoleMount is where the console is served on the public listener; a link the server builds to a console page starts with it.
	ConsoleMount = "/console"
)

// Frontend is the three apps. It is immutable once built, so one value serves any number of requests.
type Frontend struct {
	site, console, admin *app
	headers              map[string]string // the security headers of every answer
}

// New reads the apps from root, which holds site/, console/, admin/ and the content security policy. A missing app or
// policy, or a file whose type it does not know, fails here, at startup, not as a wrong response later.
func New(root fs.FS) (*Frontend, error) {
	policy, err := fs.ReadFile(root, policyFile)
	if err != nil {
		return nil, fmt.Errorf("the frontend is not embedded in this program: run `just web-embed`, then build it again: %w", err)
	}
	headers := map[string]string{
		"Content-Security-Policy": string(bytes.TrimSpace(policy)),
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY", // frame-ancestors says it too; older browsers read only this one
		"Referrer-Policy":         "strict-origin-when-cross-origin",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
	}
	f := &Frontend{headers: headers}
	for _, a := range []struct {
		dir, mount string
		into       **app
	}{{"site", "", &f.site}, {"console", ConsoleMount, &f.console}, {"admin", "", &f.admin}} {
		sub, err := fs.Sub(root, a.dir)
		if err != nil {
			return nil, err
		}
		if *a.into, err = loadApp(sub, a.dir, a.mount); err != nil {
			return nil, fmt.Errorf("%s: %w", a.dir, err)
		}
	}
	return f, nil
}

// Public serves the website at / and the console at /console/, and nothing of the admin: its files are not in this
// handler at all. Every answer carries the security headers, the redirect and the 405 of the routing included.
func (f *Frontend) Public() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(ConsoleMount+"/", f.console)
	mux.HandleFunc(ConsoleMount, func(w http.ResponseWriter, r *http.Request) {
		target := ConsoleMount + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusPermanentRedirect) //nolint:gosec // the path is constant; the query is the caller's own, sent back to the caller
	})
	mux.Handle("/", f.site)
	return f.Secured(mux)
}

// Admin serves the admin app, for the admin listener alone.
func (f *Frontend) Admin() http.Handler { return f.Secured(f.admin) }

// Secured sets the security headers before next answers, so no answer of it, whoever writes it, goes without. A
// listener that routes to the pages among other handlers wraps its outermost router in it: a router answers a path it
// has to clean with a redirect of its own.
func (f *Frontend) Secured(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, value := range f.headers {
			w.Header().Set(name, value)
		}
		next.ServeHTTP(w, r)
	})
}

// app is one app's files, read into memory once: a few MB, against a map lookup per request.
type app struct {
	dir, mount string // dir is the app's name in the build; mount is where it is served, empty for the root
	files      map[string]*file
	overlay    string      // the directory other builds' content-hashed files are kept in; empty when there is none
	other      *assetCache // files read from the overlay, once each
}

// apiPrefixes are the path prefixes of the programs' APIs. What reaches an app under them is a request no API route
// claimed, whatever its method: a 404 for the caller, never the app's entry page.
var apiPrefixes = []string{"/api/", "/v1/"}

func isAPI(urlPath string) bool {
	return slices.ContainsFunc(apiPrefixes, func(prefix string) bool { return strings.HasPrefix(urlPath, prefix) })
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isAPI(r.URL.Path) {
		reject(w, http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		reject(w, http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, a.mount), "/")
	if name == "" {
		name = entry
	}
	f, found := a.files[name]
	if !found {
		var err error
		if f, found, err = a.otherBuild(name); err != nil {
			reject(w, http.StatusInternalServerError)
			return
		}
	}
	if !found && wantsPage(r, name) {
		f, found = a.files[entry]
	}
	if !found {
		reject(w, http.StatusNotFound)
		return
	}
	f.serve(w, r)
}

// wantsPage says whether an unknown path is a page of the app's own router, which only the entry page can answer: a
// navigation (the browser asks for HTML) to a path that names no file. Anything else unknown is missing: a script or
// stylesheet of a build that is gone must fail as the 404 it is, not arrive as HTML.
func wantsPage(r *http.Request, name string) bool {
	return path.Ext(name) == "" && strings.Contains(r.Header.Get("Accept"), "text/html")
}

func reject(w http.ResponseWriter, status int) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", cacheNever)
	w.WriteHeader(status)
	_, _ = fmt.Fprintln(w, http.StatusText(status)) //nolint:errcheck // the client went away; nothing is left to tell it
}

// hashed names what the build writes under assets/ with a hash of the content in its name (Vite's, and
// web/vite.shared.ts's for theme.js): a name that never meets another content, so it may be cached for good.
var hashed = regexp.MustCompile(`^assets/.+-[A-Za-z0-9_-]{8}\.[a-z0-9]+$`)

const (
	cacheForever = "public, max-age=31536000, immutable"
	cacheNever   = "no-store"
)

func loadApp(files fs.FS, dir, mount string) (*app, error) {
	contents := map[string][]byte{}
	err := fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		contents[name], err = fs.ReadFile(files, name)
		return err
	})
	if err != nil {
		return nil, err
	}
	if _, ok := contents[entry]; !ok {
		return nil, fmt.Errorf("no %s: run `just web-embed`", entry)
	}
	a := &app{dir: dir, mount: mount, files: map[string]*file{}}
	for name, body := range contents {
		if isVariant(name, contents) {
			continue
		}
		if a.files[name], err = newFile(name, body, contents); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// newFile is the file called name with the precompressed copies the build wrote beside it.
func newFile(name string, body []byte, contents map[string][]byte) (*file, error) {
	contentType, ok := contentTypes[path.Ext(name)]
	if !ok {
		return nil, fmt.Errorf("%s: no content type for %q; add it to contentTypes", name, path.Ext(name))
	}
	cache := cacheNever
	if hashed.MatchString(name) {
		cache = cacheForever
	}
	f := &file{contentType: contentType, cache: cache, bodies: map[string][]byte{"": body}}
	for _, enc := range encodings {
		if variant, ok := contents[name+enc.suffix]; ok {
			f.bodies[enc.name] = variant
		}
	}
	return f, nil
}

// isVariant says whether name is a precompressed copy of another file, which is served as that file's encoding and
// never under its own name.
func isVariant(name string, contents map[string][]byte) bool {
	for _, enc := range encodings {
		if base, ok := strings.CutSuffix(name, enc.suffix); ok {
			if _, exists := contents[base]; exists {
				return true
			}
		}
	}
	return false
}

// contentTypes are the types of the files a build holds. A file of another type fails the start: it needs a type
// chosen, not a guess by the browser.
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".svg":   "image/svg+xml",
	".woff2": "font/woff2",
}

// file is one file with the encodings it was precompressed to.
type file struct {
	contentType, cache string
	bodies             map[string][]byte // by content coding; "" is the file as it is
}

func (f *file) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", f.contentType)
	h.Set("Cache-Control", f.cache)
	body := f.bodies[""]
	if len(f.bodies) > 1 {
		h.Add("Vary", "Accept-Encoding")
		if enc := negotiate(r.Header.Values("Accept-Encoding"), f.bodies); enc != "" {
			h.Set("Content-Encoding", enc)
			body = f.bodies[enc]
		}
	}
	// Whole, with its length: ServeContent leaves the length out of an encoded body, and no client asks for a range of
	// a script.
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body) //nolint:errcheck,gosec // the client went away, and the body is a build file, never a page built from a request
	}
}
