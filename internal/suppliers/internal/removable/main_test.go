package main

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const sampleRegistry = `package registry

import (
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/newapi"
	"axiom/internal/suppliers/internal/sub2api"
)

var adapters = []adapter.Adapter{
	newapi.New(),
	sub2api.New(),
}
`

func TestAdaptersAreReadFromTheList(t *testing.T) {
	t.Parallel()
	if diff := cmp.Diff([]string{"newapi", "sub2api"}, adapterNames(sampleRegistry)); diff != "" {
		t.Errorf("names (-want +got):\n%s", diff)
	}
	if names := adapterNames("var adapters = []adapter.Adapter{}\n"); len(names) != 0 {
		t.Errorf("an empty list names %v", names)
	}
}

func TestRemovingAnAdapterEditsOnlyItsLines(t *testing.T) {
	t.Parallel()
	got, err := withoutAdapter(sampleRegistry, "newapi")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.NewReplacer("\t\"axiom/internal/suppliers/internal/newapi\"\n", "", "\tnewapi.New(),\n", "").Replace(sampleRegistry)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("registry (-want +got):\n%s", diff)
	}
	if _, err := withoutAdapter(strings.Replace(sampleRegistry, "internal/newapi", "internal/other", 1), "newapi"); err == nil {
		t.Error("a registry that does not import the adapter was edited")
	}
}

// tree writes files, by path, into a new directory shaped like the repository's part that matters: a module named axiom
// with the registry, an adapter package and the interface they share.
func tree(t *testing.T, extra map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module axiom\n\ngo 1.26\n",
		"internal/suppliers/internal/adapter/adapter.go": "package adapter\n\ntype Adapter interface{ Kind() string }\n",
		"internal/suppliers/internal/fake/fake.go":       "package fake\n\ntype A struct{}\n\nfunc New() A { return A{} }\n\nfunc (A) Kind() string { return \"fake\" }\n",
		"internal/suppliers/internal/registry/registry.go": `package registry

import (
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/fake"
)

var adapters = []adapter.Adapter{
	fake.New(),
}

func List() []adapter.Adapter { return adapters }
`,
	}
	maps.Copy(files, extra)
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestAnAdapterThatNothingElseUsesCanBeRemoved and its opposite: the check passes for an adapter only the registry
// names, and fails, with the compiler's message, once other code depends on it.
func TestAnAdapterThatNothingElseUsesCanBeRemoved(t *testing.T) {
	t.Parallel()
	if err := checkAll(tree(t, nil)); err != nil {
		t.Errorf("a removable adapter: %v", err)
	}
	coupled := tree(t, map[string]string{
		"internal/suppliers/internal/sync/sync.go": "package sync\n\nimport \"axiom/internal/suppliers/internal/fake\"\n\nvar Default = fake.New()\n",
	})
	err := checkAll(coupled)
	if err == nil || !strings.Contains(err.Error(), "removing adapter fake") || !strings.Contains(err.Error(), "internal/suppliers/internal/fake") {
		t.Errorf("an adapter that other code uses: %v, want the build to fail naming the package", err)
	}
	// The checkout itself is untouched: the adapter's files are still there.
	if _, statErr := os.Stat(filepath.Join(coupled, "internal/suppliers/internal/fake/fake.go")); statErr != nil {
		t.Errorf("the check changed the checkout: %v", statErr)
	}
}
