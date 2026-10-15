// Command removable checks that every adapter can be removed: for each adapter in the registry, deleting its package
// and its line in the registry must leave the rest of the program building and passing go vet. It is `just
// adapters-removable`; docs/domains/suppliers.md「代码组织」 is the rule it keeps.
//
// The deletion is made with go's -overlay, which replaces files for one build without touching the checkout: the
// registry is swapped for a copy without the adapter and each of its files is mapped to nothing.
//
// usage: go run ./internal/suppliers/internal/removable <repository root>
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"axiom/internal/platform/jsonx"
)

const (
	adaptersDir  = "internal/suppliers/internal"
	registryFile = adaptersDir + "/registry/registry.go"
	importPrefix = "axiom/" + adaptersDir + "/"
)

// listed matches one adapter's line in the registry's list: the package name and its constructor.
var listed = regexp.MustCompile(`(?m)^\t([a-z][a-z0-9]*)\.New\(\),[ \t]*\r?\n`)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: removable <repository root>")
		os.Exit(2)
	}
	if err := checkAll(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func checkAll(root string) error {
	source, err := os.ReadFile(filepath.Join(root, registryFile)) //nolint:gosec // the registry of this repository
	if err != nil {
		return err
	}
	names := adapterNames(string(source))
	if len(names) == 0 {
		slog.Info("no adapters are registered, so none can be removed yet")
		return nil
	}
	for _, name := range names {
		if err := checkRemoval(root, string(source), name); err != nil {
			return fmt.Errorf("removing adapter %s: %w", name, err)
		}
		slog.Info("adapter can be removed", "adapter", name)
	}
	return nil
}

// adapterNames lists the packages the registry's list constructs adapters from.
func adapterNames(registry string) (names []string) {
	for _, m := range listed.FindAllStringSubmatch(registry, -1) {
		names = append(names, m[1])
	}
	return names
}

// withoutAdapter is the registry with the adapter's line and import removed. The import is required to be there: a
// registry that constructs an adapter it does not import would not have compiled.
func withoutAdapter(registry, name string) (string, error) {
	line := regexp.MustCompile(`(?m)^\t` + regexp.QuoteMeta(name) + `\.New\(\),[ \t]*\r?\n`)
	imported := regexp.MustCompile(`(?m)^\t"` + regexp.QuoteMeta(importPrefix+name) + `"[ \t]*\r?\n`)
	if !imported.MatchString(registry) {
		return "", fmt.Errorf("the registry does not import %q", importPrefix+name)
	}
	return imported.ReplaceAllString(line.ReplaceAllString(registry, ""), ""), nil
}

// overlay is the JSON go's -overlay takes: the registry replaced by edited, and every file of the adapter deleted.
func overlay(registryPath, edited string, deleted []string) ([]byte, error) {
	replace := map[string]string{registryPath: edited}
	for _, f := range deleted {
		replace[f] = ""
	}
	return jsonx.MarshalCanonical(map[string]map[string]string{"Replace": replace})
}

func checkRemoval(root, registry, name string) error {
	edited, err := withoutAdapter(registry, name)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	deleted, err := filesUnder(filepath.Join(root, filepath.FromSlash(adaptersDir), name))
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "removable-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch) //nolint:errcheck // a leftover temporary directory is not worth failing the check
	editedPath := filepath.Join(scratch, "registry.go")
	configPath := filepath.Join(scratch, "overlay.json")
	config, err := overlay(filepath.Join(root, filepath.FromSlash(registryFile)), editedPath, deleted)
	if err != nil {
		return err
	}
	for path, content := range map[string][]byte{editedPath: []byte(edited), configPath: config} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return err
		}
	}
	var failures []error
	for _, tool := range []string{"build", "vet"} {
		cmd := exec.CommandContext(context.Background(), "go", tool, "-overlay", configPath, "./...") //nolint:gosec // go, a fixed subcommand, and a file made above
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			failures = append(failures, fmt.Errorf("go %s: %w: %s", tool, err, strings.TrimSpace(string(out))))
		}
	}
	return errors.Join(failures...)
}

// filesUnder lists every file below dir.
func filesUnder(dir string) (files []string, err error) {
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error { //nolint:gosec // the developer names the repository root
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return err
	})
	return files, err
}
