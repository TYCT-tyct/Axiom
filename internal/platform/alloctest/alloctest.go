// Package alloctest measures what a function allocates, or what its result retains, without the rest of the test binary
// running beside it.
//
// testing.AllocsPerRun and runtime.ReadMemStats count the whole process: a collection another goroutine starts inside a
// measured run, or a goroutine the scheduler runs while the measured one is preempted, adds to the count and to the
// bytes, so a bound tight enough to prove "this call allocates nothing" fails at random on a loaded machine. Measure
// runs the function in a child process of the test binary: the child selects only the calling test, settles the heap,
// turns the collector off and keeps one processor, and the parent asserts on what the child reports. Nothing of the
// rest of the test binary is left to allocate in the measurement. MeasureRetained runs in a child the same way, but
// keeps what the function returns alive while the collector runs, because how much a result holds is not what a call
// allocated.
package alloctest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
)

const (
	// childEnv names the environment variable that marks the child and holds the test it runs, and childModeEnv which
	// measurement it runs: a test may measure both allocations and retention, each in a child of its own.
	childEnv     = "AXIOM_ALLOC_MEASURE"
	childModeEnv = "AXIOM_ALLOC_MEASURE_MODE"
	allocMode    = "allocs"
	retainedMode = "retained"
	// runs is how many times a function runs after its warm-up; the smallest run is reported, as load and the
	// runtime's own work only ever add to a run.
	runs = 5
	// marker starts every line the child writes, so the parent finds them among the test binary's own output.
	marker = "alloctest"
)

// Function is one allocation measurement: what F allocates, reported as Name.
type Function struct {
	Name string
	F    func()
}

// RetainedFunction is one retention measurement: what F returns is kept alive while the collector runs, and the heap
// that stays allocated is reported as Name. F may return nil when it retains nothing.
type RetainedFunction struct {
	Name string
	F    func() any
}

// Allocs is what a function allocated per run.
type Allocs struct {
	Count float64 // allocations per run
	Bytes float64 // bytes allocated per run
}

// measured is one function to measure: its name and whether it has a body. It lets Measure and MeasureRetained share
// the child process protocol without sharing what they run or report.
type measured interface {
	label() string
	defined() bool
}

func (f Function) label() string         { return f.Name }
func (f Function) defined() bool         { return f.F != nil }
func (f RetainedFunction) label() string { return f.Name }
func (f RetainedFunction) defined() bool { return f.F != nil }

// Measure runs each function in a child process of the test binary and returns what each allocated per run, by name.
//
// The child re-executes this test binary with only t selected, so no other test runs and no other test's goroutines
// are left to allocate. It warms each function once, then runs it five times with the collector off and one processor,
// as testing.AllocsPerRun does, and reports the smallest run: what the function allocates is the floor of every run,
// and work of the runtime's own can only add to one. Measure may be called once in t: the child stops t as soon as it
// has reported, so the test's own assertions run only in the parent.
func Measure(t *testing.T, funcs ...Function) map[string]Allocs {
	t.Helper()
	return measure(t, funcs, allocMode, childAllocs, parse)
}

// MeasureRetained runs each function in a child process of the test binary and returns the heap each result retains,
// in bytes, by name.
//
// Measure counts what a call allocates, garbage the collector later frees included; it cannot say what survives the
// call. MeasureRetained holds what F returns, settles the heap with two collections, and reports how much the heap grew
// from before the call. It repeats this five times with a fresh result and reports the smallest growth, since work of
// the runtime's own can only add retained objects. A function that allocates a large temporary and returns nil
// therefore retains nothing: the returned value is the lifetime that is measured. Like Measure, it may be called once
// in t: the child stops t as soon as it has reported.
func MeasureRetained(t *testing.T, funcs ...RetainedFunction) map[string]uint64 {
	t.Helper()
	return measure(t, funcs, retainedMode, childRetained, parseRetained)
}

