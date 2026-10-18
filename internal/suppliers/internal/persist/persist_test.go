package persist_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

var (
	t0       = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	platform = pg.NewScope(true, nil, nil)
	ttl      = time.Minute
)

// clock is a time the test moves, shared by the nodes of a test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// cluster is a database with one supplier and its login, reached by as many nodes as a test makes.
type cluster struct {
	t        *testing.T
	database pgtest.Database
	keys     *secret.Keyring
	clock    *clock
	supplier uuid.UUID
}

func newCluster(t *testing.T) *cluster {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(path, []byte("1:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := secret.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c := &cluster{t: t, database: pgtest.New(t, pg.Module{Name: "suppliers", Migrations: store.Migrations()}), keys: keys, clock: &clock{t: t0}}
	db := c.open()
	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		var err error
		if c.supplier, err = store.New().InsertSupplier(t.Context(), tx, store.InsertSupplierParams{
			OwnerScope: "platform", Name: "s", Kind: "fake", Origin: "https://fake.example", UpstreamAccount: "me",
			NativeUnit: "quota", SettlementCurrency: "USD", MgmtEgressID: "direct",
		}); err != nil {
			return err
		}
		sealed := keys.Seal(secret.New(`{"refresh":"one"}`), secret.Location{Table: "suppliers.supplier_logins", RowID: c.supplier.String(), Purpose: "login"})
		return store.New().InsertSupplierLogin(t.Context(), tx, store.InsertSupplierLoginParams{
			SupplierID: c.supplier, Kind: "tokens", Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK,
			KeyVersion: int64(sealed.KeyVersion),
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// open is a pool of its own, as a node has.
func (c *cluster) open() *pg.DB {
	c.t.Helper()
	db, err := pg.Open(c.t.Context(), c.database.App)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(db.Close)
	return db
}

func (c *cluster) node(holder string) *persist.Node {
	c.t.Helper()
	n, err := persist.New(persist.Config{DB: c.open(), Scope: platform, Keys: c.keys, Holder: holder, LeaseTTL: ttl, Now: c.clock.now})
	if err != nil {
		c.t.Fatal(err)
	}
	return n
}

func (c *cluster) acquire(n *persist.Node) *persist.Session {
	c.t.Helper()
	s, err := n.Acquire(c.t.Context(), c.supplier)
	if err != nil {
		c.t.Fatal(err)
	}
	return s
}

// count is the number of rows of table that belong to the supplier.
func (c *cluster) count(table string) (n int) {
	c.t.Helper()
	err := c.open().Tx(c.t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(c.t.Context(), "SELECT count(*) FROM suppliers."+table+" WHERE supplier_id = $1", c.supplier).Scan(&n)
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return n
}

// TestOnlyOneNodeHoldsALease: nodes that ask for one supplier's lease at once, each over its own connections, leave
// exactly one holder; the others are told it is held, which is no failure.
func TestOnlyOneNodeHoldsALease(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	const nodes = 8
	var wins, held atomic.Int32
	var wg sync.WaitGroup
	for i := range nodes {
		n := c.node(fmt.Sprintf("node-%d", i))
		wg.Go(func() {
			switch _, err := n.Acquire(t.Context(), c.supplier); {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, persist.ErrLeaseHeld):
				held.Add(1)
			default:
				t.Errorf("Acquire: %v", err)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 || held.Load() != nodes-1 {
		t.Errorf("%d nodes took the lease and %d were told it is held; want 1 and %d", wins.Load(), held.Load(), nodes-1)
	}
}

// stateAt is a state that differs from the one before, so that a commit of it can be told from none.
func stateAt(version int64) syncer.State {
	return syncer.State{Version: version, Balance: "42", BalanceAt: t0, Funding: adapter.FundingWallet}
}

// TestALeaseThatRanOutIsTakenOverAndTheOldHolderWritesNothing: a holder that stopped committing loses the lease after
// the time; the node that takes it over gets a higher fence; and the old holder's write, which names the old fence and
// the version the new holder has not yet written, leaves nothing: no state, no event, no plan, no login, and a
// supplier's balance as it was.
func TestALeaseThatRanOutIsTakenOverAndTheOldHolderWritesNothing(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	a, b := c.node("a"), c.node("b")
	old := c.acquire(a)
	if _, err := b.Acquire(t.Context(), c.supplier); !errors.Is(err, persist.ErrLeaseHeld) {
		t.Fatalf("a second node took a lease that was held: %v", err)
	}
	c.clock.advance(ttl + time.Second)
	fresh := c.acquire(b)

	commit := syncer.Commit{
		State:  stateAt(1),
		Events: []syncer.Event{{Kind: syncer.GroupAdded, Subject: "1"}},
		Plans:  []syncer.Plan{{Endpoint: "1/m", Fingerprint: "f", Schedule: `{"v":1}`, Basis: "published"}},
	}
	err := old.Store().Commit(t.Context(), 0, commit)
	if !errors.Is(err, syncer.ErrFenced) {
		t.Fatalf("the old holder's commit: %v, want ErrFenced", err)
	}
	if c.count("sync_events") != 0 || c.count("sync_plans") != 0 {
		t.Errorf("the refused commit left %d events and %d plans", c.count("sync_events"), c.count("sync_plans"))
	}
	if st, err := fresh.Store().Load(t.Context()); err != nil || st.Version != 0 {
		t.Fatalf("state after the refused commit: %+v, %v", st, err)
	}
	if err := old.Logins().Swap(t.Context(), 1, []byte(`{"refresh":"two"}`)); !errors.Is(err, adapter.ErrStaleLogin) {
		t.Errorf("the old holder's swap of the login: %v, want ErrStaleLogin", err)
	}

	if err := fresh.Store().Commit(t.Context(), 0, commit); err != nil {
		t.Fatal(err)
	}
	if c.count("sync_events") != 1 || c.count("sync_plans") != 1 {
		t.Errorf("the new holder's commit wrote %d events and %d plans, want 1 and 1", c.count("sync_events"), c.count("sync_plans"))
	}
	if err := old.Store().Commit(t.Context(), 1, commit); !errors.Is(err, syncer.ErrFenced) {
		t.Errorf("the old holder's commit over the new version: %v, want ErrFenced", err)
	}
}

// TestAListingPositionIsKeptForAnotherNodeAndOnlyInItsGeneration: the page a listing reached is stored beside the state,
// in a column of its own, so another node goes on from it while the state keeps the shape an older node reads; a change
// of the configuration (the credentials are replaced, or a pause) makes it stale, and the listing begins again.
func TestAListingPositionIsKeptForAnotherNodeAndOnlyInItsGeneration(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	kept := stateAt(1)
	kept.OfferAt, kept.KeysAt = adapter.Listing(`{"page":2}`), adapter.Listing(`{"page":3}`)
	if err := c.acquire(c.node("a")).Store().Commit(t.Context(), 0, syncer.Commit{State: kept}); err != nil {
		t.Fatal(err)
	}
	state, listing := c.stored()
	if strings.Contains(state, "OfferAt") || strings.Contains(state, "KeysAt") || !strings.Contains(listing, `"offer"`) || !strings.Contains(listing, `"keys"`) {
		t.Errorf("the state holds a listing position (%s), or the column does not hold it (%s)", state, listing)
	}

	// Another node reads the positions as the node before wrote them.
	c.clock.advance(ttl + time.Second)
	st, err := c.acquire(c.node("b")).Store().Load(t.Context())
	if err != nil || string(st.OfferAt) != `{"page":2}` || string(st.KeysAt) != `{"page":3}` {
		t.Fatalf("the position another node read: %q, %q, %v", st.OfferAt, st.KeysAt, err)
	}

	// A new configuration generation: the position belongs to a listing of an account or a run that is gone.
	if err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "UPDATE suppliers.suppliers SET config_gen = config_gen + 1 WHERE id = $1", c.supplier)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	c.clock.advance(ttl + time.Second)
	if st, err = c.acquire(c.node("c")).Store().Load(t.Context()); err != nil || st.OfferAt != nil || st.KeysAt != nil {
		t.Errorf("after the configuration changed: %q, %q, %v; want the positions given up", st.OfferAt, st.KeysAt, err)
	}
}

// stored is the supplier's state and its column of listing positions, as text.
func (c *cluster) stored() (state, listing string) {
	c.t.Helper()
	err := c.open().ReadTx(c.t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(c.t.Context(), "SELECT state::text, coalesce(listing::text, '') FROM suppliers.sync_states WHERE supplier_id = $1",
			c.supplier).Scan(&state, &listing)
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return state, listing
}

// TestACommitKeepsTheLeaseAndAReleaseGivesItUp: every commit lengthens the lease by its time, so a round longer than the
// time keeps it; a released lease can be taken at once, and a release by a holder that lost the lease takes nothing.
func TestACommitKeepsTheLeaseAndAReleaseGivesItUp(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	a, b := c.node("a"), c.node("b")
	s := c.acquire(a)
	c.clock.advance(ttl - time.Second)
	if err := s.Store().Commit(t.Context(), 0, syncer.Commit{State: stateAt(1)}); err != nil {
		t.Fatal(err)
	}
	c.clock.advance(ttl - time.Second) // past the first lease's time, within the lengthened one
	if _, err := b.Acquire(t.Context(), c.supplier); !errors.Is(err, persist.ErrLeaseHeld) {
		t.Fatalf("a lease kept by a commit was taken: %v", err)
	}
	if err := s.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	taken := c.acquire(b)
	if err := s.Release(t.Context()); err != nil { // a second release by the old holder: not its lease any more
		t.Fatal(err)
	}
	if _, err := c.node("c").Acquire(t.Context(), c.supplier); !errors.Is(err, persist.ErrLeaseHeld) {
		t.Errorf("the old holder's release gave up the lease of the node that took it: %v", err)
	}
	if err := taken.Store().Commit(t.Context(), 1, syncer.Commit{State: stateAt(2)}); err != nil {
		t.Errorf("the lease holder's commit: %v", err)
	}
}

// TestAStepIsStoredWholeOrNotAtAll: a step whose charge record the database refuses leaves no state, no event and no
// plan; the same step with a good record leaves all of them, the record, and the balance and status on the supplier.
func TestAStepIsStoredWholeOrNotAtAll(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	s := c.acquire(c.node("a"))
	step := syncer.Commit{
		State:   stateAt(1),
		Events:  []syncer.Event{{Kind: syncer.BalanceLow, New: "42"}, {Kind: syncer.GroupAdded, Subject: "1"}},
		Plans:   []syncer.Plan{{Endpoint: "1/m", Fingerprint: "f", Schedule: `{"v":1}`, Basis: "published"}},
		Charges: []adapter.Charge{{Ref: "client:1", Amount: "not a number", Funding: adapter.FundingWallet, OccurredAt: t0}},
	}
	if err := s.Store().Commit(t.Context(), 0, step); err == nil || errors.Is(err, syncer.ErrFenced) {
		t.Fatalf("a step with a charge the database refuses was stored: %v", err)
	}
	if c.count("sync_events")+c.count("sync_plans")+c.count("charge_records") != 0 {
		t.Error("the refused step left rows behind")
	}
	if st, err := s.Store().Load(t.Context()); err != nil || st.Version != 0 || !st.BalanceAt.IsZero() {
		t.Errorf("the refused step moved the state: %+v, %v", st, err)
	}

	step.Charges[0].Amount = "7"
	if err := s.Store().Commit(t.Context(), 0, step); err != nil {
		t.Fatal(err)
	}
	if got := [3]int{c.count("sync_events"), c.count("sync_plans"), c.count("charge_records")}; got != [3]int{2, 1, 1} {
		t.Errorf("events, plans and charges stored: %v, want [2 1 1]", got)
	}
	var balance string
	var status string
	err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT balance_native::text, status FROM suppliers.suppliers WHERE id = $1", c.supplier).Scan(&balance, &status)
	})
	if err != nil || balance != "42" || status != "active" {
		t.Errorf("the supplier after the step: balance %q, status %q, %v; want 42 and active", balance, status, err)
	}
}

// TestLoadDerivesTheRemovalAlert: whether the catch-up alert was sent is not in the state (an older node refuses a member
// it does not name), so Load reads it back from the removal_slow event the alert wrote. A removal that has not alerted
// loads with it clear.
func TestLoadDerivesTheRemovalAlert(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	s := c.acquire(c.node("a"))
	removal := func(version int64, alerted bool) syncer.Commit {
		st := stateAt(version)
		st.Removal, st.RemovalAt = syncer.StepFinalScan, t0
		commit := syncer.Commit{State: st}
		if alerted {
			commit.Events = []syncer.Event{{Kind: syncer.RemovalSlow, Why: "the charge records are still being read"}}
		}
		return commit
	}
	if err := s.Store().Commit(t.Context(), 0, removal(1, false)); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Store().Load(t.Context()); err != nil || st.CatchUpNotified {
		t.Fatalf("the state before the alert: %+v, %v; want it not alerted", st, err)
	}
	if err := s.Store().Commit(t.Context(), 1, removal(2, true)); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Store().Load(t.Context()); err != nil || !st.CatchUpNotified {
		t.Errorf("the state after the alert: %+v, %v; want it alerted", st, err)
	}
}

// TestALoginIsSwappedOnlyOverTheVersionReadInTheGenerationItWasReadIn: Load opens what was sealed; Swap stores a new
// login over the version read and raises it; a version already replaced, and a generation of the supplier's
// credentials that has moved on, are refused.
func TestALoginIsSwappedOnlyOverTheVersionReadInTheGenerationItWasReadIn(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	s := c.acquire(c.node("a"))
	logins := s.Logins()
	raw, version, err := logins.Load(t.Context())
	if err != nil || string(raw) != `{"refresh":"one"}` || version != 1 {
		t.Fatalf("Load: %q, %d, %v", raw, version, err)
	}
	if err := logins.Swap(t.Context(), version, []byte(`{"refresh":"two"}`)); err != nil {
		t.Fatal(err)
	}
	if err := logins.Swap(t.Context(), version, []byte(`{"refresh":"three"}`)); !errors.Is(err, adapter.ErrStaleLogin) {
		t.Errorf("a swap over a version already replaced: %v, want ErrStaleLogin", err)
	}
	if raw, version, err = logins.Load(t.Context()); err != nil || string(raw) != `{"refresh":"two"}` || version != 2 {
		t.Errorf("Load after the swap: %q, %d, %v", raw, version, err)
	}
	err = c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "UPDATE suppliers.suppliers SET config_gen = config_gen + 1 WHERE id = $1", c.supplier)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := logins.Swap(t.Context(), version, []byte(`{"refresh":"four"}`)); !errors.Is(err, adapter.ErrStaleLogin) {
		t.Errorf("a swap after the credentials' generation moved on: %v, want ErrStaleLogin", err)
	}
}

// pages is a Manager whose charge pass is four pages, each with a record of its own, that fails after failAfter pages
// as a node that crashes does. It records the position each call was given.
type pages struct {
	failAfter int
	given     []string
}

func (*pages) Account(context.Context) (adapter.Account, error) {
	return adapter.Account{Balance: "100", Funding: adapter.FundingWallet}, nil
}

func (*pages) Offer(context.Context, adapter.Listing) (adapter.Page[adapter.Group], error) {
	return adapter.Page[adapter.Group]{Done: true}, nil
}

func (*pages) Keys(context.Context, adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	return adapter.Page[adapter.KeyState]{Done: true}, nil
}

func (*pages) KeySecret(context.Context, adapter.KeyRef) (secret.Secret, error) {
	return secret.Secret{}, errors.New("not used")
}

func (m *pages) Charges(_ context.Context, scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	var pos struct{ Page int }
	if len(scan) > 0 {
		if err := jsonx.Unmarshal(scan, &pos); err != nil {
			return adapter.ChargePage{}, nil, err
		}
	}
	m.given = append(m.given, string(scan))
	if m.failAfter >= 0 && pos.Page >= m.failAfter {
		return adapter.ChargePage{}, nil, errors.New("the node died")
	}
	pos.Page++
	next, err := jsonx.MarshalCanonical(pos)
	return adapter.ChargePage{
		Charges: []adapter.Charge{{Ref: adapter.ChargeRef(fmt.Sprintf("client:%d", pos.Page)), Amount: "1", Funding: adapter.FundingWallet, OccurredAt: t0}},
		Done:    pos.Page == 4, Through: t0,
	}, next, err
}

// TestARestartedNodeGoesOnFromTheStoredPosition: a node that dies in the middle of a pass without giving its lease back
// leaves the pages it stored; when the lease has run out another node reads on from the position after the last stored
// page, not from the start, and the records of the whole pass are stored once each.
func TestARestartedNodeGoesOnFromTheStoredPosition(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	first := &pages{failAfter: 2}
	e := syncer.New(c.acquire(c.node("a")).Store(), first, syncer.Defaults(), c.clock.now)
	if err := e.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := c.count("charge_records"); got != 2 {
		t.Fatalf("pages stored before the node died: %d, want 2", got)
	}

	c.clock.advance(ttl + time.Second)
	second := &pages{failAfter: -1}
	s := c.acquire(c.node("b"))
	st, err := s.Store().Load(t.Context())
	if err != nil || !st.ScanOpen || len(st.Scan) == 0 {
		t.Fatalf("the state the second node found: scan open %v, position %q, %v", st.ScanOpen, st.Scan, err)
	}
	if err := syncer.New(s.Store(), second, syncer.Defaults(), c.clock.now).Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(second.given) != 2 || second.given[0] != string(st.Scan) {
		t.Errorf("the second node was given positions %q, want it to begin at %q and read 2 more pages", second.given, st.Scan)
	}
	if got := c.count("charge_records"); got != 4 {
		t.Errorf("records after the pass: %d, want 4, each once", got)
	}
}
