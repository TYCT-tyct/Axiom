package webui

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// plant writes files under dir, creating their directories.
func plant(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func withOverlay(t *testing.T, dir string) *Frontend {
	t.Helper()
	f, err := frontend(t).With(dir)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestOverlayServesTheOtherBuildsAssets: a node serves the content-hashed files of the build it replaced, with the
// caching and encoding of its own, and picks up a file published after it started: an older node outlives the
// publication of the next build's files (TestPublishKeepsBothBuilds covers the other direction).
func TestOverlayServesTheOtherBuildsAssets(t *testing.T) {
	dir := t.TempDir()
	plant(t, dir, map[string]string{
		"site/assets/index-EEEEEEEE.js":    "old site js",
		"site/assets/index-EEEEEEEE.js.br": "old site js, brotli",
		"site/assets/index-EEEEEEEE.js.gz": "old site js, gzip",
		"console/assets/page-FFFFFFFF.js":  "old console js",
		"admin/assets/admin-GGGGGGGG.js":   "old admin js",
	})
	f := withOverlay(t, dir)
	for _, c := range []struct {
		name, target, body string
		h                  http.Handler
	}{
		{"the website's old script", "/assets/index-EEEEEEEE.js", "old site js", f.Public()},
		{"the console's old script", "/console/assets/page-FFFFFFFF.js", "old console js", f.Public()},
		{"the admin's old script, on its listener only", "/assets/admin-GGGGGGGG.js", "old admin js", f.Admin()},
	} {
		resp, body := (request{target: c.target}).serve(t, c.h)
		if resp.StatusCode != http.StatusOK || body != c.body ||
			resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Errorf("%s: %d %q (%q)", c.name, resp.StatusCode, body, resp.Header.Get("Cache-Control"))
		}
	}
	if _, body := (request{target: "/assets/admin-GGGGGGGG.js"}).serve(t, f.Public()); body == "old admin js" {
		t.Error("the public handler served the admin's old script")
	}
	resp, body := (request{target: "/assets/index-EEEEEEEE.js", header: http.Header{"Accept-Encoding": {"br"}}}).serve(t, f.Public())
	if body != "old site js, brotli" || resp.Header.Get("Content-Encoding") != "br" {
		t.Errorf("an overlay variant: %q %q", resp.Header.Get("Content-Encoding"), body)
	}

	// Published after this frontend was built: a node that started earlier must still serve it.
	plant(t, dir, map[string]string{"site/assets/later-HHHHHHHH.js": "later js"})
	if resp, body := (request{target: "/assets/later-HHHHHHHH.js"}).serve(t, f.Public()); resp.StatusCode != http.StatusOK || body != "later js" {
		t.Errorf("a file published after start: %d %q", resp.StatusCode, body)
	}

	// A name this build has is this build's, whatever the overlay holds under it.
	plant(t, dir, map[string]string{"site/assets/index-AAAAAAAA.js": "not served"})
	if _, body := (request{target: "/assets/index-AAAAAAAA.js"}).serve(t, f.Public()); body != "site js" {
		t.Errorf("the overlay replaced this build's file: %q", body)
	}
}

// TestOverlayReadsOnlyContentHashedNames: a page or an unhashed file kept in the shared directory can not answer for
// this build; the entry page is always the embedded one.
func TestOverlayReadsOnlyContentHashedNames(t *testing.T) {
	dir := t.TempDir()
	plant(t, dir, map[string]string{
		"site/index.html":          "<old site>",
		"site/assets/old-loose.js": "old loose",
	})
	f := withOverlay(t, dir)
	if _, body := (request{target: "/"}).serve(t, f.Public()); body != "<site>" {
		t.Errorf("the entry page came from the shared directory: %q", body)
	}
	if _, body := (request{target: "/index.html"}).serve(t, f.Public()); body != "<site>" {
		t.Errorf("the named entry page came from the shared directory: %q", body)
	}
	if resp, body := (request{target: "/assets/old-loose.js"}).serve(t, f.Public()); resp.StatusCode != http.StatusNotFound || body == "old loose" {
		t.Errorf("an unhashed overlay file: %d %q", resp.StatusCode, body)
	}
}

// TestWithRefusesAnUnusableDirectory: a configured shared directory that is not one fails at start, like every other
// settings problem, instead of making every old asset a 404 later.
func TestWithRefusesAnUnusableDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"missing": filepath.Join(t.TempDir(), "gone"), "a file": file} {
		if _, err := frontend(t).With(dir); err == nil || !strings.Contains(err.Error(), "shared frontend assets") {
			t.Errorf("%s: error %v, want one naming the shared frontend assets", name, err)
		}
	}
	if f, err := frontend(t).With(""); err != nil || f == nil {
		t.Errorf("no overlay: %v", err)
	}
}

