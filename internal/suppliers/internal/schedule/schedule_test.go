package schedule

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/store"
)

var platform = pg.NewScope(true, nil, nil)

// system is the made-up supplier system the tests sync: its manager answers as its fields say, and counts the calls.
type system struct {
	adapter.Adapter
	openErr  error
	account  func() (adapter.Account, error)
	opens    atomic.Int32
	accounts atomic.Int32
	// holdKeys, if set, makes the first listing of keys of each opened adapter end at the supplier's limits for as long
	// as it says, as a listing of more pages than a window allows does; the second goes on and is counted as finished.
	holdKeys     time.Duration
	keysFinished atomic.Int32
	// holdOpen, if set, makes the first Open end at the supplier's limits for as long as it says, as a login the
	// supplier answers with a wait does; the next goes through.
	holdOpen time.Duration
	// stuckKeys, if not zero, makes every listing of keys end at limits that reopen that long from now (a time already
	// past if negative), so that the round is held up for as long as the job goes on.
	stuckKeys time.Duration
	keysCalls atomic.Int32
}

func (*system) Kind() adapter.Kind            { return "fake" }
func (*system) Buckets() []adapter.BucketSpec { return nil }
func (s *system) Open(context.Context, adapter.Client, adapter.LoginStore) (adapter.Manager, error) {
	if s.opens.Add(1) == 1 && s.holdOpen > 0 {
		return nil, adapter.Blocked{Until: time.Now().Add(s.holdOpen)}
	}
	if s.openErr != nil {
		return nil, s.openErr
	}
	return &manager{s: s}, nil
}

type manager struct {
	s    *system
	keys int
}

func (m *manager) Account(context.Context) (adapter.Account, error) {
	m.s.accounts.Add(1)
	return m.s.account()
}

func (*manager) Offer(context.Context) (adapter.Offer, error) { return adapter.Offer{}, nil }

func (m *manager) Keys(context.Context) ([]adapter.KeyState, error) {
	m.keys++
	m.s.keysCalls.Add(1)
	if m.s.stuckKeys != 0 {
		return nil, adapter.Blocked{Until: time.Now().Add(m.s.stuckKeys)}
	}
	if m.keys == 1 && m.s.holdKeys > 0 {
		return nil, adapter.Blocked{Until: time.Now().Add(m.s.holdKeys)}
	}
	m.s.keysFinished.Add(1)
	return nil, nil
}

func (*manager) KeySecret(context.Context, adapter.KeyRef) (secret.Secret, error) {
	return secret.Secret{}, errors.New("not used")
}

func (*manager) Charges(_ context.Context, scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	return adapter.ChargePage{Done: true, Through: time.Now()}, adapter.ScanState(`{"v":1}`), nil
}

func healthy() func() (adapter.Account, error) {
	return func() (adapter.Account, error) {
		return adapter.Account{Balance: "100", Funding: adapter.FundingWallet}, nil
	}
}

// node is one node of the cluster: a pool and a queue of its own.
type node struct {
	db *pg.DB
	q  *pg.Queue
	s  *Schedule
}

type cluster struct {
	t        *testing.T
	database pgtest.Database
	system   *system
	keys     *secret.Keyring
	reader   *pg.DB // one pool for all the reads of a test, which polls
	supplier uuid.UUID
}

