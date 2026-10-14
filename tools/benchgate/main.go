// Command benchgate runs the benchmarks at a base commit and in the working tree, and fails if a benchmark allocates
// more per operation, was skipped, or if every measurement disappeared. Allocations per operation are stable from run
// to run; time and bytes are noisy on shared CI machines, so they are only reported through benchstat
// (docs/performance.md「怎么验证」). Benchmark output is parsed with golang.org/x/perf/benchfmt, the format's own
// reader, so custom metrics and sub-benchmark names are read the way benchstat reads them.
//
// usage: go run -C tools ./benchgate <repository root> <base commit>
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"golang.org/x/perf/benchfmt"
)

const allocsUnit = "allocs/op"

var (
	bench   = []string{"test", "-run", "^$", "-bench", ".", "-benchmem", "-count", "6", "-benchtime", "100ms", "./..."}
	skipped = regexp.MustCompile(`(?m)^\s*--- SKIP: (Benchmark\S*)`)
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: benchgate <repository root> <base commit>")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	problems, err := run(ctx, os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, problem := range problems {
		fmt.Println(problem)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
}

func run(ctx context.Context, root, base string) ([]string, error) {
	scratch, err := os.MkdirTemp("", "benchgate")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch) //nolint:errcheck // a leftover temporary directory is harmless
	baseOut, err := benchmarksAt(ctx, root, base, filepath.Join(scratch, "base"))
	if err != nil {
		return nil, err
	}
	headOut, err := benchmarks(ctx, root, "the working tree")
	if err != nil {
		return nil, err
	}
	if err := report(ctx, scratch, baseOut, headOut); err != nil {
		return nil, err
	}
	baseAllocs, _, err := allocations(baseOut)
	if err != nil {
		return nil, err
	}
	headAllocs, headSkipped, err := allocations(headOut)
	if err != nil {
		return nil, err
	}
	problems, removed := gate(baseAllocs, headAllocs, headSkipped)
	for _, name := range removed {
		fmt.Println("removed or renamed benchmark, not compared:", name)
	}
	return problems, nil
}

// benchmarksAt runs the benchmarks of commit in a temporary worktree, which is removed even when a step fails.
func benchmarksAt(ctx context.Context, root, commit, tree string) ([]byte, error) {
	defer func() {
		// Cleanup must run even after cancellation, and its errors are expected: the worktree is absent when add failed.
		cleanup := context.WithoutCancel(ctx)
		_ = exec.CommandContext(cleanup, "git", "-C", root, "worktree", "remove", "--force", tree).Run() //nolint:errcheck // see above; prune follows
		_ = exec.CommandContext(cleanup, "git", "-C", root, "worktree", "prune").Run()                   //nolint:errcheck // a stale entry is only a line in git worktree list
	}()
	add := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "--detach", tree, commit)
	if out, err := add.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("check out the base commit %s: %w\n%s", commit, err, out)
	}
	return benchmarks(ctx, tree, "base commit "+commit)
}

// benchmarks runs every benchmark in dir and returns their output; the go command's stderr goes straight to ours.
func benchmarks(ctx context.Context, dir, label string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", bench...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("run the benchmarks of %s: %w\n%s", label, err, out)
	}
	return out, nil
}

// report prints benchstat's comparison of every metric, for people to read.
func report(ctx context.Context, scratch string, base, head []byte) error {
	for name, out := range map[string][]byte{"base.txt": base, "head.txt": head} {
		if err := os.WriteFile(filepath.Join(scratch, name), out, 0o600); err != nil {
			return err
		}
	}
	tool, err := exec.CommandContext(ctx, "go", "tool", "-n", "benchstat").Output()
	if err != nil {
		return fmt.Errorf("find benchstat: %w", err)
	}
	cmd := exec.CommandContext(ctx, strings.TrimSpace(string(tool)), "base.txt", "head.txt")
	cmd.Dir, cmd.Stdout, cmd.Stderr = scratch, os.Stdout, os.Stderr // relative names label the columns
	return cmd.Run()
}

// allocations returns every allocs/op sample per benchmark, keyed by package and full name (sub-benchmarks and the
// GOMAXPROCS suffix included), and the benchmarks that were skipped.
func allocations(out []byte) (map[string][]float64, []string, error) {
	samples := map[string][]float64{}
	reader := benchfmt.NewReader(bytes.NewReader(out), "benchmarks")
	for reader.Scan() {
		result, ok := reader.Result().(*benchfmt.Result)
		if !ok {
			continue // configuration lines and lines that are not benchmark results
		}
		if allocs, ok := result.Value(allocsUnit); ok {
			key := result.GetConfig("pkg") + "." + string(result.Name)
			samples[key] = append(samples[key], allocs)
		}
	}
	var skips []string
	for _, match := range skipped.FindAllSubmatch(out, -1) {
		skips = append(skips, string(match[1]))
	}
	return samples, skips, reader.Err()
}

// gate returns why the working tree must not merge (more allocations, skipped benchmarks, or no measurements left) and
// the base benchmarks it no longer has, which reviewers judge from the diff.
func gate(base, head map[string][]float64, skipped []string) (problems, removed []string) {
	for name, samples := range head {
		if before, ok := base[name]; ok && median(samples) > median(before) {
			problems = append(problems, fmt.Sprintf("more allocations: %s: %g -> %g %s", name, median(before), median(samples), allocsUnit))
		}
	}
	for _, name := range skipped {
		problems = append(problems, "skipped benchmark: "+name+" (every benchmark must run in CI)")
	}
	if len(base) > 0 && len(head) == 0 {
		problems = append(problems, "no benchmark measurements left, but the base had some")
	}
	for name := range base {
		if _, ok := head[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(problems)
	sort.Strings(removed)
	return problems, removed
}

func median(samples []float64) float64 {
	if len(samples) == 0 {
		panic("median of no samples") // every key is created with its first sample
	}
	sorted := slices.Sorted(slices.Values(samples))
	middle := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[middle-1] + sorted[middle]) / 2
	}
	return sorted[middle]
}
