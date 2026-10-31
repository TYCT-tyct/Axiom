package webui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Open reads the frontend embedded in the program and, when overlay is not empty, the content-hashed files other builds
// kept there: the directory a rolling release publishes every build into before it replaces a node (README.md「滚动发布」).
// With no overlay the embedded build is the whole frontend.
func Open(overlay string) (*Frontend, error) {
	f, err := Embedded()
	if err != nil {
		return nil, err
	}
	return f.With(overlay)
}

// With adds the content-hashed files of other builds kept in overlay. A file of this build wins, so the embedded
// build is what its own pages load; the overlay only fills names this build does not have. The directory is read per
// request for a missing name, never at startup, because an older node starts before the next build's files are
// published and must serve them for as long as it stays up. Files that are not content-hashed are never read from it:
// an entry page or a policy kept there by mistake can not answer for this build.
func (f *Frontend) With(overlay string) (*Frontend, error) {
	if overlay == "" {
		return f, nil
	}
	info, err := os.Stat(overlay)
	if err != nil {
		return nil, fmt.Errorf("the shared frontend assets: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("the shared frontend assets: %s is not a directory", overlay)
	}
	return &Frontend{
		site:    f.site.with(overlay),
		console: f.console.with(overlay),
		admin:   f.admin.with(overlay),
		headers: f.headers,
	}, nil
}

// assetCache is the files read from the overlay, once each. Requests miss the embedded build only for another build's
// names, so the cache stays small; misses are not cached, because a name published later must start being served.
type assetCache struct{ sync.Map }

func (a *app) with(root string) *app {
	return &app{
		dir: a.dir, mount: a.mount, files: a.files, overlay: filepath.Join(root, a.dir), other: new(assetCache),
	}
}

// otherBuild returns the file name from the overlay, when it is a content-hashed name that this build lacks. A missing
// file is not an error: the caller then answers 404, as it does for any name no build has. Other read errors answer 500
// instead of pretending the file is gone.
func (a *app) otherBuild(name string) (*file, bool, error) {
	if a.overlay == "" || !fs.ValidPath(name) || !hashed.MatchString(name) {
		return nil, false, nil
	}
	if cached, ok := a.other.Load(name); ok {
		return cached.(*file), true, nil
	}
	target := filepath.Join(a.overlay, filepath.FromSlash(name))
	body, err := os.ReadFile(target) //nolint:gosec // target is a checked, content-hashed path under the configured directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", name, err)
	}
	contents := map[string][]byte{name: body}
	for _, enc := range encodings {
		variant, err := os.ReadFile(target + enc.suffix) //nolint:gosec // the same checked target with a fixed suffix
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("%s%s: %w", name, enc.suffix, err)
		}
		contents[name+enc.suffix] = variant
	}
	f, err := newFile(name, body, contents)
	if err != nil {
		return nil, false, err
	}
	actual, _ := a.other.LoadOrStore(name, f)
	return actual.(*file), true, nil
}
