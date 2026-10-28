package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The parts of the load harness (docs/performance.md「怎么验证」) that need no database, so that `just test` checks
// them: the statistics the report is made of, the paced upstream, the proxy that cuts the database off, and reading a
// process's memory. The scenarios that use them sit behind the `load` build tag (load_scenarios_test.go).

// latencies are the durations a scenario measured.
type latencies []time.Duration

// quantile is the nearest-rank q-th quantile (0 < q <= 1): the smallest value that at least q of the samples do not
// exceed. It is the value an operator reads off a histogram; nothing is interpolated between two measurements. A
// scenario with no samples reports 0.
func (l latencies) quantile(q float64) time.Duration {
	if len(l) == 0 {
		return 0
	}
	sorted := slices.Sorted(slices.Values(l))
	rank := int(math.Ceil(q * float64(len(sorted))))
	return sorted[min(max(rank, 1), len(sorted))-1]
}

// spread is "p50 / p95 / p99" in milliseconds, "-" when nothing was measured.
func (l latencies) spread() string {
	if len(l) == 0 {
		return "-"
	}
	return fmt.Sprintf("%s / %s / %s", ms(l.quantile(0.50)), ms(l.quantile(0.95)), ms(l.quantile(0.99)))
}

func ms(d time.Duration) string {
	switch {
	case d >= 100*time.Millisecond:
		return strconv.FormatInt(d.Milliseconds(), 10)
	case d >= 10*time.Millisecond:
		return fmt.Sprintf("%.0f", float64(d)/float64(time.Millisecond))
	default:
		return fmt.Sprintf("%.1f", float64(d)/float64(time.Millisecond))
	}
}

// loadRow is one line of the result table: what a scenario's calls saw, in the columns the PR description reports.
type loadRow struct {
	Scenario string
	Calls    int // started
	Done     int // streams read to their last event
	Refused  int // answered 429 or 503: the node's own backpressure, which is not a failure
	Lost     int // streams the scenario ended on purpose: cut by the upstream or cancelled by the client
	Failed   int // everything else that did not end in a complete stream
	Overhead latencies
	TTFT     latencies
	Settle   latencies
	Wait     string // the queueing the scenario saw, in words
	PeakMiB  int64  // the node's peak resident memory above its idle level; 0 when it could not be read
}

// loadHeader is the table's first two lines.
const loadHeader = "| scenario | calls | done | refused 429/503 | cut or cancelled | failed | TTFT overhead ms p50 / p95 / p99 | TTFT ms p50 / p95 | " +
	"settle tail ms p50 / p95 / p99 | queueing | peak RSS above idle MiB |\n|---|---|---|---|---|---|---|---|---|---|---|\n"

func (r loadRow) markdown() string {
	peak := "-"
	if r.PeakMiB > 0 {
		peak = strconv.FormatInt(r.PeakMiB, 10)
	}
	ttft := "-"
	if len(r.TTFT) > 0 {
		ttft = ms(r.TTFT.quantile(0.50)) + " / " + ms(r.TTFT.quantile(0.95))
	}
	return fmt.Sprintf("| %s | %d | %d | %d | %d | %d | %s | %s | %s | %s | %s |\n",
		r.Scenario, r.Calls, r.Done, r.Refused, r.Lost, r.Failed, r.Overhead.spread(), ttft, r.Settle.spread(), r.Wait, peak)
}

// statusBytes reads a "Name:   123 kB" line of /proc/<pid>/status, in bytes.
func statusBytes(status, name string) (int64, error) {
	for line := range strings.SplitSeq(status, "\n") {
		rest, ok := strings.CutPrefix(line, name+":")
		if !ok {
			continue
		}
		number, unit, _ := strings.Cut(strings.TrimSpace(rest), " ")
		n, err := strconv.ParseInt(number, 10, 64)
		if err != nil || unit != "kB" {
			return 0, fmt.Errorf("status line %q is not a size in kB", line)
		}
		return n << 10, nil
	}
	return 0, fmt.Errorf("no %s in the process status", name)
}

// processRSS is a process's resident memory now, and its peak since the last resetPeak; Linux only.
func processRSS(pid int) (now, peak int64, err error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, 0, err
	}
	if now, err = statusBytes(string(raw), "VmRSS"); err != nil {
		return 0, 0, err
	}
	peak, err = statusBytes(string(raw), "VmHWM")
	return now, peak, err
}

// cpuTicks is the CPU time of the whole machine (busy and total ticks, from the first line of /proc/stat) or, with a
// pid, of one process (user and system ticks of /proc/<pid>/stat against the machine's total, so that the two can be
// compared over the same interval).
type cpuTicks struct{ busy, total, process int64 }

// parseMachineTicks reads the "cpu  user nice system idle iowait irq softirq steal ..." line of /proc/stat.
func parseMachineTicks(stat string) (busy, total int64, err error) {
	line, _, _ := strings.Cut(stat, "\n")
	fields := strings.Fields(line)
	if len(fields) < 9 || fields[0] != "cpu" {
		return 0, 0, fmt.Errorf("not the cpu line of /proc/stat: %q", line)
	}
	var idle int64
	for i, f := range fields[1:9] {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			return 0, 0, err
		}
		total += n
		if i == 3 || i == 4 { // idle and iowait
			idle += n
		}
	}
	return total - idle, total, nil
}

// parseProcessTicks is utime + stime of a /proc/<pid>/stat line. The command name in it is in parentheses and may hold
// spaces, so the fields are counted from the last closing one.
func parseProcessTicks(stat string) (int64, error) {
	_, rest, ok := strings.Cut(stat[strings.LastIndexByte(stat, ')')+1:], " ")
	fields := strings.Fields(rest)
	if !ok || len(fields) < 13 {
		return 0, fmt.Errorf("not a /proc/<pid>/stat line: %q", stat)
	}
	utime, err1 := strconv.ParseInt(fields[11], 10, 64)
	stime, err2 := strconv.ParseInt(fields[12], 10, 64)
	return utime + stime, errors.Join(err1, err2)
}

// since says how busy the machine was between two samples, and how much of the machine's CPU time the process used, in
// cores: the context that tells a number measured on a machine that was full from one measured on an idle one.
func (t cpuTicks) since(start cpuTicks, cores int) string {
	total := float64(t.total - start.total)
	if total <= 0 {
		return "no CPU sample"
	}
	return fmt.Sprintf("machine %.0f%% busy, node %.1f of %d cores", 100*float64(t.busy-start.busy)/total, float64(cores)*float64(t.process-start.process)/total, cores)
}
