// Command benchgate runs the benchmarks at a base commit and in the working tree, and fails if a benchmark allocates
// more per operation, was skipped, or if no measurement can be compared any more. Every run of every benchmark runs the
// same fixed number of iterations on both sides, with the garbage collector off and on one processor, so what a run pays
// once — refilling the pools the collection before it emptied — divides by the same count in both and cancels;
// allocations per operation are then stable from run to run, up to noise that only adds, so the smallest of the runs is
// compared. Time and bytes are noisy on shared CI machines, so they are only reported through benchstat
// (docs/performance.md「怎么验证」).
//
// The go command reports in its JSON form, so skipped benchmarks arrive as events rather than as text that only verbose
// mode prints, and the benchmark lines are read with golang.org/x/perf/benchfmt, the reader benchstat itself uses.
//
// usage: go run -C tools ./benchgate <repository root> <base commit>
package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext" //nolint:depguard // tools is its own module, which cannot import axiom/internal
	"encoding/json/v2"       //nolint:depguard // tools is its own module, which cannot import axiom/internal
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/perf/benchfmt"
)

const allocsUnit = "allocs/op"

// benchtime makes every run of every benchmark run the same fixed count of iterations, and the base commit and the
// working tree run it alike. allocs/op is the allocations of a run divided by the iterations it ran, so what a run
// allocates once per run — the testing package collects the garbage of the previous run before each run, and the first
// iteration of the next one then refills the pools it emptied — is spread over the same count on both sides and cancels
// out. With a duration instead (-benchtime 100ms, as benchmarks run by hand) the count follows the machine's speed: the
// slowest benchmarks here (Aggregate and Relay of 64-KiB deltas) manage two or three iterations per run on the loaded CI
// server, so the same code reads one to six allocations per operation higher than a longer run reads it, and the two
// sides are measured minutes apart, under different load: a pull request that touched no benchmarked code failed on
// that difference (#150, #157, #158). Twenty iterations keep the slowest benchmark near half a second per run and the
// whole gate as short as the 100 ms it replaced.
const benchtime = "20x"

var bench = []string{"test", "-json", "-run", "^$", "-bench", ".", "-benchmem", "-count", "6", "-benchtime", benchtime, "./..."}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: benchgate <repository root> <base commit>")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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

// measurements is what one run of every benchmark produced.
type measurements struct {
	text        []byte               // the benchmark lines, for benchstat
	diagnostics []byte               // what the build printed, such as compile errors
	allocs      map[string][]float64 // allocs/op samples by package and full benchmark name
	skipped     []string             // skipped benchmarks, and benchmarks that measured no iterations
}

func run(ctx context.Context, root, base string) (problems []string, err error) {
	scratch, err := os.MkdirTemp("", "benchgate")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(scratch)) }()
	baseRun, err := benchmarksAt(ctx, root, base, filepath.Join(scratch, "base"))
	if err != nil {
		return nil, err
	}
	headRun, err := benchmarks(ctx, root, "the working tree")
	if err != nil {
		return nil, err
	}
	if err := report(ctx, scratch, baseRun.text, headRun.text); err != nil {
		return nil, err
	}
	problems, removed := gate(baseRun, headRun)
	for _, name := range removed {
		fmt.Println("removed or renamed benchmark, not compared:", name)
	}
	return problems, nil
}

// benchmarksAt runs the benchmarks of commit in a temporary worktree, which is removed even when a step fails.
func benchmarksAt(ctx context.Context, root, commit, tree string) (run measurements, err error) {
	defer func() { err = errors.Join(err, removeWorktree(ctx, root, tree)) }()
	// The arguments reach git directly, without a shell, and --end-of-options keeps a commit or path that starts with a
	// dash from being read as an option, so neither command nor option injection is possible.
	add := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "--detach", "--end-of-options", tree, commit) // #nosec G204 G702 -- see above
	if out, err := add.CombinedOutput(); err != nil {
		return measurements{}, fmt.Errorf("check out the base commit %s: %w\n%s", commit, err, out)
	}
	return benchmarks(ctx, tree, "base commit "+commit)
}

