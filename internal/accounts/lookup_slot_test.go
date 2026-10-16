package accounts_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// slots counts the slots a service asks for and gives back.
type slots struct {
	mu          sync.Mutex
	taken, held int
	refuse      error
}

func (s *slots) take(context.Context) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse != nil {
		return nil, s.refuse
	}
	s.taken++
	s.held++
	return sync.OnceFunc(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.held--
	}), nil
}

func (s *slots) counts() (taken, held int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taken, s.held
}

// gatedNode is a node whose key lookups take their slot from s.
func gatedNode(t *testing.T, db *pg.DB, s *slots) *accounts.Service {
	t.Helper()
	svc, err := accounts.NewService(db, accounts.Config{Platform: platform, Hashes: hashKeys(t, 1), LookupSlot: s.take})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	return svc
}

// TestLookupHoldsOneSlotUntilItEnds: the many requests that share a lookup of a key take one slot between them, the
// lookup holds it though the request that started it has left, and gives it back when the lookup ends, not before: the
// slot stands for the database connection the lookup uses.
func TestLookupHoldsOneSlotUntilItEnds(t *testing.T) {
	t.Parallel()
	db, database, _, key := callSetup(t)
	var gate slots
	s := gatedNode(t, db, &gate)
	if !s.GatesLookups() {
		t.Error("a service given a slot does not say its lookups are gated")
	}
	admin, release := lockTable(t, database, "accounts.api_keys")
	first, leave := context.WithCancel(t.Context())
	results := make(chan error, 6)
	authenticate := func(ctx context.Context) {
		go func() {
			_, err := s.AuthenticateKey(ctx, accounts.SiteID(s1), key)
			results <- err
		}()
	}
	authenticate(first)
	awaitLookup(t, admin)
	for range 5 {
		authenticate(t.Context())
	}
	time.Sleep(100 * time.Millisecond) // time for the others to join the lookup
	leave()
	if err := <-results; !errors.Is(err, context.Canceled) {
		t.Errorf("the request that left: %v", err)
	}
	if taken, held := gate.counts(); taken != 1 || held != 1 {
		t.Errorf("while the lookup waits for the database: %d slots taken, %d held, want 1 and 1", taken, held)
	}
	release()
	for range 5 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if taken, held := gate.counts(); taken != 1 || held != 0 {
		t.Errorf("after the lookup: %d slots taken, %d held, want 1 and 0", taken, held)
	}
}

// TestLookupRefusedASlotTouchesNothing: when no slot is to be had the lookup does not run and nothing is remembered of
// the key, so a later request finds it.
func TestLookupRefusedASlotTouchesNothing(t *testing.T) {
	t.Parallel()
	db, _, _, key := callSetup(t)
	full := errors.New("no slot")
	gate := slots{refuse: full}
	s := gatedNode(t, db, &gate)
	if _, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key); !errors.Is(err, full) {
		t.Errorf("with no slot: %v, want the refusal", err)
	}
	gate.mu.Lock()
	gate.refuse = nil
	gate.mu.Unlock()
	if _, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key); err != nil {
		t.Errorf("once a slot is free: %v", err)
	}
}