// measure validates funcs, then runs them in the child that selects t for mode, or spawns that child and parses its
// report. It is the one place the two measurements share the child process protocol.
func measure[T measured, V any](t *testing.T, funcs []T, mode string,
	child func(*testing.T, []T), parse func([]byte, []T) (map[string]V, error),
) map[string]V {
	t.Helper()
	if len(funcs) == 0 {
		t.Fatal("alloctest: nothing to measure")
	}
	names := make(map[string]bool, len(funcs))
	for _, f := range funcs {
		if !f.defined() || f.label() == "" || strings.ContainsAny(f.label(), "\t\n") || names[f.label()] {
			t.Fatalf("alloctest: a measurement needs a unique name without a tab or newline, and a function: %q", f.label())
		}
		names[f.label()] = true
	}
	if os.Getenv(childEnv) == t.Name() && os.Getenv(childModeEnv) == mode {
		child(t, funcs)
		return nil // child stops the test before returning
	}
	out, err := spawn(t, mode)
	if err != nil {
		t.Fatalf("alloctest: %v\n%s", err, out)
	}
	results, err := parse(out, funcs)
	if err != nil {
		t.Fatalf("alloctest: %v\n%s", err, out)
	}
	return results
}

// runPattern is the -test.run expression that selects exactly name. The go command splits the expression on "/" and
// matches each element on its own, so every element is anchored: without the ends a test whose name begins with a
// selected one would run in the child too.
func runPattern(name string) string {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		parts[i] = "^" + regexp.QuoteMeta(part) + "$"
	}
	return strings.Join(parts, "/")
}

// spawn starts the child that measures t's functions in mode and returns its combined output.
func spawn(t *testing.T, mode string) ([]byte, error) {
	args := []string{"-test.count=1", "-test.timeout=10m", "-test.run=" + runPattern(t.Name())}
	cmd := exec.CommandContext(t.Context(), os.Args[0], args...) //nolint:gosec // this test binary, selected by its own name
	cmd.Env = append(os.Environ(), childEnv+"="+t.Name(), childModeEnv+"="+mode)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("the child measuring %s failed: %w", t.Name(), err)
	}
	return out, nil
}

// childAllocs measures funcs and stops t with the results on stdout. It runs in the child process only.
func childAllocs(t *testing.T, funcs []Function) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1)) // the measured function has the only processor
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	for _, f := range funcs {
		runtime.GC() // settle the heap this measurement starts from; collection stays off while it runs
		f.F()        // warm-up, as testing.AllocsPerRun runs one
		mallocs, bytes := ^uint64(0), ^uint64(0)
		for range runs {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			f.F()
			runtime.ReadMemStats(&after)
			mallocs = min(mallocs, after.Mallocs-before.Mallocs)
			bytes = min(bytes, after.TotalAlloc-before.TotalAlloc)
		}
		report(t, f.Name, mallocs, bytes)
	}
	t.SkipNow() // the parent has every measurement; the test's assertions are the parent's
}

// childRetained measures funcs and stops t with the results on stdout. It runs in the child process only.
func childRetained(t *testing.T, funcs []RetainedFunction) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1)) // the measured function has the only processor
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	for _, f := range funcs {
		report(t, f.Name, measureRetained(f.F))
	}
	t.SkipNow() // the parent has every measurement; the test's assertions are the parent's
}

// measureRetained returns the smallest heap growth a call to f leaves behind while its result stays alive.
func measureRetained(f func() any) uint64 {
	warm := f() // warm-up: any one-time cache the call fills is not part of what its result holds
	runtime.KeepAlive(warm)
	retained := ^uint64(0)
	for range runs {
		retained = min(retained, retainedOnce(f))
	}
	return retained
}

// retainedOnce measures one result of f: the heap after two collections with the result alive minus the heap after two
// collections before the call. The result's lifetime ends here, so the next call's collections free it.
func retainedOnce(f func() any) uint64 {
	runtime.GC()
	runtime.GC() // twice: the first sweep may leave the garbage of the result before
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	held := f()
	runtime.GC()
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(held)
	return heapGrowth(before.HeapAlloc, after.HeapAlloc)
}

