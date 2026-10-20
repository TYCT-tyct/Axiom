// Command benchgate runs, at a base commit and in the working tree, the benchmarks whose allocation counts that
// difference can move, and fails if a benchmark allocates more per operation, was skipped, or if no measurement can be
// compared any more. Every run of every benchmark runs the same fixed number of iterations on both sides, with the
// garbage collector off and on one processor, so what a run pays once — refilling the pools the collection before it
// emptied — divides by the same count in both and cancels; allocations per operation are then stable from run to run, up
// to noise that only adds, so the smallest of the runs is compared. Time and bytes are noisy on shared CI machines, so
// they are only reported through benchstat (docs/performance.md「怎么验证」).
//
// A benchmark's allocation count depends only on what is compiled into its test binary, so the gate reads the test
// import closure of every benchmark package (go list -deps -test) and measures only the packages whose closure a changed
// file lies in or below — testdata and the files a package embeds live there too. A change to a module file (go.mod,
// go.sum, go.work) decides how every package resolves and compiles and so cannot be attributed: it runs every package.
// The base commit and the working tree are measured at the same time, sharing the bound on how many test binaries run at
// once.
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
	"maps"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// packages is how many benchmark packages run at once, base commit and working tree together. One `go test` cannot do
// it: cmd/go runs the benchmark packages of an invocation one after another whatever -p says ("Force benchmarks to run
// in serial" in cmd/go/internal/test), so the gate gives every package its own invocation and keeps `packages` of them
// in flight. The test binaries still get one processor each and the collector off, so their allocation counts do not
// depend on how many run at once. Four at once, the same bound as `just test`: the host is shared with other jobs, and
// with the collector off the heaviest package (protocol) peaks near 530 MiB. The two sides share the bound, so measuring
// them at the same time leaves the peak where it was.
const packages = 4

var (
	// list asks the testing package which benchmarks each package has; -list runs none of them, and -p builds the test
	// binaries four at a time.
	list = []string{"test", "-json", "-run", "^$", "-list", "^Benchmark", "-p", strconv.Itoa(packages), "./..."}
	// bench runs every benchmark of the one package benchmark() runs.
	bench = []string{"test", "-json", "-run", "^$", "-bench", ".", "-benchmem", "-count", "6", "-benchtime", benchtime, "-p", strconv.Itoa(packages)}
)

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
	// The go command reports absolute directories and the test closures are relative to the tree, so the repository
	// root is made absolute here (`just bench-compare` passes `..`).
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "benchgate")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(scratch)) }()
	tree := filepath.Join(scratch, "base")
	// The worktree is removed even when a step fails, from the checkout on.
	defer func() { err = errors.Join(err, removeWorktree(ctx, root, tree)) }()
	if err := addWorktree(ctx, root, base, tree); err != nil {
		return nil, err
	}
	change, err := changedFiles(ctx, root, base)
	if err != nil {
		// The gate may compare more than the change needs, never less: with nothing attributed, every package runs.
		fmt.Fprintln(os.Stderr, "benchgate:", err)
		change = diff{all: true}
	}
	baseTargets, headTargets, skipped, err := selectPackages(ctx, tree, root, base, change)
	if err != nil {
		return nil, err
	}
	for _, pkg := range skipped {
		fmt.Println("skipped benchmark package", pkg+":", "no changed file is in its test closure")
	}
	var (
		slots  = make(chan struct{}, packages) // shared, so both sides together keep only `packages` test binaries running
		stderr = new(sync.Mutex)               // one package's stderr line at a time, from either side
		runs   [2]measurements
		errs   [2]error
		wg     sync.WaitGroup
	)
	// One side failing stops the other, which would otherwise spend minutes on a result nobody reads.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	measure := func(i int, dir, label string, targets []string) {
		runs[i], errs[i] = benchmarks(ctx, dir, label, targets, slots, stderr)
		if errs[i] != nil {
			cancel()
		}
	}
	wg.Go(func() { measure(0, tree, "base commit "+base, baseTargets) })
	wg.Go(func() { measure(1, root, "the working tree", headTargets) })
	wg.Wait()
	if err := errors.Join(errs[0], errs[1]); err != nil {
		return nil, err
	}
	if err := report(ctx, scratch, runs[0].text, runs[1].text); err != nil {
		return nil, err
	}
	problems, removed := gate(runs[0], runs[1])
	for _, name := range removed {
		fmt.Println("removed or renamed benchmark, not compared:", name)
	}
	return problems, nil
}

// addWorktree checks out commit in tree, a temporary worktree of the repository at root.
func addWorktree(ctx context.Context, root, commit, tree string) error {
	// The arguments reach git directly, without a shell, and --end-of-options keeps a commit or path that starts with a
	// dash from being read as an option, so neither command nor option injection is possible.
	add := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "--detach", "--end-of-options", tree, commit) // #nosec G204 G702 -- see above
	if out, err := add.CombinedOutput(); err != nil {
		return fmt.Errorf("check out the base commit %s: %w\n%s", commit, err, out)
	}
	return nil
}