func newCluster(t *testing.T, s *system) *cluster {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(path, []byte("1:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := secret.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c := &cluster{t: t, database: pgtest.New(t, pg.Module{Name: "suppliers", Migrations: store.Migrations()}), system: s, keys: keys}
	c.reader = c.open()
	c.supplier = c.addSupplier("fake.example", "connecting")
	return c
}

// addSupplier makes a supplier of the fake system at the host, in the status.
func (c *cluster) addSupplier(host, status string) uuid.UUID {
	c.t.Helper()
	return c.addSuppliers(status, "direct", host)[0]
}

// addSuppliers makes suppliers of the fake system at the hosts, in the status, reached through the exit.
func (c *cluster) addSuppliers(status, exit string, hosts ...string) (ids []uuid.UUID) {
	c.t.Helper()
	err := c.open().Tx(c.t.Context(), platform, func(tx pg.Tx) error {
		for _, host := range hosts {
			id, err := store.New().InsertSupplier(c.t.Context(), tx, store.InsertSupplierParams{
				OwnerScope: "platform", Name: host, Kind: "fake", Origin: "https://" + host, UpstreamAccount: "me",
				NativeUnit: "quota", SettlementCurrency: "USD", MgmtEgressID: exit,
			})
			if err != nil {
				return err
			}
			if _, err = tx.Exec(c.t.Context(), "UPDATE suppliers.suppliers SET status = $2 WHERE id = $1", id, status); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return ids
}

func (c *cluster) open() *pg.DB {
	c.t.Helper()
	db, err := pg.Open(c.t.Context(), c.database.App)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(db.Close)
	return db
}

// node registers the schedule on a queue of its own; the queue is not started.
func (c *cluster) node(holder string, sweepEvery time.Duration) *node {
	c.t.Helper()
	reg, err := registry.New(c.system)
	if err != nil {
		c.t.Fatal(err)
	}
	db := c.open()
	q := db.NewQueue(slog.New(slog.DiscardHandler))
	cfg := Config{
		DB: db, Scope: platform, Keys: c.keys, Egress: egress.NewForTesting(egresstest.Loopback(c.t)), Queue: q, Registry: reg,
		Holder: holder, SweepEvery: sweepEvery, Now: time.Now, Logger: slog.New(slog.DiscardHandler),
	}
	s, err := New(cfg)
	if err != nil {
		c.t.Fatal(err)
	}
	return &node{db: db, q: q, s: s}
}

func (c *cluster) start(n *node) {
	c.t.Helper()
	if err := n.q.Start(c.t.Context()); err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.t.Context()), 10*time.Second)
		defer cancel()
		if err := n.q.Stop(ctx); err != nil {
			c.t.Errorf("Stop: %v", err)
		}
	})
}

// row reads one value about the supplier.
func (c *cluster) row(query string, dest ...any) {
	c.t.Helper()
	err := c.reader.ReadTx(c.t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(c.t.Context(), query, c.supplier).Scan(dest...)
	})
	if err != nil {
		c.t.Fatal(err)
	}
}