// heapGrowth is the heap that grew from before to after. HeapAlloc is the whole process heap, so a collection freeing
// more than the measured call retained can make the second reading smaller: a shrinking heap grew by nothing, where
// subtracting the two unsigned readings would wrap into a huge number.
func heapGrowth(before, after uint64) uint64 {
	if after <= before {
		return 0
	}
	return after - before
}

// report writes one measurement for the parent to read.
func report(t *testing.T, name string, values ...uint64) {
	t.Helper()
	var line strings.Builder
	line.WriteString(marker)
	line.WriteByte('\t')
	line.WriteString(name)
	for _, value := range values {
		line.WriteByte('\t')
		line.WriteString(strconv.FormatUint(value, 10))
	}
	line.WriteByte('\n')
	if _, err := io.WriteString(os.Stdout, line.String()); err != nil {
		t.Fatalf("alloctest: write the measurement of %s: %v", name, err)
	}
}

// parse reads the allocation measurements the child wrote. Every function must be reported exactly once: a missing
// one means the child stopped before it, and its output, which the caller shows, says why.
func parse(out []byte, funcs []Function) (map[string]Allocs, error) {
	names := make([]string, len(funcs))
	for i, f := range funcs {
		names[i] = f.Name
	}
	return parseMeasurements(out, names, func(values []uint64) (Allocs, error) {
		if len(values) != 2 {
			return Allocs{}, errors.New("want allocations and bytes")
		}
		return Allocs{Count: float64(values[0]), Bytes: float64(values[1])}, nil
	})
}

// parseRetained reads the retention measurements the child wrote, with the same promises as parse.
func parseRetained(out []byte, funcs []RetainedFunction) (map[string]uint64, error) {
	names := make([]string, len(funcs))
	for i, f := range funcs {
		names[i] = f.Name
	}
	return parseMeasurements(out, names, func(values []uint64) (uint64, error) {
		if len(values) != 1 {
			return 0, errors.New("want retained bytes")
		}
		return values[0], nil
	})
}

// parseMeasurements reads the child's lines for names, turning each measurement's numbers into its result.
func parseMeasurements[T any](out []byte, names []string, convert func([]uint64) (T, error)) (map[string]T, error) {
	results := make(map[string]T, len(names))
	for line := range strings.Lines(string(out)) {
		name, values, ok, err := parseLine(line)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		result, err := convert(values)
		if err != nil {
			return nil, fmt.Errorf("malformed measurement %q: %w", line, err)
		}
		if _, ok := results[name]; ok {
			return nil, fmt.Errorf("the child reported %q twice", name)
		}
		results[name] = result
	}
	if err := checkResults(results, names); err != nil {
		return nil, err
	}
	return results, nil
}

// parseLine reads one line of the child's output; ok is false for a line that is not a measurement, and values are the
// numbers after the name.
func parseLine(line string) (name string, values []uint64, ok bool, err error) {
	rest, found := strings.CutPrefix(line, marker+"\t")
	if !found {
		return "", nil, false, nil
	}
	name, rest, found = strings.Cut(rest, "\t")
	if !found {
		return "", nil, true, fmt.Errorf("malformed measurement %q", line)
	}
	for field := range strings.SplitSeq(strings.TrimSuffix(rest, "\n"), "\t") {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return "", nil, true, fmt.Errorf("malformed number in %q: %w", line, err)
		}
		values = append(values, value)
	}
	return name, values, true, nil
}

// checkResults reports the first function the child did not measure, or measurements it reported beyond those asked
// for.
func checkResults[T any](results map[string]T, names []string) error {
	for _, name := range names {
		if _, ok := results[name]; !ok {
			return fmt.Errorf("the child did not report %q", name)
		}
	}
	if len(results) != len(names) {
		return fmt.Errorf("the child reported %d measurements, want %d", len(results), len(names))
	}
	return nil
}