// published reads a file the publish step wrote.
func published(t *testing.T, dir, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))) //nolint:gosec // the test's own temp directory
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestPublishKeepsBothBuilds: publishing a build keeps every content-hashed file already in the directory and writes
// only those.
func TestPublishKeepsBothBuilds(t *testing.T) {
	dir := t.TempDir()
	old := frontend(t)
	if err := old.Publish(dir); err != nil {
		t.Fatal(err)
	}
	if err := old.Publish(dir); err != nil {
		t.Fatalf("publishing the same build twice: %v", err)
	}
	next := build()
	next["site/assets/index-IIIIIIII.js"] = &fstest.MapFile{Data: []byte("new site js")}
	newFrontend, err := New(next)
	if err != nil {
		t.Fatal(err)
	}
	if err := newFrontend.Publish(dir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"site/assets/index-AAAAAAAA.js":    "site js",
		"site/assets/index-IIIIIIII.js":    "new site js",
		"console/assets/page-CCCCCCCC.js":  "console js",
		"admin/assets/admin-DDDDDDDD.js":   "admin js",
		"site/assets/index-AAAAAAAA.js.br": "site js, brotli",
	} {
		if got := published(t, dir, name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	for _, absent := range []string{"site/index.html", "content-security-policy.txt", "site/assets/loose.js"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(absent))); !os.IsNotExist(err) {
			t.Errorf("%s was published: %v", absent, err)
		}
	}
}

// TestPublishedFilesServeEitherBuild: a node started before the new build was published serves the new build's chunks,
// because the overlay is read per request, and the new node serves the old build's.
func TestPublishedFilesServeEitherBuild(t *testing.T) {
	dir := t.TempDir()
	old := frontend(t)
	if err := old.Publish(dir); err != nil {
		t.Fatal(err)
	}
	oldWithOverlay := withOverlay(t, dir)
	next := build()
	next["site/assets/index-IIIIIIII.js"] = &fstest.MapFile{Data: []byte("new site js")}
	newFrontend, err := New(next)
	if err != nil {
		t.Fatal(err)
	}
	if err := newFrontend.Publish(dir); err != nil {
		t.Fatal(err)
	}
	if _, body := (request{target: "/assets/index-AAAAAAAA.js"}).serve(t, withOverlay(t, dir).Public()); body != "site js" {
		t.Errorf("the new frontend lost the old chunk: %q", body)
	}
	if resp, body := (request{target: "/assets/index-IIIIIIII.js"}).serve(t, oldWithOverlay.Public()); resp.StatusCode != http.StatusOK || body != "new site js" {
		t.Errorf("the old frontend did not pick up the new chunk: %d %q", resp.StatusCode, body)
	}
}

// TestPublishRefusesDifferentBytesUnderAName: a content-hashed name carries its content; writing different bytes under
// one means the directory is not what it claims, so the publish stops instead of choosing one of them.
func TestPublishRefusesDifferentBytesUnderAName(t *testing.T) {
	dir := t.TempDir()
	old := frontend(t)
	if err := old.Publish(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "site", "assets", "index-AAAAAAAA.js"), []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := old.Publish(dir); err == nil || !strings.Contains(err.Error(), "different bytes") {
		t.Errorf("a published name accepted different bytes: %v", err)
	}
}

// TestConcurrentPublishKeepsOneContentUnderAName: two publishers can both find a hashed name missing before either
// writes it. The target is created with a link, so only one can win; the other must fail after comparing what the
// winner left, never overwrite it and still report success.
func TestConcurrentPublishKeepsOneContentUnderAName(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "site", "assets", "index-AAAAAAAA.js")
	bodies := make([][]byte, 8)
	for i := range bodies {
		bodies[i] = bytes.Repeat([]byte{byte('a' + i)}, 1<<18)
	}
	start := make(chan struct{})
	errs := make([]error, len(bodies))
	var wg sync.WaitGroup
	for i := range bodies {
		wg.Go(func() {
			<-start
			errs[i] = writeAdditive(target, bodies[i])
		})
	}
	close(start)
	wg.Wait()
	won := -1
	for i, err := range errs {
		switch {
		case err == nil && won < 0:
			won = i
		case err == nil:
			t.Errorf("publisher %d also succeeded; one hashed name cannot hold two bodies", i)
		case !strings.Contains(err.Error(), "different bytes"):
			t.Errorf("publisher %d: %v; want the different-bytes refusal", i, err)
		}
	}
	if won < 0 {
		t.Fatal("no publisher succeeded")
	}
	got, err := os.ReadFile(target) //nolint:gosec // the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bodies[won]) {
		t.Errorf("the target holds %d bytes, not the winner's", len(got))
	}
}