// removeWorktree deletes the temporary worktree and its registration, touching no other worktree, also after a
// cancelled run, and fails if git still lists it.
func removeWorktree(ctx context.Context, root, tree string) error {
	ctx = context.WithoutCancel(ctx)
	// Removal fails when add never created the worktree; its error matters only if git still lists the worktree at the
	// end. The second --force also removes a worktree that an interrupted add left locked.
	removed, removeErr := exec.CommandContext(ctx, "git", "-C", root, "worktree", "remove", "--force", "--force", "--end-of-options", tree).CombinedOutput() // #nosec G204 G702 -- as in benchmarksAt
	if err := os.RemoveAll(tree); err != nil {
		return fmt.Errorf("remove the base worktree: %w", err)
	}
	list, err := exec.CommandContext(ctx, "git", "-C", root, "worktree", "list", "--porcelain").Output() // #nosec G204 G702 -- as in benchmarksAt
	if err != nil {
		return fmt.Errorf("list worktrees: %w", err)
	}
	if listed(list, tree) {
		return fmt.Errorf("git still lists the base worktree %s: %w\n%s", tree, removeErr, removed)
	}
	return nil
}

// listed reports whether `git worktree list --porcelain` output names dir.
func listed(list []byte, dir string) bool {
	for line := range strings.Lines(string(list)) {
		path, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "worktree ")
		if ok && sameDir(filepath.Clean(filepath.FromSlash(path)), filepath.Clean(dir)) {
			return true
		}
	}
	return false
}

// sameDir compares two cleaned paths the way the operating system does: Windows ignores case.
func sameDir(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// benchmarks runs every benchmark in dir at the fixed iteration count, with the garbage collector off and on one
// processor; the go command's stderr goes straight to ours.
func benchmarks(ctx context.Context, dir, label string) (measurements, error) {
	cmd := exec.CommandContext(ctx, "go", bench...)
	// A collection empties the pools the code keeps its buffers in, and the run then allocates them again: that cost is
	// per operation, not per run, so more iterations do not spread it. Aggregate with 64-KiB deltas read 582 to 585
	// allocs/op at five to fifty iterations with the collector on, and 575 with it off; Relay's benchmark turns it off
	// for the same reason. One processor keeps a benchmark's goroutine on the same pools from run to run; with more it
	// moves between them, finds the other processor's pools empty after the collection the testing package runs before
	// each run, and allocates them again — Convert with four turns read 430 in two of five runs with eight processors and
	// in none of five with one. What is left is one re-warming per run, which divides by the fixed iteration count
	// (benchtime) and cancels between the base commit and the working tree. The heap grows until a package's run ends:
	// protocol, the heaviest, peaks near 530 MiB at twenty iterations.
	cmd.Env = append(os.Environ(), "GOGC=off", "GOMAXPROCS=1")
	cmd.Dir, cmd.Stderr = dir, os.Stderr
	out, runErr := cmd.Output()
	run, parseErr := parse(bytes.NewReader(out))
	if runErr != nil {
		return measurements{}, fmt.Errorf("run the benchmarks of %s: %w\n%s%s", label, runErr, run.diagnostics, run.text)
	}
	if parseErr != nil {
		return measurements{}, fmt.Errorf("read the benchmarks of %s: %w", label, parseErr)
	}
	return run, nil
}

// event is the part of a `go test -json` event the gate needs.
type event struct {
	Action string
	Test   string
	Output string
}

// parse reads `go test -json` output: the text of every output event, in order, and the benchmarks that were skipped.
func parse(r io.Reader) (measurements, error) {
	var run measurements
	dec := jsontext.NewDecoder(r)
	for {
		var ev event
		if err := json.UnmarshalDecode(dec, &ev); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return run, fmt.Errorf("decode go test events: %w", err)
		}
		switch {
		case ev.Action == "output":
			run.text = append(run.text, ev.Output...)
		case ev.Action == "build-output":
			run.diagnostics = append(run.diagnostics, ev.Output...)
		case ev.Action == "skip" && strings.HasPrefix(ev.Test, "Benchmark"):
			run.skipped = append(run.skipped, ev.Test)
		}
	}
	allocs, unmeasured, err := allocations(run.text)
	run.allocs, run.skipped = allocs, append(run.skipped, unmeasured...)
	return run, err
}

