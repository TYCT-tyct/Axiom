package accounts_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestMissesShareOneLookup: many requests with a key not yet known make one lookup between them. The key table is
// locked, so the lookup waits where it can be counted.
func TestMissesShareOneLookup(t *testing.T) {
	t.Parallel()
	_, database, s, key := callSetup(t)
	joins := observeLookupJoins(s)
	admin, release := lockTable(t, database, "accounts.api_keys")
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			_, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key)
			results <- err
		})
	}
	awaitJoins(t, joins, 20)
	awaitLookup(t, admin)
	if n := lookupsWaiting(t, admin); n != "1" {
		t.Errorf("%s lookups waiting, want 1", n)
	}
	release()
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Error(err)
		}
	}
}

// TestEachWaiterKeepsItsDeadline: a shared lookup does not end when the request that started it leaves, and a request
// waiting on it stops at its own deadline, while the others still get the answer.
func TestEachWaiterKeepsItsDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		finish := make(chan struct{})
		answer := errors.New("shared lookup finished")
		// Pause before database I/O so the waiters and the shared lookup use the same controlled clock.
		s, err := accounts.NewService(nil, accounts.Config{
			Hashes: hashKeys(t, 1),
			LookupSlot: func(ctx context.Context) (func(), error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-finish:
					return nil, answer
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		key := accounts.NewKeyText("sk-test-")
		authenticate := func(ctx context.Context) <-chan error {
			result := make(chan error, 1)
			go func() {
				_, err := s.AuthenticateKey(ctx, accounts.SiteID(s1), key)
				result <- err
			}()
			return result
		}

		first, leave := context.WithCancel(t.Context())
		started := authenticate(first)
		synctest.Wait()
		patient := authenticate(t.Context())
		hurried, cancel := context.WithTimeout(t.Context(), accounts.LookupTimeout/2)
		defer cancel()
		impatient := authenticate(hurried)
		synctest.Wait()
		leave()
		if err := <-started; !errors.Is(err, context.Canceled) {
			t.Errorf("the request that left: %v", err)
		}
		if err := <-impatient; !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the request whose deadline passed: %v", err)
		}
		close(finish)
		if err := <-patient; !errors.Is(err, answer) {
			t.Errorf("the request still waiting: %v, want %v", err, answer)
		}
	})
}

// TestALookupKeepsItsCallersDeadline: the shared lookup has the deadline of the request that started it, and
// LookupTimeout from its start when that request set none. The deadline is read where the lookup takes its slot, so
// the test compares values instead of racing two clocks.
func TestALookupKeepsItsCallersDeadline(t *testing.T) {
	t.Parallel()
	db, _, s, first := callSetup(t)
	second := createKey(t, db, s)
	var deadline time.Time
	var set bool
	gated, err := accounts.NewService(db, accounts.Config{
		Platform: platform, Hashes: hashKeys(t, 1),
		LookupSlot: func(ctx context.Context) (func(), error) {
			deadline, set = ctx.Deadline()
			return func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := gated.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	callers := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(t.Context(), callers)
	defer cancel()
	if _, err := gated.AuthenticateKey(ctx, accounts.SiteID(s1), first); err != nil {
		t.Fatal(err)
	}
	if !set || !deadline.Equal(callers) {
		t.Errorf("a caller with a deadline: the lookup's is %v (set %v), want %v", deadline, set, callers)
	}
	before := time.Now()
	if _, err := gated.AuthenticateKey(t.Context(), accounts.SiteID(s1), second); err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	if !set || deadline.Before(before.Add(accounts.LookupTimeout)) || deadline.After(after.Add(accounts.LookupTimeout)) {
		t.Errorf("a caller without one: the lookup's deadline is %v (set %v), want %v after its start", deadline, set, accounts.LookupTimeout)
	}
}

// observeLookupJoins makes each request that joins the shared key lookup signal on the returned channel.
func observeLookupJoins(s *accounts.Service) <-chan struct{} {
	joined := make(chan struct{}, 64)
	s.ObserveLookupJoins(func() { joined <- struct{}{} })
	return joined
}

// awaitJoins waits until want requests have joined the shared lookup: a target that never arrives fails the test
// instead of leaving the following assertions to a sleep.
func awaitJoins(t *testing.T, joined <-chan struct{}, want int) {
	t.Helper()
	for got := range want {
		select {
		case <-joined:
		case <-time.After(10 * time.Second):
			t.Fatalf("%d of %d requests joined the shared lookup", got, want)
		}
	}
}

// lockTable holds table locked from a superuser connection until release, which waits for the unlock. release is
// idempotent and runs as a cleanup before admin.Close, so a Fatal while waiting for a request still lets the locked
// transaction finish and the connection return.
func lockTable(t *testing.T, database pgtest.Database, table string) (*pg.DB, func()) {
	t.Helper()
	admin, err := pg.Open(t.Context(), database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	locked, unlock, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- admin.Tx(context.WithoutCancel(t.Context()), pg.Scope{}, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "LOCK TABLE "+table+" IN ACCESS EXCLUSIVE MODE"); err != nil {
				return err
			}
			close(locked)
			<-unlock
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-done:
		t.Fatalf("lock %s: %v", table, err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			close(unlock)
			if err := <-done; err != nil {
				t.Errorf("unlock %s: %v", table, err)
			}
		})
	}
	t.Cleanup(release)
	return admin, release
}

// awaitLookup waits until a key lookup waits for the lock, and fails when none does.
func awaitLookup(t *testing.T, admin *pg.DB) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); lookupsWaiting(t, admin) == "0" && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if n := lookupsWaiting(t, admin); n == "0" {
		t.Fatal("the key lookup did not reach the locked table")
	}
}

func lookupsWaiting(t *testing.T, admin *pg.DB) string {
	t.Helper()
	return texts(t, admin, pg.Scope{}, `SELECT count(*)::text FROM pg_stat_activity
		WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%key_tenant%'`)[0]
}
