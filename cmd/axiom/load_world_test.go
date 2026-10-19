//go:build load

package main

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/pgtest"
)

// loadPlan sizes a run. At scale 1 it is the load docs/performance.md「怎么验证」 asks for, in the 20 minutes a shared
// CI server allows it; a smaller scale shrinks the counts and the times alike, to try the harness on a small machine.
type loadPlan struct{ scale float64 }

const loadScaleVar = "AXIOM_LOAD_SCALE"

func loadPlanFromEnv(t *testing.T) loadPlan {
	t.Helper()
	raw, ok := os.LookupEnv(loadScaleVar)
	if !ok {
		return loadPlan{scale: 1}
	}
	scale, err := strconv.ParseFloat(raw, 64)
	if err != nil || scale <= 0 || scale > 1 {
		t.Fatalf("%s=%q: want a number above 0 and at most 1", loadScaleVar, raw)
	}
	return loadPlan{scale: scale}
}

// n is a count of base, scaled; at least one.
func (p loadPlan) n(base int) int { return max(1, int(math.Round(float64(base)*p.scale))) }

// d is a time of base, scaled, but not below two seconds (or base itself, when that is shorter): a stream must outlast
// the first token for anything to be measured.
func (p loadPlan) d(base time.Duration) time.Duration {
	return max(min(base, 2*time.Second), time.Duration(float64(base)*p.scale))
}

// loadWorld is the database, the accounts, the upstream and the clients of one run, and what it has measured.
type loadWorld struct {
	*callWorld
	plan     loadPlan
	upstream *loadUpstream
	driver   *loadDriver
	keyFiles [3]string // the master key, the key-hash key and the identity key, as files
	keys     []string  // one call key for each load account
	hotKeys  []string  // many keys of the first account, which the hot-account scenarios use: one key is limited to 6000 calls a minute

	mu    sync.Mutex
	rows  []loadRow
	notes []string
}

// loadAccounts is how many accounts the load is spread over: the node limits one to a tenth of its calls in flight.
const loadAccounts = 50

// loadCredit funds each account with a thousand dollars, in billionths, which covers the largest reservation of every
// call the scenarios make.
const loadCredit = 1_000_000_000_000

func newLoadWorld(t *testing.T, plan loadPlan) *loadWorld {
	t.Helper()
	w := newCallWorld(t)
	w.price()
	upstream := newLoadUpstream(t, streamShape{firstToken: 300 * time.Millisecond, every: 250 * time.Millisecond, length: 2 * time.Second})
	// The one supplier of the seeded world is pointed at the paced upstream and allowed as many calls at once as the load
	// makes (the seed allows four), and the models are given a context large enough for the megabytes of text scenario 3
	// sends, which at four bytes a token would not fit the 200000 tokens of the seed.
	if err := pgtest.Run(t.Context(), w.database.Superuser,
		"UPDATE suppliers.suppliers SET origin = '"+upstream.server.URL+"'",
		"UPDATE suppliers.credentials SET max_concurrency = 100000",
		"UPDATE catalog.model SET spec = jsonb_set(spec, '{context_length}', '4000000')"); err != nil {
		t.Fatal(err)
	}
	lw := &loadWorld{
		callWorld: w, plan: plan, upstream: upstream, driver: newLoadDriver(upstream),
		keyFiles: [3]string{keyFile(t, 'm'), keyFile(t, 'h'), keyFile(t, 'i')},
	}
	t.Cleanup(lw.driver.close)
	for i := range max(2, plan.n(loadAccounts)) {
		email := fmt.Sprintf("load%02d@example.com", i)
		if _, err := w.user(ownerPassword+"\n", "create", "--email", email); err != nil {
			t.Fatal(err)
		}
		lw.keys = append(lw.keys, w.fundedKey(email, loadCredit))
	}
	lw.hotKeys = []string{lw.keys[0]}
	for range hotKeys - 1 {
		lw.hotKeys = append(lw.hotKeys, w.fundedKey("load00@example.com", 0)) // another key of the same account, with no more credit
	}
	return lw
}

// hotKeys is how many keys the hot account has: the node refuses a key more than 6000 calls in a minute (gateway.Config.KeyRate),
// so an account that takes hundreds of calls a second must spread them over keys, as a busy customer does.
const hotKeys = 20

// hot is the i-th call of a load on the one hot account.
func (lw *loadWorld) hot(i int) callSpec {
	return callSpec{client: i % sourceAddresses, key: lw.hotKeys[i%len(lw.hotKeys)]}
}

// scenario runs one scenario as a subtest and makes it the test the world's checks report to.
func (lw *loadWorld) scenario(t *testing.T, name string, run func()) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		parent := lw.t
		lw.t = t
		defer func() { lw.t = parent }()
		run()
	})
}

// node starts a node on the world's database, through proxy when there is one, and aims the clients at it.
func (lw *loadWorld) node(proxy *faultProxy) *loadNode {
	lw.t.Helper()
	url, err := nodeDatabaseURL(lw.database.App, proxy)
	if err != nil {
		lw.t.Fatal(err)
	}
	n := lw.startLoadNode(url)
	lw.driver.public.Store(&n.addrs.Public)
	return n
}

