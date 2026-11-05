package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// TestACallSettledInTheBackgroundKeepsItsRecordThroughACrash: a call whose settlement misses its response is recorded,
// pending, before its ending is written, and the background settlement completes that record once it commits. A node
// killed before the background settlement, or after it committed and before its settled record was kept, still has
// the call's record on the next start, once, and the call is charged once by the settlement that committed or not at
// all, which leaves the reservation to the cleanup. The record of a settlement that committed carries the ledger's
// amounts, which the next start's writer reads from billing; one without a settlement stays pending. Replaying the
// same parked record again, as a crash between its insert and the removal of its file would, changes nothing. The kill
// stops the call log without a drain, as a killed process leaves its spill, and fences the dead node's background
// goroutine off the ledger and the log. A record left pending is kept but not read as a call without a cost: the
// account's generation lookup does not find it.
func TestACallSettledInTheBackgroundKeepsItsRecordThroughACrash(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		end     func(*dyingNode)
		charges int64
		pending bool // the record has no amounts: the ledger has no settlement of the call
	}{
		{"not killed", (*dyingNode).live, 1, false},
		{"killed before the background settlement", (*dyingNode).dieBeforeSettlement, 0, true},
		{"killed after the background settlement committed", (*dyingNode).dieAfterCommit, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := newDyingNode(t)
			n.call()
			tc.end(n)
			parked := t.TempDir()
			if err := os.CopyFS(parked, os.DirFS(filepath.Join(n.dir, "pending"))); err != nil {
				t.Fatal(err)
			}
			n.restart() // the next start replays the spill
			checkRecordAndCharge(n.r, tc.pending, tc.charges)
			n.checkGeneration(tc.pending)

			stored := n.storedRecord()
			if err := os.CopyFS(filepath.Join(n.dir, "pending"), os.DirFS(parked)); err != nil {
				t.Fatal(err)
			}
			n.restart()
			if again := n.storedRecord(); again != stored {
				t.Errorf("the record after the repair ran again:\n%s\nwant it unchanged:\n%s", again, stored)
			}
		})
	}
}

// TestASettlementCommittingAfterItWasGivenUpReachesItsRecord: the background gives a settlement up while its last try
// is still committing in the database, as a try whose answer the gateway stopped waiting for. The pending record is
// then in the spill, and the writer reads the ledger before that commit lands. The read waits for the reservation the
// try wrote, so the call's one row carries the ledger's amounts and the call is charged once; replaying the same
// pending record, as a crash between its insert and the removal of its file would, changes nothing.
func TestASettlementCommittingAfterItWasGivenUpReachesItsRecord(t *testing.T) {
	t.Parallel()
	n := newDyingNode(t)
	n.call()
	parked := t.TempDir()
	if err := os.CopyFS(parked, os.DirFS(filepath.Join(n.dir, "pending"))); err != nil {
		t.Fatal(err)
	}
	n.stray.Store(true)
	n.r.exec.Outstanding.Drain(t.Context(), time.Minute) // given up: Release moved the pending record into the spill
	select {
	case <-n.strayWritten:
	case err := <-n.strayDone:
		t.Fatalf("the straggling settlement did not write: %v", err)
	}

	n.writeWhileTheStrayCommits()
	checkRecordAndCharge(n.r, false, 1)

	stored := n.storedRecord()
	if err := os.CopyFS(filepath.Join(n.dir, "pending"), os.DirFS(parked)); err != nil {
		t.Fatal(err)
	}
	n.restart()
	if again := n.storedRecord(); again != stored {
		t.Errorf("the record after the pending record was replayed:\n%s\nwant it unchanged:\n%s", again, stored)
	}
}

