package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// events is `go test -json` output as the go command writes it: config lines, a verbose name line, a result split
// across two events, a custom metric printed before allocs/op, and a skipped benchmark.
const events = `{"Action":"start","Package":"x"}
{"Action":"output","Package":"x","Output":"pkg: axiom/internal/routing\n"}
{"Action":"run","Package":"x","Test":"BenchmarkPick/case"}
{"Action":"output","Package":"x","Test":"BenchmarkPick/case","Output":"BenchmarkPick/case\n"}
{"Action":"output","Package":"x","Test":"BenchmarkPick/case","Output":"BenchmarkPick/case-16   \t"}
{"Action":"output","Package":"x","Test":"BenchmarkPick/case","Output":"10\t200 ns/op\t1000 allocs/op_total\t64 B/op\t3 allocs/op\n"}
{"Action":"output","Package":"x","Test":"BenchmarkPick/case#01","Output":"BenchmarkPick/case#01-16\t10\t120 ns/op\t0 B/op\t0 allocs/op\n"}
{"Action":"output","Package":"x","Test":"BenchmarkSlow","Output":"--- SKIP: BenchmarkSlow\n"}
{"Action":"skip","Package":"x","Test":"BenchmarkSlow"}
{"Action":"output","Package":"x","Output":"PASS\n"}
{"Action":"pass","Package":"x"}
`

func TestParse(t *testing.T) {
	run, err := parse(strings.NewReader(events))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]float64{
		"axiom/internal/routing.Pick/case-16":    {3}, // the custom metric is not mistaken for allocs/op
		"axiom/internal/routing.Pick/case#01-16": {0}, // same-named sub-benchmarks stay apart
	}
	if diff := cmp.Diff(want, run.allocs); diff != "" {
		t.Errorf("allocations (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"BenchmarkSlow"}, run.skipped); diff != "" {
		t.Errorf("skipped (-want +got):\n%s", diff)
	}
}

// probeModule writes a throwaway module with the given test file and returns its directory.
func probeModule(t *testing.T, test string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"go.mod": "module probe\n\ngo 1.27.2\n", "b_test.go": test}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestBenchmarksSeeRealSkips runs the go command: a b.Skip() at the start is reported only as an event, and one after
// the calibrating first run only as a result line with zero iterations.
func TestBenchmarksSeeRealSkips(t *testing.T) {
	t.Setenv("GOMAXPROCS", "2") // fixes the -2 suffix of the names; one CPU would print none
	dir := probeModule(t, "package probe\n\nimport \"testing\"\n\nvar sink []byte\n\n"+
		"func BenchmarkKept(b *testing.B) {\n\tfor b.Loop() {\n\t\tsink = make([]byte, 8)\n\t}\n}\n\n"+
		"func BenchmarkSkipped(b *testing.B) { b.Skip(\"needs a database\") }\n\n"+
		"func BenchmarkLate(b *testing.B) {\n\tif b.N > 1 {\n\t\tb.Skip(\"cannot run the calibrated workload\")\n\t}\n"+
		"\tfor range b.N {\n\t\tsink = make([]byte, 8)\n\t}\n}\n")
	run, err := benchmarks(t.Context(), dir, "the probe module")
	if err != nil {
		t.Fatal(err)
	}
	skipped := slices.Compact(slices.Sorted(slices.Values(run.skipped)))
	if diff := cmp.Diff([]string{"BenchmarkSkipped", "probe.Late-2 (no iterations)"}, skipped); diff != "" {
		t.Errorf("skipped (-want +got):\n%s", diff)
	}
	if samples := run.allocs["probe.Kept-2"]; len(samples) != 6 {
		t.Errorf("BenchmarkKept: %d samples, want 6 (%v)", len(samples), run.allocs)
	}
}

func TestBenchmarksKeepCompileErrors(t *testing.T) {
	dir := probeModule(t, "package probe\n\nimport \"testing\"\n\nfunc BenchmarkBroken(b *testing.B) { undefinedName() }\n")
	_, err := benchmarks(t.Context(), dir, "the probe module")
	if err == nil || !strings.Contains(err.Error(), "undefinedName") {
		t.Fatalf("the compile error is lost: %v", err)
	}
}

// git runs git in root and returns what it printed, failing the test if git fails.
func git(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...).CombinedOutput() // #nosec G204 -- the test's own arguments
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

