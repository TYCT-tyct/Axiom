package suppliers

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/persist"
)

// Funds pairs one synced native balance with the supplier's live refusal state. Copies keep the balance they were
// built with, but a refusal stops all of them at once, including routes frozen before it happened.
type Funds struct {
	Balance platform.Exact
	state   *fundsState
}

type fundsState struct {
	mu          sync.Mutex // orders refusals, sync recovery and probe recovery
	version     uint64     // never reset: an older probe cannot clear a later refusal, even after an intervening recovery
	refusedAt   time.Time
	observedAt  time.Time // latest published balance observation; reloading it is not new recovery evidence
	rested      atomic.Bool
	pending     atomic.Bool
	lastRequest time.Time // guarded by Service.balanceMu
}

// CanPay compares a native cost bound to the snapshot's balance. A refusal overrides even a positive balance.
func (f *Funds) CanPay(cost platform.Exact) bool {
	return f.Balance.Cmp(cost) >= 0 && !f.resting()
}

func (f *Funds) resting() bool { return f.state != nil && f.state.rested.Load() }

func (s *Service) fundsOf(id uuid.UUID, balance platform.Exact, observed *time.Time) *Funds {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	rt := s.funds[id]
	if rt == nil {
		rt = &fundsState{}
		s.funds[id] = rt
	}
	rt.mu.Lock()
	if observed != nil && observed.After(rt.observedAt) {
		rt.observedAt = *observed
		if observed.After(rt.refusedAt) && balance.Cmp(platform.Exact{}) > 0 {
			rt.rested.Store(false)
		}
	}
	rt.mu.Unlock()
	return &Funds{Balance: balance, state: rt}
}

func (f *Funds) rest(now time.Time) {
	f.state.mu.Lock()
	defer f.state.mu.Unlock()
	f.state.version++
	f.state.refusedAt = now
	f.state.rested.Store(true)
	f.state.pending.Store(true)
}

// RefreshBalances hands coalesced refusal signals to the existing sync scheduler. Only the node's background loop
// calls it: Send marks memory, without a database write, a goroutine or an unbounded request queue. Failed writes keep
// their signal; successful ones are limited to one per supplier per minute, in addition to the sync engine's backoff.
func (s *Service) RefreshBalances(ctx context.Context) error {
	s.balanceMu.Lock()
	defer s.balanceMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s.runtimeMu.Lock()
	pending := make(map[uuid.UUID]*fundsState)
	for id, rt := range s.funds {
		if rt.pending.Load() && s.now().Sub(rt.lastRequest) >= time.Minute {
			pending[id] = rt
		}
	}
	s.runtimeMu.Unlock()
	for id, rt := range pending {
		rt.pending.Store(false)
		now := s.now()
		if err := s.cfg.DB.Tx(ctx, s.cfg.Scope, func(tx pg.Tx) error {
			return persist.RequestBalance(ctx, tx, id, now)
		}); err != nil {
			rt.pending.Store(true)
			return fmt.Errorf("request a supplier balance sync: %w", err)
		}
		rt.lastRequest = now
	}
	return nil
}
