//go:build load

package main

import (
	"bufio"
	"context"
	"encoding/json/v2" //nolint:depguard // decodes the node's own log records; jsonx guards input from outside
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/telemetry"
)

// The load test runs the node as a process of its own, so that its memory is its own and not shared with the clients
// and the upstream that fill the test's: the harness runs this test binary again, with loadNodeEnv set, and that
// run is the node. It is what callWorld.start does in process, with the same loopback exception for the supplier.

const loadNodeEnv = "AXIOM_LOAD_NODE_ENVIRON"

// TestLoadNodeProcess is the node of a load run, not a test: it serves until its standard input closes, and does
// nothing when the harness did not start it.
func TestLoadNodeProcess(t *testing.T) {
	environ, ok := os.LookupEnv(loadNodeEnv)
	if !ok {
		t.Skip("started by TestLoad as the node")
	}
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin) //nolint:errcheck // the read ends when the harness closes the pipe or dies
		cancel()
	}()
	loopback := func(tel *telemetry.Telemetry) (*egress.Client, error) {
		return egress.NewForTesting(egresstest.Loopback(t), egress.WithTracerProvider(tel.Traces)), nil
	}
	if err := serveWith(ctx, strings.Split(environ, "\n"), slog.New(slog.NewJSONHandler(os.Stdout, nil)), loopback, startRecorder, nil); err != nil {
		t.Fatal(err)
	}
}

// loadNode is a node process as the harness sees it.
type loadNode struct {
	cmd    *exec.Cmd
	stdin  io.Closer
	addrs  addresses
	log    *loadLog
	exited chan struct{} // closed when the process has ended
	result error         // how it ended; read after exited
	meter  *rssMeter
}

// loadLog counts the node's log records by level and message: a load run is judged by what the node says about itself
// as well as by what its clients saw.
type loadLog struct {
	mu     sync.Mutex
	counts map[string]int
	lines  atomic.Int64
}

// add counts a record. A warning or an error is told apart by its cause too, shortened, but only while there are few
// kinds: a cause that never repeats must not grow the map with the load.
func (l *loadLog) add(level, msg, cause string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := level + " " + msg
	if cause != "" && level != "INFO" && len(l.counts) < 200 {
		key += " (" + shorten(cause) + ")"
	}
	l.counts[key]++
}

// problems lists the warnings and errors the node logged, the most frequent first, with how often.
func (l *loadLog) problems() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for key, n := range l.counts {
		if !strings.HasPrefix(key, "INFO") && !strings.HasPrefix(key, "DEBUG") {
			out = append(out, fmt.Sprintf("%d x %s", n, key))
		}
	}
	return out
}

// nodeDatabaseURL is the application's database URL, or the same database reached through proxy.
func nodeDatabaseURL(direct string, proxy *faultProxy) (string, error) {
	if proxy == nil {
		return direct, nil
	}
	u, err := url.Parse(direct)
	if err != nil {
		return "", err
	}
	u.Host = proxy.addr()
	return u.String(), nil
}

// startLoadNode starts a node on the world's database, through databaseURL, and returns it ready.
func (lw *loadWorld) startLoadNode(databaseURL string) *loadNode {
	t := lw.t
	t.Helper()
	environ := []string{
		"AXIOM_DATABASE_URL=" + databaseURL,
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0",
		"AXIOM_SHUTDOWN_TIMEOUT=30s", "AXIOM_MASTER_KEY_FILE=" + lw.keyFiles[0], "AXIOM_KEY_HASH_FILE=" + lw.keyFiles[1],
		"AXIOM_IDENTITY_KEY_FILE=" + lw.keyFiles[2], "AXIOM_CALL_LOG_DIR=" + t.TempDir(),
	}
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), os.Args[0], "-test.run=^TestLoadNodeProcess$", "-test.count=1", "-test.timeout=2h") //nolint:gosec // this very binary, run again
	cmd.Env = append(os.Environ(), loadNodeEnv+"="+strings.Join(environ, "\n"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	n := &loadNode{cmd: cmd, stdin: stdin, log: &loadLog{counts: map[string]int{}}, exited: make(chan struct{})}
	serving := make(chan addresses, 1)
	go n.read(stdout, serving)
	go func() { n.result = cmd.Wait(); close(n.exited) }()
	select {
	case n.addrs = <-serving:
	case <-n.exited:
		t.Fatalf("the node ended before it served: %v", n.result)
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill() //nolint:errcheck // the node never served; the test fails below
		t.Fatal("the node did not serve within a minute")
	}
	t.Cleanup(func() { _ = n.stop() }) //nolint:errcheck // a node that is not stopped yet is stopped here; its result was asked for by the scenario
	lw.waitReady(n)
	n.meter = newRSSMeter(cmd.Process.Pid)
	return n
}

// read scans the node's standard output: its log records, one JSON object a line, and the testing package's own lines.
func (n *loadNode) read(stdout io.Reader, serving chan<- addresses) {
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 1<<20), 1<<20)
	for scan.Scan() {
		line := scan.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var record struct {
			Level string `json:"level"`
			Err   string `json:"err"`
			addresses
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		n.log.lines.Add(1)
		n.log.add(record.Level, record.Msg, record.Err)
		if record.Msg == "serving" {
			select {
			case serving <- record.addresses:
			default:
			}
		}
	}
}

func (lw *loadWorld) waitReady(n *loadNode) {
	t := lw.t
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !n.isReady() {
		if time.Now().After(deadline) {
			t.Fatal("the node did not become ready within a minute")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (n *loadNode) isReady() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+n.addrs.Ops+"/readyz", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close() //nolint:errcheck // a probe
	return resp.StatusCode == http.StatusOK
}

// alive says whether the process is still running.
func (n *loadNode) alive() bool {
	select {
	case <-n.exited:
		return false
	default:
		return true
	}
}

// stop asks the node to shut down, by closing its standard input, and reports how it ended; a node that does not end
// within the shutdown deadline and its drain is killed, and that is an error.
func (n *loadNode) stop() error {
	_ = n.stdin.Close() //nolint:errcheck // closing twice is harmless
	select {
	case <-n.exited:
		return n.result
	case <-time.After(90 * time.Second):
		_ = n.cmd.Process.Kill() //nolint:errcheck // it is being killed because it did not end
		<-n.exited
		return errors.New("the node did not shut down within 90 seconds and was killed")
	}
}
