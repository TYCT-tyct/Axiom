package billing

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// bank is a test database with one account whose balance and floor the test chooses, and a billing whose holder is
// alive. Its quotes are made exact: input at one billionth a token, no output, so TextBytes is the amount in billionths.
type bank struct {
	t         testing.TB
	db        pgtest.Database
	app       *pg.DB
	platform  pg.Scope
	billing   *Billing
	admitted  Admitted
	supplies  *supplies
	exact     Quotable
	corrected correctionStats
}

// exactQuote is a route entry priced in billionths: a token of input 1, no output, so TextBytes is the amount in
// billionths. Each bank keeps its own: one package value whose price every newBank wrote was a race between the
// package's parallel tests.
func exactQuote(tb testing.TB) Quotable {
	tb.Helper()
	return Quotable{Price: mustSchedule(tb, `{"v":1,"currency":"USD","rates":{"input":"0.000000001","output":"0"}}`), Model: Model{ContextLength: 1e12}}
}

func newBank(t testing.TB, balance, floor int64, config Config) *bank {
	t.Helper()
	db := pgtest.New(t, Migrations())
	app, err := pg.Open(t.Context(), db.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	site, account := uuid.NewV7(), uuid.NewV7()
	b := &bank{t: t, db: db, app: app, platform: pg.NewScope(true, nil, nil), exact: exactQuote(t), admitted: Admitted{
		Scope: pg.NewScope(false, nil, []uuid.UUID{account}), Site: site, Account: account,
		Workspace: uuid.NewV7(), Key: uuid.NewV7(),
	}}
	if config == (Config{}) {
		config = Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour}
	}
	b.supplies = newSupplies()
	if b.billing, err = Start(t.Context(), app, b.platform, config, b.supplies); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.billing.conn.Close(t.Context()) })
	if err := app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		return OpenBalance(t.Context(), tx, site, account, floor)
	}); err != nil {
		t.Fatal(err)
	}
	if balance > 0 {
		b.credit(balance)
	}
	return b
}

// credit adds amount to the account as a gift, through the ledger, in the platform scope.
func (b *bank) credit(amount int64) { b.creditBatch(amount, nil) }

// sql runs statements as the superuser, for what the application may not do, such as crediting a balance.
func (b *bank) sql(statements ...string) {
	b.t.Helper()
	if err := pgtest.Run(b.t.Context(), b.db.Superuser, statements...); err != nil {
		b.t.Fatal(err)
	}
}

func (b *bank) count(query string) int64 {
	b.t.Helper()
	var n int64
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error { return tx.QueryRow(b.t.Context(), query).Scan(&n) }); err != nil {
		b.t.Fatal(err)
	}
	return n
}

// reserve reserves amount billionths for a new request in a transaction of its own.
func (b *bank) reserve(a Admitted, amount int64) (*Hold, error) {
	var h *Hold
	err := b.app.Tx(b.t.Context(), a.Scope, func(tx pg.Tx) (err error) {
		h, _, err = b.billing.Reserve(b.t.Context(), tx, a, uuid.NewV7(), Demand{TextBytes: amount}, []Quotable{b.exact})
		return err
	})
	return h, err
}

// consistent checks the counters against the reservations: the balance row's reserved and each window's reserved
// are the sums of the pending amounts, and the balance never passes its floor.
func (b *bank) consistent() {
	b.t.Helper()
	// What a reservation still holds: all of a pending one, since only settlement or cleanup ends it; and what it
	// charged, which its windows have spent.
	const held = `CASE r.state WHEN 'pending' THEN r.amount ELSE 0 END`
	const window = `((r.workspace_id, r.workspace_window) = (s.subject, s.window_start)
		OR (r.key_id, r.key_window) = (s.subject, s.window_start))`
	for name, query := range map[string]string{
		"balance": `SELECT b.reserved - coalesce((SELECT sum(` + held + `) FROM billing.reservations r), 0) FROM billing.balances b`,
		"windows": `SELECT count(*) FROM billing.spend s WHERE s.reserved <> coalesce((SELECT sum(` + held + `)
			FROM billing.reservations r WHERE ` + window + `), 0)
			OR s.spent <> coalesce((SELECT sum(r.charged) FROM billing.reservations r WHERE ` + window + `), 0)`,
		"floor": `SELECT count(*) FROM billing.balances WHERE balance - frozen - reserved < floor`,
	} {
		if n := b.count(query); n != 0 {
			b.t.Errorf("%s inconsistent: %d", name, n)
		}
	}
}

