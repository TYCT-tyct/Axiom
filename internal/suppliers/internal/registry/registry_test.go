package registry_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
)

// stub is an adapter that is only a kind and its rate buckets.
type stub struct {
	adapter.Adapter
	kind    adapter.Kind
	buckets []adapter.BucketSpec
}

func (s stub) Kind() adapter.Kind            { return s.kind }
func (s stub) Buckets() []adapter.BucketSpec { return s.buckets }

var bucket = adapter.BucketSpec{Name: "global", Window: time.Minute, Limit: 10}

// TestRegistryRefusesWhatWouldFailLater: a registry that starts holds only adapters that can be stored, are unique, and
// declare limits that admit requests.
func TestRegistryRefusesWhatWouldFailLater(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		list []adapter.Adapter
		want string
	}{
		{"none", nil, ""},
		{"two kinds", []adapter.Adapter{stub{kind: "newapi"}, stub{kind: "sub2api", buckets: []adapter.BucketSpec{bucket}}}, ""},
		{"a kind with a capital", []adapter.Adapter{stub{kind: "NewApi"}}, `"NewApi"`},
		{"an empty kind", []adapter.Adapter{stub{}}, "adapter kind"},
		{"a kind twice", []adapter.Adapter{stub{kind: "newapi"}, stub{kind: "newapi"}}, `"newapi": registered twice`},
		{"a bucket without a window", []adapter.Adapter{stub{kind: "a", buckets: []adapter.BucketSpec{{Name: "x", Limit: 1}}}}, `bucket "x"`},
		{"a bucket without a limit", []adapter.Adapter{stub{kind: "a", buckets: []adapter.BucketSpec{{Name: "x", Window: time.Second}}}}, `bucket "x"`},
		{"a bucket twice", []adapter.Adapter{stub{kind: "a", buckets: []adapter.BucketSpec{bucket, bucket}}}, `bucket "global": declared twice`},
	}
	for _, c := range cases {
		_, err := registry.New(c.list...)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

func TestRegistryFindsByKind(t *testing.T) {
	t.Parallel()
	r, err := registry.New(stub{kind: "sub2api"}, stub{kind: "newapi"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]adapter.Kind{"newapi", "sub2api"}, r.Kinds()); diff != "" {
		t.Errorf("kinds (-want +got):\n%s", diff)
	}
	if !r.Has("newapi") || r.Has("ghost") {
		t.Error("Has finds a kind that is not registered, or misses one that is")
	}
}

// TestTheProgramsAdaptersAreValid: the list the program carries passes the same checks, so a broken line fails the
// tests and not only the start.
func TestTheProgramsAdaptersAreValid(t *testing.T) {
	t.Setenv("AXIOM_SUPPLIER_ADAPTERS", "")
	if _, err := registry.Default(); err != nil {
		t.Fatal(err)
	}
}

// TestDefaultIncludesDeclarations: the env directory is part of the program's registry, not a separate list the
// service has to remember to consult.
func TestDefaultIncludesDeclarations(t *testing.T) {
	t.Setenv("AXIOM_SUPPLIER_ADAPTERS", filepath.Join("..", "declared", "testdata"))
	r, err := registry.Default()
	if err != nil {
		t.Fatal(err)
	}
	if !r.Has("acme") {
		t.Fatalf("kinds %v do not include the declaration", r.Kinds())
	}
	if _, ok := r.Get("acme"); !ok {
		t.Error("Get does not find the declaration")
	}
}

// TestDefaultRefusesADeclarationThatShadowsABuiltIn: a declaration's kind is stored in suppliers.kind, so two adapters
// of one kind would make an existing supplier open with the wrong protocol.
func TestDefaultRefusesADeclarationThatShadowsABuiltIn(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "declared", "testdata", "acme.json"))
	if err != nil {
		t.Fatal(err)
	}
	shadow := strings.Replace(string(raw), `"kind": "acme"`, `"kind": "newapi"`, 1)
	if shadow == string(raw) {
		t.Fatal("the fixture's kind was not replaced")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shadow.json"), []byte(shadow), 0o600); err != nil { //nolint:gosec // a test temporary directory
		t.Fatal(err)
	}
	t.Setenv("AXIOM_SUPPLIER_ADAPTERS", dir)
	if _, err := registry.Default(); err == nil || !strings.Contains(err.Error(), `"newapi": registered twice`) {
		t.Fatalf("Default: %v, want the duplicate kind", err)
	}
}
