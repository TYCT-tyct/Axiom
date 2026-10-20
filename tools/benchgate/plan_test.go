package main

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestAffects is the decision the gate makes for every benchmark package: a changed file in a directory its test
// binary compiles, or below it, can move the allocation counts, and so can a module file, which decides how every
// package compiles. Everything else is skipped.
func TestAffects(t *testing.T) {
	routing := closure{"internal/protocol", "internal/routing"}
	cases := []struct {
		name     string
		change   diff
		closures []closure
		want     bool
	}{
		{"a file of a package the test binary compiles", diff{files: []string{"internal/routing/plan.go"}}, []closure{routing}, true},
		{"a test file", diff{files: []string{"internal/routing/session_test.go"}}, []closure{routing}, true},
		{"a file below a package, such as its testdata", diff{files: []string{"internal/platform/typeid/testdata/many"}}, []closure{{"internal/platform/typeid"}}, true},
		{"a file a package embeds, which lives in its directory", diff{files: []string{"internal/catalog/testdata/dataset.json"}}, []closure{{"internal/catalog"}}, true},
		{"a file of a package the test binary does not compile", diff{files: []string{"internal/gateway/scan.go"}}, []closure{routing}, false},
		{"a directory that only starts like one of the closure", diff{files: []string{"internal/routing2/plan.go"}}, []closure{routing}, false},
		{"a package only the other side compiles", diff{files: []string{"internal/new/scan.go"}}, []closure{{}, {"internal/new"}}, true},
		{"no change", diff{}, []closure{routing}, false},
		{"go.mod", diff{files: []string{"go.mod"}}, []closure{routing}, true},
		{"go.sum", diff{files: []string{"go.sum"}}, nil, true},
		{"go.work", diff{files: []string{"go.work"}}, nil, true},
		{"a module file of another module", diff{files: []string{"tools/go.mod"}}, nil, true},
		{"the changed files could not be read", diff{all: true}, nil, true},
		{"the justfile", diff{files: []string{"justfile"}}, []closure{routing}, true},
		{"the ci workflow", diff{files: []string{".github/workflows/ci.yml"}}, []closure{routing}, true},
		{"the setup action", diff{files: []string{".github/actions/setup/action.yml"}}, []closure{routing}, true},
		{"the gate itself", diff{files: []string{"tools/benchgate/main.go"}}, []closure{routing}, true},
		{"another workflow", diff{files: []string{".github/workflows/review.yml"}}, []closure{routing}, false},
		{"a justfile of a subdirectory", diff{files: []string{"web/justfile"}}, []closure{routing}, false},
		{"another tool", diff{files: []string{"tools/other/main.go"}}, []closure{routing}, false},
		{"a file nobody compiles", diff{files: []string{"docs/performance.md"}}, []closure{routing}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.change.affects(c.closures...); got != c.want {
				t.Errorf("affects(%v) = %v, want %v", c.change.files, got, c.want)
			}
		})
	}
}

// TestPlan keeps the decision per package, whichever side lists it: a package both sides have is measured on both or
// left out of both, and a package only one side has stays there.
func TestPlan(t *testing.T) {
	routing, gateway := closure{"internal/routing"}, closure{"internal/gateway"}
	cases := []struct {
		name                            string
		change                          diff
		base, head                      side
		wantBase, wantHead, wantSkipped []string
	}{
		{
			"a package the change cannot reach is left out of both sides",
			diff{files: []string{"internal/routing/plan.go"}},
			side{[]string{"internal/gateway", "internal/routing"}, map[string]closure{"internal/gateway": gateway, "internal/routing": routing}},
			side{[]string{"internal/gateway", "internal/routing"}, map[string]closure{"internal/gateway": gateway, "internal/routing": routing}},
			[]string{"internal/routing"},
			[]string{"internal/routing"},
			[]string{"internal/gateway"},
		},
		{
			"a package only one side has stays there",
			diff{files: []string{"internal/new/x.go", "internal/routing/plan.go"}},
			side{[]string{"internal/routing"}, map[string]closure{"internal/routing": routing}},
			side{[]string{"internal/new", "internal/routing"}, map[string]closure{"internal/new": {"internal/new"}, "internal/routing": routing}},
			[]string{"internal/routing"},
			[]string{"internal/new", "internal/routing"},
			nil,
		},
		{
			"a module file runs every package",
			diff{files: []string{"go.mod"}},
			side{packages: []string{"internal/gateway"}},
			side{packages: []string{"internal/gateway"}},
			[]string{"internal/gateway"},
			[]string{"internal/gateway"},
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, head, skipped := plan(c.change, c.base, c.head)
			if diff := cmp.Diff(c.wantBase, base); diff != "" {
				t.Errorf("base targets (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(c.wantHead, head); diff != "" {
				t.Errorf("head targets (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(c.wantSkipped, skipped); diff != "" {
				t.Errorf("skipped (-want +got):\n%s", diff)
			}
		})
	}
}
