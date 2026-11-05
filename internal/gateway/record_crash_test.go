package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// TestACallSettledInTheBackgroundKeepsItsRecordThroughACrash: a call whose settlement misses its response is recorded,
// pending, before its ending is written, and the background settlement completes that record once it commits. A node
// killed before the background settlement, or after it committed and before its settled record was kept, still has
// the call's record on the next start, once, and the call is charged once by the settlement that committed or not at
// all, which leaves the reservation to the cleanup. The kill stops the call log without a drain, as a killed process
// leaves its spill, and fences the dead node's background goroutine off the ledger and the log.
func TestACallSettledInTheBackgroundKeepsItsRecordThroughACrash(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		end     func(*dyingNode)
		charges int64
		pending bool // the record has no amounts: none were known when the node died
	}{
		{"not killed", (*dyingNode).live, 1, false},
		{"killed before the background settlement", (*dyingNode).dieBeforeSettlement, 0, true},
		{"killed after the background settlement committed", (*dyingNode).dieAfterCommit, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := newDyingNode(t)
			n.call()
			tc.end(n)
			if err := callLog(t, n.r, n.dir).Close(t.Context()); err != nil { // the next start replays the spill
				t.Fatal(err)
			}
			checkRecordAndCharge(n.r, tc.pending, tc.charges)
		})
	}
}

// dyingNode is one node whose ledger takes no settlement until writable is set, so its call's ending goes out pending
// and the settlement is left to the background.
type dyingNode struct {
	t        *testing.T
	r        *rig
	dir      string
	records  *observability.Recorder
	up       *fakeUpstreams
	writable atomic.Bool
	hold     atomic.Bool   // a committed settlement waits for killed before its caller hears of it
	commit   chan struct{} // closed when a held settlement has committed
	killed   chan struct{}
}

func newDyingNode(t *testing.T) *dyingNode {
	n := &dyingNode{t: t, dir: t.TempDir(), commit: make(chan struct{}), killed: make(chan struct{})}
	n.r = newRig(t, func(o *rigOptions) {
		o.segments = billing.Segments{Output: 100, Overshoot: time.Second}
		o.endpoints = func(_ string, e *routing.Endpoint) {
			e.OutputSpeed, e.Target = 100, protocol.Target{Model: "up"}
		}
		o.config = func(c *Config) { c.SettleRetry, c.SettleBackground = 200*time.Millisecond, 2*time.Second }
	})
	n.up = withExecutor(n.r, &flakyLedger{
		settle: func() (bool, error) {
			if n.writable.Load() {
				return true, nil
			}
			return false, io.ErrUnexpectedEOF
		},
		committed: func() {
			if n.hold.Load() {
				close(n.commit)
				<-n.killed
			}
		},
	})
	n.records = callLog(t, n.r, n.dir)
	n.r.exec.Records, n.r.handler.Records = n.records, n.records
	return n
}

// call makes the node's one call, whose ending must be out pending with the call's record already kept.
func (n *dyingNode) call() {
	a := n.r.funded(1e12)
	sample, _ := sample(n.t, protocol.Messages)
	for _, e := range n.up.eps {
		e.script = []scripted{streamed(sample, 3, 50, false)}
	}
	resp, body := n.r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(stream("acme/beta")))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"billing_pending":true`) {
		n.t.Fatalf("%d:\n%s", resp.StatusCode, body)
	}
	if kept, _ := n.records.AlertSample(); kept != 1 {
		n.t.Fatalf("%d records kept when the ending was written, want the call's pending one", kept)
	}
}

// live lets the background settlement commit and the node shut down cleanly.
func (n *dyingNode) live() {
	n.writable.Store(true)
	n.r.exec.Outstanding.Drain(n.t.Context(), time.Minute)
	if err := n.records.Close(n.t.Context()); err != nil {
		n.t.Fatal(err)
	}
}

// dieBeforeSettlement kills the node while the ledger is still closed: its settlement never commits.
func (n *dyingNode) dieBeforeSettlement() {
	kill(n.t, n.records)
	n.r.exec.Outstanding.Drain(n.t.Context(), time.Minute)
}

// dieAfterCommit kills the node once the background settlement has committed and before the gateway hears of it, so
// before the settled record can be kept. What the dead node's goroutine does afterwards reaches neither the spill nor
// the database.
func (n *dyingNode) dieAfterCommit() {
	n.hold.Store(true)
	n.writable.Store(true)
	<-n.commit
	kill(n.t, n.records)
	close(n.killed)
	n.r.exec.Outstanding.Drain(n.t.Context(), time.Minute)
}

// checkRecordAndCharge: the call has one record, pending without amounts or settled at the ledger's charge, and the
// ledger booked charges settlements.
func checkRecordAndCharge(r *rig, pending bool, charges int64) {
	r.t.Helper()
	settled, wantPending := int64(1), int64(0)
	if pending {
		settled, wantPending = 0, 1
	}
	if n := r.count("SELECT count(*) FROM observability.call_log"); n != 1 {
		r.t.Errorf("%d records of the call, want 1", n)
	}
	if n := r.count(`SELECT count(*) FROM observability.call_log
		WHERE billing_pending AND price_nano = 0 AND usage = '{}' AND price_version IS NULL`); n != wantPending {
		r.t.Errorf("%d pending records without amounts, want %d", n, wantPending)
	}
	if n := r.count(`SELECT count(*) FROM observability.call_log l JOIN billing.reservations v ON v.request_id = l.id
		WHERE NOT l.billing_pending AND v.charged > 0 AND l.price_nano = v.charged`); n != settled {
		r.t.Errorf("%d settled records at the ledger's charge, want %d", n, settled)
	}
	if n := r.count("SELECT count(*) FROM billing.transactions WHERE kind = 'settle'"); n != charges {
		r.t.Errorf("%d settlements booked, want %d", n, charges)
	}
}

// callLog opens the node's call log on dir, holding records in the spill until it closes or the node dies.
func callLog(t *testing.T, r *rig, dir string) *observability.Recorder {
	t.Helper()
	records, err := observability.NewRecorder(t.Context(), r.db, platformScope, observability.Config{
		Dir: dir, Capacity: 8, BatchSize: 8, FlushInterval: time.Hour,
		RetryBase: time.Millisecond, RetryMax: 10 * time.Millisecond, WriteTimeout: 5 * time.Second,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return records
}

// kill stops the call log as a killed process leaves it: no drain, and the records it kept stay in the spill for the
// next start. Every Commit after it fails, as no Commit happens in a dead process.
func kill(t *testing.T, records *observability.Recorder) {
	t.Helper()
	dead, cancel := context.WithCancel(t.Context())
	cancel()
	if err := records.Close(dead); err != nil && !errors.Is(err, observability.ErrPending) {
		t.Fatal(err)
	}
}
