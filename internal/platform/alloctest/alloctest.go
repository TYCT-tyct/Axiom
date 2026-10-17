// Package alloctest measures what a function allocates without the rest of the test binary running beside it.
//
// testing.AllocsPerRun counts the whole process: a collection another goroutine starts inside a measured run, or a
// goroutine the scheduler runs while the measured one is preempted, adds to the count and to the bytes, so a bound
// tight enough to prove "this call allocates nothing" fails at random on a loaded machine. Measure runs the function
// in a child process of the test binary: the child selects only the calling test, settles the heap, turns the
// collector off and keeps one processor, and the parent asserts on what the child reports. Nothing of the rest of the
// test binary is left to allocate in the measurement.
package alloctest

import (
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
	// childEnv names the environment variable that marks the child and holds the test it runs.
	childEnv = "AXIOM_ALLOC_MEASURE"
	// runs is how many times a function runs after its warm-up; the smallest run is reported, as load and the
	// runtime's own work only ever add to a run.
	runs = 5
	// marker starts every line the child writes, so the parent finds them among the test binary's own output.
	marker = "alloctest"
)

// Function is one measurement: what F allocates, reported as Name.
type Function struct {
	Name string
	F    func()
}

// Allocs is what a function allocated per run.
type Allocs struct {
	Count float64 // allocations per run
	Bytes float64 // bytes allocated per run
}

// Measure runs each function in a child process of the test binary and returns what each allocated per run, by name.
//
// The child re-executes this test binary with only t selected, so no other test runs and no other test's goroutines
// are left to allocate. It warms each function once, then runs it five times with the collector off and one processor,
// as testing.AllocsPerRun does, and reports the smallest run: what the function allocates is the floor of every run,
// and work of the runtime's own can only add to one. Measure may be called once in t: the child stops t as soon as it
// has reported, so the test's own assertions run only in the parent.
func Measure(t *testing.T, funcs ...Function) map[string]Allocs {
	t.Helper()
	if len(funcs) == 0 {
		t.Fatal("alloctest: nothing to measure")
	}
	names := make(map[string]bool, len(funcs))
	for _, f := range funcs {
		if f.F == nil || f.Name == "" || strings.ContainsAny(f.Name, "\t\n") || names[f.Name] {
			t.Fatalf("alloctest: a measurement needs a unique name without a tab or newline, and a function: %q", f.Name)
		}
		names[f.Name] = true
	}
	if os.Getenv(childEnv) == t.Name() {
		child(t, funcs)
		return nil // child stops the test before returning
	}
	out, err := spawn(t)
	if err != nil {
		t.Fatalf("alloctest: %v\n%s", err, out)
	}
	results, err := parse(out, funcs)
	if err != nil {
		t.Fatalf("alloctest: %v\n%s", err, out)
	}
	return results
}

// spawn starts the child that measures t's functions and returns its combined output.
func spawn(t *testing.T) ([]byte, error) {
	args := []string{"-test.count=1", "-test.timeout=10m", "-test.run=^" + regexp.QuoteMeta(t.Name()) + "$"}
	cmd := exec.CommandContext(t.Context(), os.Args[0], args...) //nolint:gosec // this test binary, selected by its own name
	cmd.Env = append(os.Environ(), childEnv+"="+t.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("the child measuring %s failed: %w", t.Name(), err)
	}
	return out, nil
}

// child measures funcs and stops t with the results on stdout. It runs in the child process only.
func child(t *testing.T, funcs []Function) {
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

// report writes one measurement for the parent to read.
func report(t *testing.T, name string, mallocs, bytes uint64) {
	t.Helper()
	line := marker + "\t" + name + "\t" + strconv.FormatUint(mallocs, 10) + "\t" + strconv.FormatUint(bytes, 10) + "\n"
	if _, err := io.WriteString(os.Stdout, line); err != nil {
		t.Fatalf("alloctest: write the measurement of %s: %v", name, err)
	}
}

// parse reads the measurements the child wrote. Every function must be reported exactly once: a missing one means the
// child stopped before it, and its output, which the caller shows, says why.
func parse(out []byte, funcs []Function) (map[string]Allocs, error) {
	results := make(map[string]Allocs, len(funcs))
	for line := range strings.Lines(string(out)) {
		rest, ok := strings.CutPrefix(line, marker+"\t")
		if !ok {
			continue
		}
		name, rest, ok := strings.Cut(rest, "\t")
		if !ok {
			return nil, fmt.Errorf("malformed measurement %q", line)
		}
		count, rest, ok := strings.Cut(rest, "\t")
		if !ok {
			return nil, fmt.Errorf("malformed measurement %q", line)
		}
		mallocs, err := strconv.ParseUint(count, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("malformed allocation count in %q: %w", line, err)
		}
		bytes, err := strconv.ParseUint(strings.TrimSuffix(rest, "\n"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("malformed allocation bytes in %q: %w", line, err)
		}
		if _, ok := results[name]; ok {
			return nil, fmt.Errorf("the child reported %q twice", name)
		}
		results[name] = Allocs{Count: float64(mallocs), Bytes: float64(bytes)}
	}
	for _, f := range funcs {
		if _, ok := results[f.Name]; !ok {
			return nil, fmt.Errorf("the child did not report %q", f.Name)
		}
	}
	if len(results) != len(funcs) {
		return nil, fmt.Errorf("the child reported %d measurements, want %d", len(results), len(funcs))
	}
	return results, nil
}