// writeWhileTheStrayCommits drains the call log while the straggling settlement is still to commit, and lets it commit
// once the writer waits on the reservation it wrote, or once the writer finished without waiting.
func (n *dyingNode) writeWhileTheStrayCommits() {
	closed := make(chan error, 1)
	go func() { closed <- n.records.Close(n.t.Context()) }()
	var err error
	finished := false
	waitFor(n.t, "the writer to wait for the settlement or finish without it", func() bool {
		select {
		case err = <-closed:
			finished = true
			return true
		default:
			return n.r.count(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
				AND wait_event_type = 'Lock' AND query LIKE '%FROM billing.reservations%FOR SHARE%'`) > 0
		}
	})
	close(n.strayCommit)
	if err := <-n.strayDone; err != nil {
		n.t.Fatal(err)
	}
	if !finished {
		err = <-closed
	}
	if err != nil {
		n.t.Fatal(err)
	}
}

// restart starts the node's call log again on its directory and closes it once the spill is written.
func (n *dyingNode) restart() {
	if err := callLog(n.t, n.r, n.dir).Close(n.t.Context()); err != nil {
		n.t.Fatal(err)
	}
}

// storedRecord is the call's one row in the call log, every column.
func (n *dyingNode) storedRecord() (row string) {
	n.t.Helper()
	if err := n.r.db.Tx(n.t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(n.t.Context(), "SELECT to_jsonb(l)::text FROM observability.call_log l").Scan(&row)
	}); err != nil {
		n.t.Fatal(err)
	}
	return row
}

// dyingNode is one node whose ledger takes no settlement until writable is set, so its call's ending goes out pending
// and the settlement is left to the background. Nothing here waits on a clock: the background retries until the
// test lets the settlement through or kills the node, and SettleBackground is far longer than the test.
type dyingNode struct {
	t        *testing.T
	r        *rig
	dir      string
	records  *observability.Recorder
	up       *fakeUpstreams
	key      string
	callID   string
	writable atomic.Bool
	dead     atomic.Bool   // the ledger refuses for good, as a dead node's writes never arrive
	hold     atomic.Bool   // a committed settlement waits for killed before its caller hears of it
	commit   chan struct{} // closed when a held settlement has committed
	killed   chan struct{}
	// stray makes the next try write its settlement on a transaction of its own and fail as given up; that
	// transaction closes strayWritten once its writes are in and commits when strayCommit is closed.
	stray                     atomic.Bool
	strayWritten, strayCommit chan struct{}
	strayDone                 chan error
}

func newDyingNode(t *testing.T) *dyingNode {
	n := &dyingNode{
		t: t, dir: t.TempDir(), commit: make(chan struct{}), killed: make(chan struct{}),
		strayWritten: make(chan struct{}), strayCommit: make(chan struct{}), strayDone: make(chan error, 1),
	}
	n.r = newRig(t, func(o *rigOptions) {
		o.segments = billing.Segments{Output: 100, Overshoot: time.Second}
		o.endpoints = func(_ string, e *routing.Endpoint) {
			e.OutputSpeed, e.Target = 100, protocol.Target{Model: "up"}
		}
		o.config = func(c *Config) { c.SettleRetry, c.SettleBackground = 200*time.Millisecond, 10*time.Minute }
	})
	n.up = withCosts(n.r, strayLedger{n: n, flakyLedger: &flakyLedger{
		Billing: n.r.billing,
		settle: func() (bool, error) {
			switch {
			case n.dead.Load():
				return false, pg.ErrNoRows // permanent: the background gives the settlement up at once
			case n.writable.Load():
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
	}}, fakeCosts{cost: 1000, known: true})
	n.records = callLog(t, n.r, n.dir)
	n.r.exec.Records, n.r.handler.Records = n.records, n.records
	return n
}

// strayLedger is the dying node's ledger. A try taken while stray is set settles on a transaction the gateway does not
// wait for and reports a failure no retry mends, so the background gives the settlement up while it is still to
// commit, as a try whose commit the database finishes after the gateway stopped waiting for it.
type strayLedger struct {
	*flakyLedger
	n *dyingNode
}

func (l strayLedger) SettleIn(ctx context.Context, tx pg.Tx, h *billing.Hold, o billing.Outcome) (billing.Final, func(), error) {
	if !l.n.stray.CompareAndSwap(true, false) {
		return l.flakyLedger.SettleIn(ctx, tx, h, o)
	}
	go func() {
		ctx := context.WithoutCancel(ctx)
		l.n.strayDone <- l.n.r.db.Tx(ctx, platformScope, func(tx pg.Tx) error {
			if _, _, err := l.Billing.SettleIn(ctx, tx, h, o); err != nil {
				return err
			}
			close(l.n.strayWritten)
			<-l.n.strayCommit
			return nil
		})
	}()
	return billing.Final{}, nil, pg.ErrNoRows
}

// call makes the node's one call, whose ending must be out pending.
func (n *dyingNode) call() {
	a := n.r.funded(1e12)
	n.key = a.key
	sample, _ := sample(n.t, protocol.Messages)
	for _, e := range n.up.eps {
		e.script = []scripted{streamed(sample, 3, 50, false)}
	}
	resp, body := n.r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(stream("acme/beta")))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"billing_pending":true`) {
		n.t.Fatalf("%d:\n%s", resp.StatusCode, body)
	}
	n.callID = resp.Header.Get("X-Generation-Id")
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
	n.dead.Store(true)
	n.r.exec.Outstanding.Drain(n.t.Context(), time.Minute)
}

// dieAfterCommit kills the node once the background settlement has committed and before the gateway hears of it, so
// before the settled record can be kept. What the dead node's goroutine does afterwards reaches neither the spill nor
// the database.
func (n *dyingNode) dieAfterCommit() {
	n.hold.Store(true)
	n.writable.Store(true)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		n.r.exec.Outstanding.Drain(n.t.Context(), time.Minute)
	}()
	select {
	case <-n.commit:
	case <-drained:
		n.t.Fatal("the background settlement ended without committing")
	}
	kill(n.t, n.records)
	close(n.killed)
	<-drained
}

// checkGeneration: the account finds a settled call by its number and does not find a pending one, whose cost and usage
// are not known to the log.
func (n *dyingNode) checkGeneration(pending bool) {
	resp, body := n.r.do(http.MethodGet, "/api/v1/generation?id="+n.callID, n.key, nil)
	want := http.StatusOK
	if pending {
		want = http.StatusNotFound
	}
	if resp.StatusCode != want {
		n.t.Errorf("generation of the call: %d, want %d:\n%s", resp.StatusCode, want, body)
	}
}

// checkRecordAndCharge: the call has one record, pending without amounts or settled at the ledger's charge, price
// version and waiver, and the ledger booked charges settlements.
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
		WHERE NOT l.billing_pending AND v.state = 'settled' AND v.charged > 0 AND l.price_nano = v.charged
		AND l.price_version = v.price_versions[v.entry + 1] AND l.free_of_charge = v.waived AND l.usage <> '{}'`); n != settled {
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
		Ledger: LedgerAmounts(billing.NewChargeReader(nil, nil)),
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
