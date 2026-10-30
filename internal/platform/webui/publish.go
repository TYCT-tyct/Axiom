package webui

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Publish writes this build's content-hashed files under dir, keeping every file already there. A deployment runs it
// from each release before it replaces a node, so every node finds the files of both builds while old and new nodes
// overlap (README.md「滚动发布」). Entry pages and the policy are never written: they belong to the build that serves
// them, and a shared page would outlive it.
func (f *Frontend) Publish(dir string) error {
	for _, a := range []*app{f.site, f.console, f.admin} {
		if err := a.publish(dir); err != nil {
			return err
		}
	}
	return nil
}

// publish writes one app's content-hashed files under root, which is the shared directory plus the app's own name.
func (a *app) publish(root string) error {
	for name, file := range a.files {
		if !hashed.MatchString(name) {
			continue
		}
		if err := file.publish(filepath.Join(root, a.dir), name); err != nil {
			return err
		}
	}
	return nil
}

// publish writes one file and every encoding the build made of it, base first: a node reading the directory while this
// runs never finds a variant without the file it belongs to.
func (f *file) publish(dir, name string) error {
	base := filepath.Join(dir, filepath.FromSlash(name))
	if err := writeAdditive(base, f.bodies[""]); err != nil {
		return err
	}
	for _, enc := range encodings {
		body, ok := f.bodies[enc.name]
		if !ok {
			continue
		}
		if err := writeAdditive(base+enc.suffix, body); err != nil {
			return err
		}
	}
	return nil
}

// writeAdditive writes body to target unless the file is already there with the same bytes. A content-hashed name
// carries its content, so two different bodies under one name mean the directory is not what it claims: stop instead of
// serving one of them. The file is written aside and renamed, so a node reading it never sees a half-written file.
func writeAdditive(target string, body []byte) error {
	existing, err := os.ReadFile(target) //nolint:gosec // target is a content-hashed path under the operator's shared directory
	switch {
	case err == nil:
		if !bytes.Equal(existing, body) {
			return fmt.Errorf("%s already holds different bytes: a content-hashed name must never change", target)
		}
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	// The node runs as 65532 and reads what root publishes, so the directories are world-readable (never writable).
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // see above
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".publish-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // the successful rename has already taken it
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close() //nolint:errcheck // the write's error is the one worth reporting
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close() //nolint:errcheck // the chmod's error is the one worth reporting
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Link creates the target only if it does not exist: two publishers that both found it missing cannot both
	// succeed, and the second compares what the first left instead of overwriting it. Rename would replace it.
	err = os.Link(tmp.Name(), target)
	if errors.Is(err, fs.ErrExist) {
		existing, readErr := os.ReadFile(target) //nolint:gosec // target is a content-hashed path under the operator's shared directory
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(existing, body) {
			return fmt.Errorf("%s already holds different bytes: a content-hashed name must never change", target)
		}
		return nil
	}
	return err
}
