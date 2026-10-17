// Package syncertest is an in-memory syncer.Store for the tests of the adapters that run through the engine. It fences
// like the real one: a commit that names an older version than the stored one is refused.
package syncertest

import (
	"context"
	"sync"

	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/syncer"
)

// Store keeps one supplier's state and everything committed to it.
type Store struct {
	mu      sync.Mutex
	state   syncer.State
	Events  []syncer.Event
	Plans   []syncer.Plan
	Charges map[adapter.ChargeRef]adapter.Charge
}

// New is an empty store.
func New() *Store { return &Store{Charges: map[adapter.ChargeRef]adapter.Charge{}} }

// Load implements syncer.Store. Like the real one, it reads whether a removal's alert was sent from the stored events:
// the state does not keep it (syncer.State.CatchUpNotified).
func (s *Store) Load(context.Context) (syncer.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	st.CatchUpNotified = false
	for _, e := range s.Events {
		if e.Kind == syncer.RemovalSlow {
			st.CatchUpNotified = true
		}
	}
	return st, nil
}

// Commit implements syncer.Store.
func (s *Store) Commit(_ context.Context, expect int64, c syncer.Commit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expect != s.state.Version {
		return syncer.ErrFenced
	}
	s.state = c.State
	s.Events, s.Plans = append(s.Events, c.Events...), append(s.Plans, c.Plans...)
	for _, ch := range c.Charges {
		s.Charges[ch.Ref] = ch
	}
	return nil
}

// State is the state last committed.
func (s *Store) State() syncer.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}