// TestReserveStorm: many reservations at once never take the account past its floor, and each refusal was for an
// amount above what was left even after every reservation that succeeded.
func TestReserveStorm(t *testing.T) {
	daily := &Limit{Amount: 1500, Reset: ResetDaily}
	for _, tc := range []struct {
		name     string
		limit    *Limit
		capacity int64
		source   Source
	}{
		{"balance 1000 and floor -200", nil, 1200, SourceBalance},
		{"a daily key limit of 1500 above a capacity of 3000", daily, 1500, SourceKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBank(t, 1000+1800*boolInt(tc.limit != nil), -200, Config{})
			a := b.admitted
			a.KeyLimit = tc.limit
			total, refused := b.storm(a, tc.source)
			if total > tc.capacity || len(refused) == 0 {
				t.Fatalf("reserved %d of %d with %d refusals", total, tc.capacity, len(refused))
			}
			for _, n := range refused {
				if n <= tc.capacity-total {
					t.Errorf("refused %d with %d left", n, tc.capacity-total)
				}
			}
			b.consistent()
		})
	}
}

// storm reserves 40 random amounts at once and returns the total reserved and the amounts source refused.
func (b *bank) storm(a Admitted, source Source) (total int64, refused []int64) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range 40 {
		amount := rand.Int64N(100) + 1 + int64(i%2)*50 //nolint:gosec // test data
		wg.Go(func() {
			_, err := b.reserve(a, amount)
			mu.Lock()
			defer mu.Unlock()
			if refusedBy(b.t, err, source) {
				refused = append(refused, amount)
			} else {
				total += amount
			}
		})
	}
	wg.Wait()
	return total, refused
}