// allocations returns every allocs/op sample by package and full name (sub-benchmarks and the GOMAXPROCS suffix
// included), and the benchmarks that reported no iterations: a benchmark that skips after its first, calibrating run
// still prints a result line, with zero iterations and zero allocations. A benchmark line benchfmt cannot read is an
// error: dropping it could hide a regression.
func allocations(text []byte) (samples map[string][]float64, unmeasured []string, err error) {
	samples = map[string][]float64{}
	reader := benchfmt.NewReader(bytes.NewReader(text), "benchmarks")
	for reader.Scan() {
		switch record := reader.Result().(type) {
		case *benchfmt.SyntaxError:
			return nil, nil, record
		case *benchfmt.Result:
			key := record.GetConfig("pkg") + "." + string(record.Name)
			if record.Iters <= 0 {
				unmeasured = append(unmeasured, key+" (no iterations)")
			} else if allocs, ok := record.Value(allocsUnit); ok {
				samples[key] = append(samples[key], allocs)
			}
		}
	}
	return samples, unmeasured, reader.Err()
}

// report prints benchstat's comparison of every metric, for people to read.
func report(ctx context.Context, scratch string, base, head []byte) error {
	for name, text := range map[string][]byte{"base.txt": base, "head.txt": head} {
		if err := os.WriteFile(filepath.Join(scratch, name), text, 0o600); err != nil {
			return err
		}
	}
	find := exec.CommandContext(ctx, "go", "tool", "-n", "benchstat")
	find.Stderr = os.Stderr
	tool, err := find.Output()
	if err != nil {
		return fmt.Errorf("find benchstat: %w", err)
	}
	cmd := exec.CommandContext(ctx, strings.TrimSpace(string(tool)), "base.txt", "head.txt") // #nosec G204 -- the path the go command just printed, no shell
	cmd.Dir, cmd.Stdout, cmd.Stderr = scratch, os.Stdout, os.Stderr                          // relative names label the columns
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("compare with benchstat: %w", err)
	}
	return nil
}

// gate returns why the working tree must not merge (more allocations, skipped benchmarks, or nothing left to compare)
// and the base benchmarks it no longer has, which reviewers judge from the diff.
func gate(base, head measurements) (problems, removed []string) {
	compared := 0
	for name, samples := range head.allocs {
		before, ok := base.allocs[name]
		if !ok {
			continue
		}
		compared++
		if lowest(samples) > lowest(before)+tolerance(lowest(before)) {
			problems = append(problems, fmt.Sprintf("more allocations: %s: %g -> %g %s", name, lowest(before), lowest(samples), allocsUnit))
		}
	}
	for _, name := range slices.Compact(slices.Sorted(slices.Values(head.skipped))) { // each -count repeats a skip
		problems = append(problems, "skipped benchmark: "+name+" (every benchmark must run in CI)")
	}
	if len(base.allocs) > 0 && compared == 0 {
		problems = append(problems, "no benchmark of the base commit can be compared any more")
	}
	for name := range base.allocs {
		if _, ok := head.allocs[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(problems)
	sort.Strings(removed)
	return problems, removed
}

// tolerance is how far above the base count a benchmark's count may be: a thousandth of it, rounded down, so counts
// below a thousand are exact. A run still pays for the one collection the testing package makes before it, and the total
// divided by the iterations can then land one or two higher on one side than on the other, where the two minima fall on
// either side of a multiple of the iteration count; the minimum of six runs cannot remove what every run pays. A relay
// or conversion that allocates once more per event rises by tens to hundreds, far above a thousandth of a count of
// thousands.
func tolerance(base float64) float64 { return math.Floor(base / 1000) }

// lowest is the allocation count of a benchmark: the runtime only ever adds allocations of its own to a run (after
// a garbage collection, for one), never removes any, so the smallest of the -count samples is the code's own count,
// while a real regression raises every sample and so raises the smallest too.
func lowest(samples []float64) float64 {
	if len(samples) == 0 {
		panic("lowest of no samples") // every key is created with its first sample
	}
	return slices.Min(samples)
}
