package declared

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// LoadDir reads every *.json declaration in dir and compiles it. Any file that is not a declaration this version reads
// stops the load with its path: a node must not start with a declaration it only partly understands and discover the
// rest at a supplier's first call. An unset directory is no declarations, not an error; a set directory that cannot be
// read is one.
func LoadDir(dir string) ([]adapter.Adapter, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("declarations: read %s: %w", dir, err)
	}
	type loaded struct {
		path string
		a    *Adapter
	}
	var files []loaded
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path) //nolint:gosec // the directory and file names are the deployment's own configuration
		if err != nil {
			return nil, fmt.Errorf("declaration %s: %w", path, err)
		}
		var spec Spec
		if err := jsonx.UnmarshalKnown(raw, &spec); err != nil {
			return nil, fmt.Errorf("declaration %s: %w", path, err)
		}
		a, err := Compile(spec)
		if err != nil {
			return nil, fmt.Errorf("declaration %s: %w", path, err)
		}
		files = append(files, loaded{path: path, a: a})
	}
	seen := make(map[adapter.Kind]string, len(files))
	out := make([]adapter.Adapter, 0, len(files))
	for _, f := range files {
		if first, dup := seen[f.a.kind]; dup {
			return nil, fmt.Errorf("declarations %s and %s both declare kind %q", first, f.path, f.a.kind)
		}
		seen[f.a.kind] = f.path
		out = append(out, f.a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind() < out[j].Kind() })
	return out, nil
}

// LoadEnv reads the directory named by EnvVar. It is the deployment's way to add declarations without a build: every
// node reads the same files before it serves, and a node without one refuses a supplier of that kind at startup
// (suppliers.CheckAdapters).
func LoadEnv() ([]adapter.Adapter, error) { return LoadDir(os.Getenv(EnvVar)) }