// TestRemoveWorktree runs git: the gate's worktree goes away, also when its directory is already gone; a registration
// git refuses to remove is reported with git's failure; and another session's worktree, even a stale one, is untouched.
func TestRemoveWorktree(t *testing.T) {
	type outcome struct{ Failed, CarriesGitFailure, StillListed, DirectoryGone, StaleKept bool }
	cases := []struct {
		name    string
		prepare func(tree string) error
		refused bool
	}{
		{"intact", func(string) error { return nil }, false},
		{"directory already gone", os.RemoveAll, false},
		// Without its .git file, git cannot validate the worktree and refuses to remove it, even with --force twice.
		{"git refuses", func(tree string) error { return os.Remove(filepath.Join(tree, ".git")) }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, scratch := t.TempDir(), t.TempDir()
			tree, stale := filepath.Join(scratch, "tree"), filepath.Join(scratch, "stale")
			git(t, root, "init", "-q")
			git(t, root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "base")
			git(t, root, "worktree", "add", "-q", "--detach", tree)
			git(t, root, "worktree", "add", "-q", "--detach", stale)
			if err := errors.Join(os.RemoveAll(stale), c.prepare(tree)); err != nil {
				t.Fatal(err)
			}

			err := removeWorktree(t.Context(), root, tree)
			list := git(t, root, "worktree", "list", "--porcelain")
			_, statErr := os.Stat(tree)
			var exitErr *exec.ExitError
			got := outcome{err != nil, errors.As(err, &exitErr), listed(list, tree), os.IsNotExist(statErr), listed(list, stale)}
			want := outcome{c.refused, c.refused, c.refused, true, true}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("after removal (-want +got):\n%s\nerror: %v", diff, err)
			}
		})
	}
}

func TestListed(t *testing.T) {
	list := []byte("worktree /repo\nHEAD abc\nbranch refs/heads/main\n\nworktree /tmp/benchgate1/base\nHEAD def\ndetached\nlocked\n")
	if !listed(list, filepath.FromSlash("/tmp/benchgate1/base")) || listed(list, filepath.FromSlash("/tmp/benchgate2/base")) {
		t.Error("listed matches the wrong worktree")
	}
}

func TestParseRejectsUnreadableBenchmarkLines(t *testing.T) {
	broken := `{"Action":"output","Output":"BenchmarkScan-8\t100\t10 ns/op\t9 allocs/op\ttrailing\n"}` + "\n"
	if _, err := parse(strings.NewReader(broken)); err == nil {
		t.Fatal("a benchmark line benchfmt cannot read was accepted")
	}
}

func TestGate(t *testing.T) {
	allocs := func(m map[string][]float64) measurements { return measurements{allocs: m} }
	cases := []struct {
		name              string
		base, head        measurements
		problems, removed []string
	}{
		{"equal", allocs(map[string][]float64{"a": {3, 3}}), allocs(map[string][]float64{"a": {3, 3}}), nil, nil},
		{"fewer", allocs(map[string][]float64{"a": {3}}), allocs(map[string][]float64{"a": {2}}), nil, nil},
		{
			"noise that raises the median but not the minimum passes",
			allocs(map[string][]float64{"a": {6033, 6033, 6033, 6034, 6034, 6034}}),
			allocs(map[string][]float64{"a": {6033, 6034, 6034, 6034, 6034, 6035}}),
			nil, nil,
		},
		{
			"one more allocation in every run fails",
			allocs(map[string][]float64{"a": {6033, 6033, 6034}}),
			allocs(map[string][]float64{"a": {6034, 6034, 6035}}),
			[]string{"more allocations: a: 6033 -> 6034 allocs/op"},
			nil,
		},
		{
			"half an allocation more in every run fails",
			allocs(map[string][]float64{"a": {6826}}),
			allocs(map[string][]float64{"a": {6826.5}}),
			[]string{"more allocations: a: 6826 -> 6826.5 allocs/op"},
			nil,
		},
		{"new benchmarks have nothing to compare", allocs(nil), allocs(map[string][]float64{"b": {9}}), nil, nil},
		{
			"removed benchmarks are listed for review",
			allocs(map[string][]float64{"a": {1}, "b": {1}}),
			allocs(map[string][]float64{"a": {1}}),
			nil,
			[]string{"b"},
		},
		{
			"a skipped benchmark fails",
			allocs(map[string][]float64{"a": {1}}),
			measurements{allocs: map[string][]float64{"a": {1}}, skipped: []string{"BenchmarkSlow"}},
			[]string{"skipped benchmark: BenchmarkSlow (every benchmark must run in CI)"},
			nil,
		},
		{
			"nothing left to compare fails",
			allocs(map[string][]float64{"a": {1}, "b": {1}}),
			allocs(map[string][]float64{"c": {1}}),
			[]string{"no benchmark of the base commit can be compared any more"},
			[]string{"a", "b"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems, removed := gate(c.base, c.head)
			if diff := cmp.Diff(c.problems, problems); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(c.removed, removed); diff != "" {
				t.Errorf("removed (-want +got):\n%s", diff)
			}
		})
	}
}