func (c *cluster) waitFor(what string, done func() bool) {
	c.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			c.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// enqueued lists the suppliers a sync job exists for.
func (c *cluster) enqueued(db *pg.DB) (ids []string) {
	c.t.Helper()
	err := db.ReadTx(c.t.Context(), platform, func(tx pg.Tx) error {
		rows, err := tx.Query(c.t.Context(), `SELECT args->>'supplier' FROM river.river_job WHERE kind = 'suppliers.sync' ORDER BY 1`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return ids
}

// TestADueSupplierIsSyncedByTheQueue: a supplier that was never synced is found by the sweep, enqueued, and a round of
// the engine runs over its adapter: its state is stored, and its balance and status are written on the supplier. Once
// done it is not due, so no second round follows.
func TestADueSupplierIsSyncedByTheQueue(t *testing.T) {
	t.Parallel()
	s := &system{account: healthy()}
	c := newCluster(t, s)
	c.start(c.node("a", 100*time.Millisecond))
	c.waitFor("the first round", func() bool {
		var version int64
		c.row("SELECT coalesce((SELECT version FROM suppliers.sync_states WHERE supplier_id = $1), 0)", &version)
		return version > 0
	})
	var balance, status string
	c.row("SELECT balance_native::text, status FROM suppliers.suppliers WHERE id = $1", &balance, &status)
	if balance != "100" || status != "active" {
		t.Errorf("the supplier after its first round: balance %q, status %q, want 100 and active", balance, status)
	}
	time.Sleep(time.Second) // ten sweeps: the supplier is not due again for a minute
	if got := s.accounts.Load(); got != 1 {
		t.Errorf("the balance was read %d times, want once: the supplier was due only once", got)
	}
}

// TestARejectedAccountStopsTheSyncing: when the supplier rejects the account, whether in the login or in a request, the
// supplier needs attention, and no round after calls it again.
func TestARejectedAccountStopsTheSyncing(t *testing.T) {
	t.Parallel()
	for name, s := range map[string]*system{
		"in a request": {account: func() (adapter.Account, error) { return adapter.Account{}, adapter.ErrAuthRejected }},
		"in the login": {openErr: adapter.ErrAuthRejected},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newCluster(t, s)
			c.start(c.node("a", 100*time.Millisecond))
			c.waitFor("the supplier to need attention", func() bool {
				var status string
				c.row("SELECT status FROM suppliers.suppliers WHERE id = $1", &status)
				return status == "needs_attention"
			})
			opens, accounts := s.opens.Load(), s.accounts.Load()
			time.Sleep(time.Second)
			if s.opens.Load() != opens || s.accounts.Load() != accounts {
				t.Errorf("the supplier was called again after it rejected the account")
			}
			if opens != 1 {
				t.Errorf("the adapter was opened %d times in the round, want once for all its tasks", opens)
			}
		})
	}
}

// TestTheAdapterIsLoggedInOncePerRound: when the login fails in a way that is not a rejection, every task of the round
// fails with it, and the supplier is logged into once, not once for each of the four.
func TestTheAdapterIsLoggedInOncePerRound(t *testing.T) {
	t.Parallel()
	s := &system{openErr: errors.New("the supplier is down")}
	c := newCluster(t, s)
	if err := c.node("a", time.Hour).s.sync(t.Context(), c.supplier); err != nil {
		t.Fatal(err)
	}
	if got := s.opens.Load(); got != 1 {
		t.Errorf("the adapter was opened %d times in one round, want once", got)
	}
	var version int64
	c.row("SELECT version FROM suppliers.sync_states WHERE supplier_id = $1", &version)
	if version != 4 {
		t.Errorf("the state is at version %d, want 4: each task recorded its failure", version)
	}
}

// TestASupplierAnotherNodeIsSyncingIsLeftToIt: while one node is in a round of a supplier, a job for it on another node
// ends without calling the supplier, and without failing.
func TestASupplierAnotherNodeIsSyncingIsLeftToIt(t *testing.T) {
	t.Parallel()
	release, inRound := make(chan struct{}), make(chan struct{}, 1)
	s := &system{account: func() (adapter.Account, error) {
		inRound <- struct{}{}
		<-release
		return adapter.Account{Balance: "1", Funding: adapter.FundingWallet}, nil
	}}
	c := newCluster(t, s)
	a, b := c.node("a", time.Hour), c.node("b", time.Hour)
	done := make(chan error, 1)
	go func() { done <- a.s.sync(t.Context(), c.supplier) }()
	<-inRound
	if err := b.s.sync(t.Context(), c.supplier); err != nil {
		t.Errorf("the second node's job: %v", err)
	}
	if got := s.opens.Load(); got != 1 {
		t.Errorf("the supplier was opened %d times, want only by the node that holds it", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Errorf("the first node's job: %v", err)
	}
	if err := b.s.sync(t.Context(), c.supplier); err != nil { // free again, but not due for a minute: nothing to read
		t.Fatal(err)
	}
	if got := s.accounts.Load(); got != 1 {
		t.Errorf("the balance was read %d times, want once", got)
	}
	if _, err := b.s.node.Acquire(t.Context(), c.supplier); err != nil {
		t.Errorf("the lease was not given back after the rounds: %v", err)
	}
}

// TestTheSweepEnqueuesOnlyWhatIsDueAndFree: a supplier never synced is enqueued; one that is not due, one a node holds,
// and one a person paused are not; the supplier that is enqueued is enqueued once however often the sweep runs.
func TestTheSweepEnqueuesOnlyWhatIsDueAndFree(t *testing.T) {
	t.Parallel()
	c := newCluster(t, &system{account: healthy()})
	n := c.node("a", time.Hour)
	// The queue is not started: its periodic sweep would run beside the three explicit ones below, and a supplier
	// added after that sweep looked would be enqueued by the background job before this test gives it its state.
	paused := c.addSupplier("paused.example", "paused")
	held, notDue := c.addSupplier("held.example", "active"), c.addSupplier("notdue.example", "active")
	err := n.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO suppliers.sync_states (supplier_id, next_due, lease_holder, lease_fence, lease_until)
			VALUES ($1, now() - interval '1 hour', 'other', 1, now() + interval '1 hour'),
			       ($2, now() + interval '1 hour', NULL, 1, NULL)`, held, notDue)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := n.s.sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.enqueued(n.db); len(got) != 1 || got[0] != c.supplier.String() {
		t.Errorf("jobs for suppliers %v, want one for %s, never for the held, the not-yet-due (%s, %s) or the paused (%s)",
			got, c.supplier, held, notDue, paused)
	}
}

// TestAJobFinishedDoesNotHoldUpTheNext: the unique job of a supplier covers it while it waits and while it runs, and
// does not outlive its run, or the supplier could not be enqueued for as long as the queue keeps finished jobs. The
// supplier is not due, so the start-up sweep leaves it alone and the test alone enqueues; the job is held in its round
// until the test lets it go.
func TestAJobFinishedDoesNotHoldUpTheNext(t *testing.T) {
	t.Parallel()
	release, inRound := make(chan struct{}), make(chan struct{}, 2)
	c := newCluster(t, &system{account: func() (adapter.Account, error) {
		inRound <- struct{}{}
		<-release
		return adapter.Account{Balance: "1", Funding: adapter.FundingWallet}, nil
	}})
	n := c.node("a", time.Hour)
	err := n.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "INSERT INTO suppliers.sync_states (supplier_id, next_due) VALUES ($1, now() + interval '1 hour')", c.supplier)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	c.start(n)
	enqueue := func() (inserted int) {
		err := n.db.Tx(t.Context(), platform, func(tx pg.Tx) (err error) {
			inserted, err = n.q.InsertManyTx(t.Context(), tx, []river.InsertManyParams{{Args: SyncArgs{Supplier: c.supplier.String()}}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return inserted
	}
	if enqueue() != 1 {
		t.Fatal("the first insert should be new")
	}
	<-inRound
	if enqueue() != 0 {
		t.Error("an insert while the job runs was not a duplicate")
	}
	close(release)
	c.waitFor("the job to finish", func() bool {
		var done int
		c.row("SELECT count(*) FROM river.river_job WHERE kind = 'suppliers.sync' AND args->>'supplier' = $1::text AND state = 'completed'", &done)
		return done == 1
	})
	if enqueue() != 1 {
		t.Error("a finished job kept the supplier from being enqueued again")
	}
}

// TestAQueuedJobOfASupplierNoLongerSyncedDoesNothing: a supplier paused, put in removal or stopped for attention after
// its job was queued is not called when the job runs: it takes the lease and gives it back, and reads nothing.
func TestAQueuedJobOfASupplierNoLongerSyncedDoesNothing(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"paused", "removing", "removed", "needs_attention"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			s := &system{account: healthy()}
			c := newCluster(t, s)
			n := c.node("a", time.Hour)
			err := n.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
				_, err := tx.Exec(t.Context(), "UPDATE suppliers.suppliers SET status = $2 WHERE id = $1", c.supplier, status)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := n.s.sync(t.Context(), c.supplier); err != nil {
				t.Fatal(err)
			}
			var version int64
			c.row("SELECT coalesce((SELECT version FROM suppliers.sync_states WHERE supplier_id = $1), 0)", &version)
			if s.opens.Load() != 0 || s.accounts.Load() != 0 || version != 0 {
				t.Errorf("a %s supplier was opened %d times, its balance read %d times, its state moved to version %d", status, s.opens.Load(), s.accounts.Load(), version)
			}
			if _, err := n.s.node.Acquire(t.Context(), c.supplier); err != nil {
				t.Errorf("the lease was not given back: %v", err)
			}
		})
	}
}

// TestARoundHeldUpByTheLimitsGoesOnOverTheSameAdapter: when the supplier's limits end a listing half way, the job waits
// for them to reopen and goes on with the adapter it has, whose listing remembers how far it got; a job that ended there
// would leave the next to open a new adapter and begin the listing again, and a listing longer than a window would never
// be finished.
func TestARoundHeldUpByTheLimitsGoesOnOverTheSameAdapter(t *testing.T) {
	t.Parallel()
	s := &system{account: healthy(), holdKeys: 300 * time.Millisecond}
	c := newCluster(t, s)
	if err := c.node("a", time.Hour).s.sync(t.Context(), c.supplier); err != nil {
		t.Fatal(err)
	}
	if s.opens.Load() != 1 || s.keysFinished.Load() != 1 {
		t.Errorf("the adapter was opened %d times and finished the listing %d times, want once and once", s.opens.Load(), s.keysFinished.Load())
	}
}

// TestALoginHeldUpByTheLimitsIsTriedAgainWhenTheyReopen: an Open that the supplier's limits refuse is not remembered as
// the round's failure: the job waits and opens again, and the round goes through.
func TestALoginHeldUpByTheLimitsIsTriedAgainWhenTheyReopen(t *testing.T) {
	t.Parallel()
	s := &system{account: healthy(), holdOpen: 300 * time.Millisecond}
	c := newCluster(t, s)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second) // a job that keeps the refusal waits here
	defer cancel()
	if err := c.node("a", time.Hour).s.sync(ctx, c.supplier); err != nil {
		t.Fatal(err)
	}
	if s.opens.Load() < 2 || s.accounts.Load() != 1 {
		t.Errorf("the adapter was opened %d times and the balance read %d times, want a second open and one read", s.opens.Load(), s.accounts.Load())
	}
}

// TestAJobHeldUpByTheLimitsEndsWhenItsTimeIsUp: a round that the limits keep holding up ends with the job's budget,
// even when the time they name has already passed, which would otherwise keep it going until the queue cancels it.
func TestAJobHeldUpByTheLimitsEndsWhenItsTimeIsUp(t *testing.T) {
	t.Parallel()
	s := &system{account: healthy(), stuckKeys: -time.Hour}
	c := newCluster(t, s)
	n := c.node("a", time.Hour)
	n.s.budget = 3 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := n.s.sync(ctx, c.supplier); err != nil {
		t.Fatalf("the job did not end within its budget: %v", err)
	}
	if s.keysCalls.Load() < 2 {
		t.Errorf("the listing was tried %d times, want it retried while the budget lasted", s.keysCalls.Load())
	}
}

// TestSuppliersThatCannotBeSyncedDoNotTakeTheSweepsPlace: a sweep enqueues the longest-overdue suppliers, a batch at a
// time. More of them than a batch, and than the jobs a node works at once (whose leases take them out of the list),
// that fail before they are called (their exit is one this node lacks) are backed off like any failure, so the sweeps
// after reach the supplier that is due later and works.
func TestSuppliersThatCannotBeSyncedDoNotTakeTheSweepsPlace(t *testing.T) {
	t.Parallel()
	s := &system{account: healthy()}
	c := newCluster(t, s)
	hosts := make([]string, sweepBatch+50)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("broken%d.example", i)
	}
	c.addSuppliers("active", "elsewhere", hosts...)
	err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO suppliers.sync_states (supplier_id, next_due)
			SELECT id, now() - interval '2 hours' FROM suppliers.suppliers WHERE mgmt_egress_id = 'elsewhere'`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), "INSERT INTO suppliers.sync_states (supplier_id, next_due) VALUES ($1, now() - interval '1 hour')", c.supplier)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	n := c.node("a", time.Hour)
	c.start(n)
	c.waitFor("the supplier that works to be synced", func() bool {
		if err := n.s.sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		var version int64
		c.row("SELECT version FROM suppliers.sync_states WHERE supplier_id = $1", &version)
		return version > 0
	})
}
