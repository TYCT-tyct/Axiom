package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"axiom/internal/platform/webui"
)

// TestPublishWebAssets: the command writes the embedded build's content-hashed files into a shared directory, and a
// frontend opened on that directory serves them; the entry pages stay the embedded ones.
func TestPublishWebAssets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := publishWebAssets(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "site", "assets"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("published site assets: %v %v", entries, err)
	}
	script := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".js") {
			script = entry.Name()
			break
		}
	}
	if script == "" {
		t.Fatal("no site script was published")
	}
	frontend, err := webui.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://public/assets/"+script, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	frontend.Public().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("a published asset: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if _, err := os.Stat(filepath.Join(dir, "site", "index.html")); !os.IsNotExist(err) {
		t.Errorf("the entry page was published: %v", err)
	}
}

// TestWebAssetsTakesOneDirectory: the command refuses to guess where to publish.
func TestWebAssetsTakesOneDirectory(t *testing.T) {
	for _, args := range [][]string{nil, {"one", "two"}} {
		if err := webAssets(args); err == nil || !strings.Contains(err.Error(), "usage: axiom web-assets") {
			t.Errorf("%v: %v", args, err)
		}
	}
}