// removeWorktree deletes the temporary worktree and its registration, touching no other worktree, also after a
// cancelled run, and fails if git still lists it.
func removeWorktree(ctx context.Context, root, tree string) error {
	ctx = context.WithoutCancel(ctx)
	// Removal fails when add never created the worktree; its error matters only if git still lists the worktree at the
	// end. The second --force also removes a worktree that an interrupted add left locked.
	removed, removeErr := exec.CommandContext(ctx, "git", "-C", root, "worktree", "remove", "--force", "--force", "--end-of-options", tree).CombinedOutput() // #nosec G204 G702 -- as in addWorktree
	if err := os.RemoveAll(tree); err != nil {
		return fmt.Errorf("remove the base worktree: %w", err)
	}
	list, err := exec.CommandContext(ctx, "git", "-C", root, "worktree", "list", "--porcelain").Output() // #nosec G204 G702 -- as in addWorktree
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

// benchmarks runs every benchmark of the packages targets in dir at the fixed iteration count, one `go test` per
// package, up to `packages` at a time across both sides: each run holds a slot of slots from before its `go test`
// starts until it ends. The discovery pass (benchmarkPackages) built the test binaries, so the runs only start them.
// stderr is shared by both sides, so their lines stay whole.
func benchmarks(ctx context.Context, dir, label string, targets []string, slots chan struct{}, stderr *sync.Mutex) (measurements, error) {
	var (
		wg       sync.WaitGroup
		runs     = make([]measurements, len(targets))
		failures = make([]error, len(targets))
	)
	for i, pkg := range targets {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			run, err := benchmark(ctx, dir, pkg, stderr)
			if err != nil {
				failures[i] = fmt.Errorf("package %s: %w", pkg, err)
				return
			}
			runs[i] = run
		})
	}
	wg.Wait()
	if err := errors.Join(failures...); err != nil {
		return measurements{}, fmt.Errorf("run the benchmarks of %s: %w", label, err)
	}
	all := measurements{allocs: map[string][]float64{}}
	for _, run := range runs {
		all.text = append(all.text, run.text...)
		all.diagnostics = append(all.diagnostics, run.diagnostics...)
		all.skipped = append(all.skipped, run.skipped...)
		for name, samples := range run.allocs {
			all.allocs[name] = append(all.allocs[name], samples...)
		}
	}
	return all, nil
}

// benchmarkPackages returns the packages of dir that have a benchmark, in the go command's order.
func benchmarkPackages(ctx context.Context, dir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "go", list...) //nolint:gosec // the arguments are the fixed flags in list; no caller input reaches them
	cmd.Env = append(os.Environ(), "GOGC=off", "GOMAXPROCS=1")
	cmd.Dir, cmd.Stderr = dir, os.Stderr
	out, runErr := cmd.Output()
	found := map[string]bool{}
	var diagnostics, text []byte
	dec := jsontext.NewDecoder(bytes.NewReader(out))
	for {
		var ev event
		if err := json.UnmarshalDecode(dec, &ev); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode go test events: %w", err)
		}
		switch ev.Action {
		case "build-output":
			diagnostics = append(diagnostics, ev.Output...)
		case "output":
			text = append(text, ev.Output...)
			for line := range strings.Lines(ev.Output) {
				if strings.HasPrefix(line, "Benchmark") { // -list prints the name of every benchmark it found
					found[ev.Package] = true
				}
			}
		}
	}
	if runErr != nil {
		return nil, fmt.Errorf("%w\n%s%s", runErr, diagnostics, text)
	}
	targets := slices.Sorted(maps.Keys(found))
	if len(targets) == 0 {
		return nil, errors.New("no package has a benchmark")
	}
	return targets, nil
}

// benchmark runs the benchmarks of one package at the fixed iteration count, with the garbage collector off and on one
// processor; the go command's stderr reaches ours, prefixed with the package, because four packages run at once.
func benchmark(ctx context.Context, dir, pkg string, stderr *sync.Mutex) (measurements, error) {
	cmd := exec.CommandContext(ctx, "go", append(slices.Clone(bench), pkg)...) //nolint:gosec // pkg is an import path the go command listed for this tree; the other arguments are fixed flags
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
	log := &prefixWriter{lock: stderr, out: os.Stderr, prefix: pkg}
	cmd.Dir, cmd.Stderr = dir, log
	out, runErr := cmd.Output()
	flushErr := log.flush()
	run, parseErr := parse(bytes.NewReader(out))
	if err := errors.Join(runErr, flushErr); err != nil {
		return measurements{}, fmt.Errorf("%w\n%s%s", err, run.diagnostics, run.text)
	}
	if parseErr != nil {
		return measurements{}, fmt.Errorf("read the benchmarks: %w", parseErr)
	}
	return run, nil
}

// prefixWriter writes every line it is given to out with prefix, over the lock shared by the packages running at once,
// so their stderr lines stay whole and tell which package printed them.
type prefixWriter struct {
	lock   *sync.Mutex
	out    io.Writer
	prefix string
	buf    []byte // the tail of a line the command has not ended yet
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	w.lock.Lock()
	defer w.lock.Unlock()
	w.buf = append(w.buf, p...)
	for {
		line, rest, found := bytes.Cut(w.buf, []byte("\n"))
		if !found {
			break
		}
		if _, err := fmt.Fprintf(w.out, "%s: %s\n", w.prefix, line); err != nil {
			return 0, fmt.Errorf("write stderr: %w", err)
		}
		w.buf = rest
	}
	return len(p), nil
}

// flush writes a last line the command left without a newline, and reports a write error.
func (w *prefixWriter) flush() error {
	w.lock.Lock()
	defer w.lock.Unlock()
	if len(w.buf) > 0 {
		if _, err := fmt.Fprintf(w.out, "%s: %s\n", w.prefix, w.buf); err != nil {
			return fmt.Errorf("write stderr: %w", err)
		}
		w.buf = nil
	}
	return nil
}

// event is the part of a `go test -json` event the gate needs; Package names the package of the events of the list pass.
type event struct {
	Action  string
	Package string
	Test    string
	Output  string
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
