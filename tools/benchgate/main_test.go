package main

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// TestBenchmarksRunPackagesInParallel runs the go command: one `go test` runs the benchmark packages of an invocation
// one after another whatever -p says, so benchmarks() gives each package its own invocation and runs four at once. Each
// package writes a marker of its own and waits for the other package's, so the other marker appears only while both run
// at once; packages run one after another fail after the deadline.
func TestBenchmarksRunPackagesInParallel(t *testing.T) {
	dir := probeFiles(t, map[string]string{
		"a/a_test.go": overlapBenchmark("a", "b"),
		"b/b_test.go": overlapBenchmark("b", "a"),
	})
	if _, err := benchmarks(t.Context(), dir, "the probe module"); err != nil {
		t.Fatalf("the packages did not run at the same time: %v", err)
	}
}

// TestBenchmarksReportAFailedPackage runs the go command: a benchmark that fails in one package must fail the gate even
// while other packages run, and the error must name the package.
func TestBenchmarksReportAFailedPackage(t *testing.T) {
	dir := probeFiles(t, map[string]string{
		"ok/ok_test.go": `package ok

import "testing"

func BenchmarkOK(b *testing.B) {
	for b.Loop() {
	}
}
`,
		"bad/bad_test.go": `package bad

import "testing"

func BenchmarkBad(b *testing.B) {
	b.Fatal("the benchmark broke")
}
`,
	})
	if _, err := benchmarks(t.Context(), dir, "the probe module"); err == nil || !strings.Contains(err.Error(), "probe/bad") {
		t.Fatalf("a failing package did not fail the gate: %v", err)
	}
}

// TestBenchmarksWithoutAny runs the go command: a tree with no benchmark at all is an error, never a silent pass.
func TestBenchmarksWithoutAny(t *testing.T) {
	dir := probeFiles(t, map[string]string{
		"plain/plain_test.go": `package plain

import "testing"

func TestNothing(t *testing.T) {
}
`,
	})
	if _, err := benchmarks(t.Context(), dir, "the probe module"); err == nil || !strings.Contains(err.Error(), "no package has a benchmark") {
		t.Fatalf("a tree without benchmarks was accepted: %v", err)
	}
}

// TestBenchmarksSkipPackagesWithoutBenchmarks runs the go command: a package with no benchmark is only listed, so its
// tests are never run and only the package with a benchmark contributes results.
func TestBenchmarksSkipPackagesWithoutBenchmarks(t *testing.T) {
	dir := probeFiles(t, map[string]string{
		"bench/bench_test.go": `package bench

import "testing"

func BenchmarkOK(b *testing.B) {
	for b.Loop() {
	}
}
`,
		"plain/plain_test.go": `package plain

import "testing"

func TestFails(t *testing.T) {
	t.Fatal("a package without benchmarks must not be run")
}
`,
	})
	run, err := benchmarks(t.Context(), dir, "the probe module")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.allocs) != 1 {
		t.Fatalf("results of %d packages, want only the one with a benchmark: %v", len(run.allocs), run.allocs)
	}
	for name := range run.allocs {
		if !strings.HasPrefix(name, "probe/bench.OK") {
			t.Errorf("unexpected benchmark %s", name)
		}
	}
}

// TestBenchmarksReportSkipsFromEveryPackage runs the go command: a skip in either of two packages running at once still
// reaches the gate, including one after the first run, which leaves a result line with no iterations.
func TestBenchmarksReportSkipsFromEveryPackage(t *testing.T) {
	dir := probeFiles(t, map[string]string{
		"a/a_test.go": `package a

import "testing"

func BenchmarkSkipped(b *testing.B) {
	b.Skip("needs a database")
}
`,
		"b/b_test.go": `package b

import "testing"

func BenchmarkLate(b *testing.B) {
	if b.N > 1 {
		b.Skip("cannot run the calibrated workload")
	}
}
`,
	})
	run, err := benchmarks(t.Context(), dir, "the probe module")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"BenchmarkSkipped", "probe/b.Late (no iterations)"}
	if diff := cmp.Diff(want, slices.Compact(slices.Sorted(slices.Values(run.skipped)))); diff != "" {
		t.Errorf("skipped (-want +got):\n%s", diff)
	}
}

// TestPrefixWriter checks that a package's stderr lines carry its name, including a last line without a newline.
func TestPrefixWriter(t *testing.T) {
	var out bytes.Buffer
	w := &prefixWriter{lock: new(sync.Mutex), out: &out, prefix: "probe/a"}
	if _, err := w.Write([]byte("first\nsecond")); err != nil {
		t.Fatal(err)
	}
	w.flush()
	if diff := cmp.Diff("probe/a: first\nprobe/a: second\n", out.String()); diff != "" {
		t.Errorf("stderr (-want +got):\n%s", diff)
	}
}

// overlapBenchmark is a benchmark that writes its package's marker and waits for the other package's. A benchmark runs
// in its package's directory, so the module root is one up. Both markers exist from the second run on, which ends the
// wait at once; a package whose partner never runs fails after the deadline.
func overlapBenchmark(mine, theirs string) string {
	return fmt.Sprintf(`package %[1]s

import (
	"os"
	"testing"
	"time"
)

func BenchmarkOverlap(b *testing.B) {
	if err := os.WriteFile("../marker-%[1]s", nil, 0o600); err != nil {
		b.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat("../marker-%[2]s"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			b.Fatal("the other package did not run at the same time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for b.Loop() {
	}
}
`, mine, theirs)
}