// spec is the i-th call of a load spread over all the accounts and source addresses.
func (lw *loadWorld) spec(i int) callSpec {
	return callSpec{client: i % sourceAddresses, key: lw.keys[i%len(lw.keys)]}
}

func (lw *loadWorld) note(scenario, format string, args ...any) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	line := scenario + ": " + fmt.Sprintf(format, args...)
	lw.notes = append(lw.notes, line)
	lw.t.Log(line)
}

func (lw *loadWorld) addRow(r loadRow) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	lw.rows = append(lw.rows, r)
}

// record adds the row of outs, with the node's peak memory of the phase that made them.
func (lw *loadWorld) record(name string, outs []outcome, peakMiB int64) {
	row := summarize(name, outs)
	row.PeakMiB = peakMiB
	lw.addRow(row)
	if f := failures(outs); f != "" {
		lw.note(name, "failures: %s", f)
	}
}

// rssMeter reads a node process's resident memory while a phase runs: its level when it was ready, and the highest
// since the phase began, from the kernel's own peak counter where it can be reset and from sampling where not.
type rssMeter struct {
	pid   int
	idle  int64
	start cpuTicks // the machine's and the node's CPU time when the phase began

	mu     sync.Mutex
	peak   int64
	kernel bool // the kernel's peak was reset when the phase began, so it is this phase's
}

func newRSSMeter(pid int) *rssMeter {
	m := &rssMeter{pid: pid}
	if now, _, err := processRSS(pid); err == nil {
		m.idle = now
	}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			now, _, err := processRSS(pid)
			if err != nil {
				return // the process has ended
			}
			m.mu.Lock()
			m.peak = max(m.peak, now)
			m.mu.Unlock()
		}
	}()
	return m
}

func (m *rssMeter) begin() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.peak, m.kernel = 0, resetPeak(m.pid)
	m.start, _ = readTicks(m.pid) //nolint:errcheck // without a sample the phase's CPU line says so
}

// context says how busy the machine and the node were since the phase began, and the machine's load average.
func (m *rssMeter) context() string {
	now, err := readTicks(m.pid)
	if err != nil {
		return "CPU not measured"
	}
	load, _ := os.ReadFile("/proc/loadavg") //nolint:errcheck // an unreadable file reads as empty
	return now.since(m.start, runtime.NumCPU()) + ", load average " + strings.Join(strings.Fields(string(load))[:min(3, len(strings.Fields(string(load))))], " ")
}

// end is the phase's peak above the idle level, in MiB; 0 where memory cannot be read.
func (m *rssMeter) end() int64 {
	_, kernelPeak, err := processRSS(m.pid)
	if err != nil {
		kernelPeak = 0 // the node has ended: what was sampled stands
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	peak := m.peak
	if m.kernel {
		peak = max(peak, kernelPeak)
	}
	return max(0, peak-m.idle) >> 20
}

// report prints the table, the notes and the machine they were measured on.
func (lw *loadWorld) report() {
	var total int64
	if memory, err := os.ReadFile("/proc/meminfo"); err == nil {
		if total, err = statusBytes(string(memory), "MemTotal"); err != nil {
			total = 0
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nload run at scale %v on %d CPUs (GOMAXPROCS %d), %d MiB of memory, open files %s; client, upstream, database and node share the machine\n\n",
		lw.plan.scale, runtime.NumCPU(), runtime.GOMAXPROCS(0), total>>20, openFiles())
	b.WriteString(loadHeader)
	for _, r := range lw.rows {
		b.WriteString(r.markdown())
	}
	b.WriteString("\nnotes\n")
	for _, n := range lw.notes {
		b.WriteString("- " + n + "\n")
	}
	fmt.Print(b.String())
}

// openFiles is this process's limit on open files, soft and hard: 5000 streams are 20000 sockets across the harness and
// the node, and a limit below that shows as dial errors.
func openFiles() string {
	limits, err := os.ReadFile("/proc/self/limits")
	if err != nil {
		return "unknown"
	}
	for line := range strings.SplitSeq(string(limits), "\n") {
		if fields := strings.Fields(strings.TrimPrefix(line, "Max open files")); strings.HasPrefix(line, "Max open files") && len(fields) >= 2 {
			return fields[0] + "/" + fields[1]
		}
	}
	return "unknown"
}

// resetPeak makes the next VmHWM the peak from now on (proc(5), clear_refs 5), so a scenario reads its own peak and
// not an earlier one's. It reports whether the kernel allowed it.
func resetPeak(pid int) bool {
	return os.WriteFile("/proc/"+strconv.Itoa(pid)+"/clear_refs", []byte("5"), 0o200) == nil
}

// readTicks samples the machine and the process pid now.
func readTicks(pid int) (cpuTicks, error) {
	machine, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTicks{}, err
	}
	process, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return cpuTicks{}, err
	}
	var t cpuTicks
	if t.busy, t.total, err = parseMachineTicks(string(machine)); err != nil {
		return cpuTicks{}, err
	}
	t.process, err = parseProcessTicks(string(process))
	return t, err
}