// refusedBy reports whether err is a refusal by source, and fails the test for any other error.
func refusedBy(t testing.TB, err error, source Source) bool {
	var short *Insufficient
	if err != nil && (!errors.As(err, &short) || short.Source != source) {
		t.Errorf("Reserve: %v", err)
	}
	return err != nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// TestReserveSeesCommitsBeforeItsLock: B starts while A holds the account's lock; once A commits, B's counters
// include A's reservation, read by a statement that starts after B's lock is granted, so the key limit holds.
func TestReserveSeesCommitsBeforeItsLock(t *testing.T) {
	b := newBank(t, 1e6, 0, Config{})
	a := b.admitted
	a.KeyLimit = &Limit{Amount: 1000, Reset: ResetNever}
	release, first := b.holdOpen(a, uuid.NewV7(), 700)
	second := make(chan error, 1)
	go func() { _, err := b.reserve(a, 700); second <- err }()
	b.waitForLockWait()
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	var short *Insufficient
	if err := <-second; !errors.As(err, &short) || short.Source != SourceKey || short.Transient {
		t.Fatalf("B: %v, want the key limit short, not transient", err)
	}
	b.consistent()
}

// holdOpen reserves amount for request id in a transaction it keeps open, holding the account's lock, until release
// is closed; done reports how the transaction ended.
func (b *bank) holdOpen(a Admitted, id uuid.UUID, amount int64) (release chan struct{}, done chan error) {
	b.t.Helper()
	release, done, held := make(chan struct{}), make(chan error, 1), make(chan struct{})
	go func() {
		done <- b.app.Tx(b.t.Context(), a.Scope, func(tx pg.Tx) error {
			_, _, err := b.billing.Reserve(b.t.Context(), tx, a, id, Demand{TextBytes: amount}, []Quotable{b.exact})
			close(held)
			<-release
			return err
		})
	}()
	<-held
	return release, done
}

// waitFor returns once query reads want; waitForLockWait once a backend waits on a lock.
func (b *bank) waitFor(query string, want int64) {
	b.t.Helper()
	for b.count(query) != want {
		time.Sleep(time.Millisecond)
	}
}

func (b *bank) waitForLockWait() {
	b.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for b.superCount("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'") == 0 {
		if time.Now().After(deadline) {
			b.t.Fatal("nothing waits on a lock")
		}
		time.Sleep(time.Millisecond)
	}
}

func (b *bank) superCount(query string) int64 {
	b.t.Helper()
	super, err := pg.Open(b.t.Context(), b.db.Superuser)
	if err != nil {
		b.t.Fatal(err)
	}
	defer super.Close()
	var n int64
	if err := super.Tx(b.t.Context(), pg.Scope{}, func(tx pg.Tx) error { return tx.QueryRow(b.t.Context(), query).Scan(&n) }); err != nil {
		b.t.Fatal(err)
	}
	return n
}

// TestExtend: an extension grows the amount and the counters by the difference to its target, once per segment
// number; a target below the amount keeps it; an unaffordable one is refused and changes nothing; and an extension of a
// released reservation is terminated.
func TestExtend(t *testing.T) {
	b := newBank(t, 1000, 0, Config{})
	ctx := t.Context()
	h, err := b.reserve(b.admitted, 100)
	if err != nil {
		t.Fatal(err)
	}
	amount := func() int64 {
		return b.count(fmt.Sprintf("SELECT amount FROM billing.reservations WHERE request_id = '%s'", h.id))
	}
	for _, step := range []struct {
		name   string
		seq    int
		target int64
		want   int64
		err    func(error) bool
	}{
		{"segment 2", 2, 300, 300, nil},
		{"segment 2 again, as after a lost acknowledgement", 2, 300, 300, nil},
		{"segment 3 below the amount", 3, 200, 300, nil},
		{"segment 4 past the balance", 4, 1001, 300, func(err error) bool {
			s, ok := errors.AsType[*Insufficient](err)
			return ok && s.Source == SourceBalance
		}},
		{"segment 4 at the balance", 4, 1000, 1000, nil},
	} {
		err := b.billing.Extend(ctx, h, ExtendRequest{Seq: step.seq, Target: platform.AmountExact(step.target)})
		if (step.err == nil) != (err == nil) || step.err != nil && !step.err(err) || amount() != step.want {
			t.Errorf("%s: Extend = %v, amount %d; want %d", step.name, err, amount(), step.want)
		}
		b.consistent()
	}
	if err := b.billing.SettleOrphan(ctx, b.admitted, h.id); err != nil {
		t.Fatal(err)
	}
	if err := b.billing.Extend(ctx, h, ExtendRequest{Seq: 5, Target: platform.AmountExact(1000)}); !errors.Is(err, ErrTerminated) {
		t.Errorf("after release: Extend = %v, want ErrTerminated", err)
	}
	b.consistent()
}

// TestExtendRace: extensions of several requests race for what is left, and the same segment is sent twice at once;
// the account never passes its floor and each segment counts once.
func TestExtendRace(t *testing.T) {
	b := newBank(t, 1000, 0, Config{})
	var holds []*Hold
	for range 5 {
		h, err := b.reserve(b.admitted, 100)
		if err != nil {
			t.Fatal(err)
		}
		holds = append(holds, h)
	}
	var wg sync.WaitGroup
	for _, h := range holds {
		for range 2 {
			wg.Go(func() {
				err := b.billing.Extend(t.Context(), h, ExtendRequest{Seq: 2, Target: platform.AmountExact(250)})
				var short *Insufficient
				if err != nil && !errors.As(err, &short) {
					t.Error(err)
				}
			})
		}
	}
	wg.Wait()
	if extended := b.count("SELECT count(*) FROM billing.reservations WHERE segment = 2"); extended != 3 {
		t.Errorf("%d extensions of 150 fit in 500, want 3", extended)
	}
	b.consistent()
}

// TestSettleOrphan*: whatever became of an admission whose COMMIT went unacknowledged, its reservation ends released:
// committed, it is released; rolled back, a tombstone keeps the request ID; still open, the orphan waits for it.
func TestSettleOrphanCommitted(t *testing.T) {
	ctx := t.Context()
	b := newBank(t, 1000, 0, Config{})
	h, err := b.reserve(b.admitted, 400)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // a retry finds it released and does nothing
		if err := b.billing.SettleOrphan(ctx, b.admitted, h.id); err != nil {
			t.Fatal(err)
		}
	}
	if n := b.count("SELECT reserved FROM billing.balances"); n != 0 {
		t.Errorf("reserved %d after the orphan, want 0", n)
	}
	b.consistent()
}

func TestSettleOrphanRolledBack(t *testing.T) {
	ctx := t.Context()
	b := newBank(t, 1000, 0, Config{})
	id := uuid.NewV7()
	if err := b.billing.SettleOrphan(ctx, b.admitted, id); err != nil {
		t.Fatal(err)
	}
	err := b.app.Tx(ctx, b.admitted.Scope, func(tx pg.Tx) error {
		_, _, err := b.billing.Reserve(ctx, tx, b.admitted, id, Demand{TextBytes: 1}, []Quotable{b.exact})
		return err
	})
	if err == nil || b.count("SELECT count(*) FROM billing.reservations WHERE state = 'released' AND segment = 0") != 1 {
		t.Errorf("an admission after the tombstone: %v; want it refused by the tombstone", err)
	}
}