// probeModule writes a throwaway module with the given test file at its root and returns its directory.
func probeModule(t *testing.T, test string) string {
	t.Helper()
	return probeFiles(t, map[string]string{"b_test.go": test})
}

// probeFiles writes a throwaway module with the given files (paths relative to its root) and returns its directory.
func probeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files = maps.Clone(files)
	files["go.mod"] = "module probe\n\ngo " + probeGoVersion() + "\n"
	for name, src := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// goVersion is the language version in a toolchain string: "1.27" in both "go1.27.2" and "devel go1.28-...".
var goVersion = regexp.MustCompile(`1\.\d+`)

// probeGoVersion is the go directive for a probe module: the running toolchain's own language version, so a probe never
// asks the go command to download another toolchain, whatever Go version the runner has.
func probeGoVersion() string {
	if version := goVersion.FindString(runtime.Version()); version != "" {
		return version
	}
	return "1.24" // older than any toolchain running these tests; the probes' language features need 1.22 or later
}

// TestBenchmarksRunFixedIterations runs the go command: the benchmark command asks for a fixed count of iterations, and
// every run of every benchmark reports that count, -count times over, so what a run pays once is spread over the same
// count on both sides. With a duration instead the count follows the machine's speed (see the comment on benchtime).
func TestBenchmarksRunFixedIterations(t *testing.T) {
	count, fixed := strings.CutSuffix(benchtime, "x")
	iterations, err := strconv.Atoi(count)
	if !fixed || err != nil || iterations < 2 {
		t.Fatalf("-benchtime %s is not a fixed count of at least two iterations", benchtime)
	}
	dir := probeModule(t, "package probe\n\nimport \"testing\"\n\nvar sink []byte\n\n"+
		"func BenchmarkIters(b *testing.B) {\n\tfor i := 0; i < b.N; i++ {\n\t\tsink = make([]byte, 8)\n\t}\n"+
		"\tb.ReportMetric(float64(b.N), \"iters\")\n}\n")
	run, err := benchmarks(t.Context(), dir, "the probe module")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(slices.Repeat([]float64{float64(iterations)}, 6), metric(run, "iters")); diff != "" {
		t.Errorf("iteration counts of the 6 runs (-want +got):\n%s", diff)
	}
}

// TestBenchmarksQuietTheRuntime runs the go command: a benchmark reports the collector's target percentage and the
// processors it runs on, -1 and 1 for no collection inside a run and no move to another processor's pools between runs,
// and every run must see both, or one side's counts come out higher than the other's for the same code (see the comment
// in benchmarks).
func TestBenchmarksQuietTheRuntime(t *testing.T) {
	dir := probeModule(t, "package probe\n\nimport (\n\t\"runtime\"\n\t\"runtime/debug\"\n\t\"testing\"\n)\n\n"+
		"func BenchmarkRuntime(b *testing.B) {\n\tfor b.Loop() {\n\t}\n"+
		"\twas := debug.SetGCPercent(100)\n\tdebug.SetGCPercent(was)\n"+
		"\tb.ReportMetric(float64(was), \"gcpercent\")\n"+
		"\tb.ReportMetric(float64(runtime.GOMAXPROCS(0)), \"processors\")\n}\n")
	run, err := benchmarks(t.Context(), dir, "the probe module")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		unit string
		want float64
	}{{"gcpercent", -1}, {"processors", 1}} {
		if diff := cmp.Diff(slices.Repeat([]float64{m.want}, 6), metric(run, m.unit)); diff != "" {
			t.Errorf("%s of the 6 runs (-want +got):\n%s", m.unit, diff)
		}
	}
}

// metric returns the numbers reported under unit, one per run of the benchmark.
func metric(run measurements, unit string) []float64 {
	var values []float64
	fields := strings.Fields(string(run.text))
	for i := 1; i < len(fields); i++ {
		if fields[i] != unit {
			continue
		}
		if n, err := strconv.ParseFloat(fields[i-1], 64); err == nil {
			values = append(values, n)
		}
	}
	return values
}

// TestBenchmarksSeeRealSkips runs the go command: a b.Skip() at the start is reported only as an event, and one after
// the calibrating first run only as a result line with zero iterations.
func TestBenchmarksSeeRealSkips(t *testing.T) {
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
	if diff := cmp.Diff([]string{"BenchmarkSkipped", "probe.Late (no iterations)"}, skipped); diff != "" {
		t.Errorf("skipped (-want +got):\n%s", diff)
	}
	if samples := run.allocs["probe.Kept"]; len(samples) != 6 {
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
			allocs(map[string][]float64{"a": {500, 500, 500, 501, 501, 501}}),
			allocs(map[string][]float64{"a": {500, 501, 501, 501, 501, 502}}),
			nil, nil,
		},
		{
			"a thousandth more passes",
			allocs(map[string][]float64{"a": {6032, 6032}}),
			allocs(map[string][]float64{"a": {6033, 6033}}),
			nil, nil,
		},
		{
			"more than a thousandth fails",
			allocs(map[string][]float64{"a": {6032}}),
			allocs(map[string][]float64{"a": {6040}}),
			[]string{"more allocations: a: 6032 -> 6040 allocs/op"},
			nil,
		},
		{
			"below a thousand the count is exact",
			allocs(map[string][]float64{"a": {500}}),
			allocs(map[string][]float64{"a": {501}}),
			[]string{"more allocations: a: 500 -> 501 allocs/op"},
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