func TestSettleOrphanStillOpen(t *testing.T) {
	ctx := t.Context()
	b := newBank(t, 1000, 0, Config{})
	id := uuid.NewV7()
	release, admitted := b.holdOpen(b.admitted, id, 400)
	orphan := make(chan error, 1)
	go func() { orphan <- b.billing.SettleOrphan(ctx, b.admitted, id) }()
	b.waitForLockWait()
	close(release)
	if err := <-admitted; err != nil {
		t.Fatal(err)
	}
	if err := <-orphan; err != nil {
		t.Fatal(err)
	}
	if n := b.count("SELECT count(*) FROM billing.reservations WHERE state = 'released' AND amount = 400"); n != 1 {
		t.Errorf("the admission's reservation was not released")
	}
	b.consistent()
}

// TestReap: a holder reaped while a write it fenced is in flight waits for that write, which commits and stays for
// cleanup; after the reap, its reservations and extensions are refused and its heartbeat reports ErrReaped. The holder
// is expired by hand rather than by a short TTL: the TTL also sets the heartbeat, and the reap gets half a beat to wait
// for the write, so a TTL short enough to expire quickly would leave too little of it to observe the wait. The update
// touches no key column, so it does not conflict with the write's fence.
func TestReap(t *testing.T) {
	ctx := t.Context()
	b := newBank(t, 1000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Millisecond, Lifetime: time.Hour})
	h, err := b.reserve(b.admitted, 100)
	if err != nil {
		t.Fatal(err)
	}
	release, written := b.holdOpen(b.admitted, uuid.NewV7(), 100)
	b.sql("UPDATE billing.holders SET expires_at = clock_timestamp() - interval '1 second' WHERE id = '" + b.billing.holder.String() + "'")
	reaped := make(chan int, 1)
	go func() {
		n, err := b.billing.Reap(ctx)
		if err != nil {
			t.Error(err)
		}
		reaped <- n
	}()
	b.waitForLockWait()
	close(release)
	if err := <-written; err != nil {
		t.Fatalf("the write in flight: %v", err)
	}
	if n := <-reaped; n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	if n := b.count("SELECT count(*) FROM billing.reservations WHERE state = 'pending'"); n != 2 {
		t.Errorf("%d pending reservations left for cleanup, want 2", n)
	}
	if _, err := b.reserve(b.admitted, 1); !errors.Is(err, ErrTerminated) {
		t.Errorf("Reserve after the reap: %v, want ErrTerminated", err)
	}
	if err := b.billing.Extend(ctx, h, ExtendRequest{Seq: 2, Target: platform.AmountExact(200)}); !errors.Is(err, ErrTerminated) {
		t.Errorf("Extend after the reap: %v, want ErrTerminated", err)
	}
	if err := b.billing.Heartbeat(ctx); !errors.Is(err, ErrReaped) {
		t.Errorf("Heartbeat after the reap: %v, want ErrReaped", err)
	}
	// A reaped holder stays fenced even if its expiry lies ahead again, as after the database's clock goes back.
	b.sql("UPDATE billing.holders SET expires_at = clock_timestamp() + interval '1 hour'")
	if _, err := b.reserve(b.admitted, 1); !errors.Is(err, ErrTerminated) {
		t.Errorf("Reserve by a reaped holder unexpired: %v, want ErrTerminated", err)
	}
}

// TestHeartbeat: a heartbeat keeps a holder whose time ran out alive while it is not reaped, so a database outage
// shorter than the grace costs nothing.
func TestHeartbeat(t *testing.T) {
	b := newBank(t, 1000, 0, Config{HolderTTL: time.Second, ReapGrace: time.Hour, Lifetime: time.Hour})
	b.waitFor("SELECT count(*) FROM billing.holders WHERE expires_at < clock_timestamp()", 1)
	if _, err := b.reserve(b.admitted, 1); !errors.Is(err, ErrTerminated) {
		t.Fatalf("Reserve with the holder expired: %v, want ErrTerminated", err)
	}
	if err := b.billing.Heartbeat(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.reserve(b.admitted, 1); err != nil {
		t.Errorf("Reserve after the heartbeat: %v", err)
	}
}

// TestHolderPolicy: an account's transaction locks a holder row for its fence but cannot change it.
func TestHolderPolicy(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		if err := b.billing.fence(t.Context(), tx); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), "UPDATE billing.holders SET reaped_at = clock_timestamp()")
		return err
	})
	if err == nil {
		t.Error("an account's transaction updated a holder")
	}
}
