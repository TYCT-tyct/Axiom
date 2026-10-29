package suppliers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"axiom/internal/platform/kv"
)

// ShareRegistry is the background-only core membership boundary. ConfirmShares must reject a view whose membership
// changed since ShareView, and nil fences this process for rejoining after its local lease expires, retaining the last enforced view for peers.
type ShareRegistry interface {
	ShareID() string
	ShareView(context.Context) ([]kv.ShareNode, error)
	ConfirmShares(context.Context, []string) error
}

// SyncShares lowers local ceilings before acknowledging membership. New capacity is used only after every live core
// has published a view containing this process. Occupancy above the target drains rather than being cancelled.
func (s *Service) SyncShares(ctx context.Context) error {
	if s.cfg.Nodes == nil {
		return nil
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	asked := s.now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !s.shareUntil.IsZero() && !asked.Before(s.shareUntil) {
		if err := s.cfg.Nodes.ConfirmShares(ctx, nil); err != nil {
			return err
		}
	}
	own, ids, err := s.readShares(ctx, asked)
	if err != nil {
		return err
	}
	next := cloneSnapshot(s.snap.Load())
	s.applyShares(next)
	s.snap.Store(next)
	if (own.Joining || !slices.Equal(own.View, ids)) && s.shareDrained(ids) {
		if err := s.cfg.Nodes.ConfirmShares(ctx, ids); err != nil {
			if errors.Is(err, kv.ErrShareMembershipChanged) {
				return nil
			}
			return err
		}
		own.View, own.Joining = ids, false
		next = cloneSnapshot(next)
		s.applyShares(next)
		s.snap.Store(next)
	}
	return nil
}

func (s *Service) readShares(ctx context.Context, asked time.Time) (*kv.ShareNode, []string, error) {
	nodes, err := s.cfg.Nodes.ShareView(ctx)
	if err != nil {
		return nil, nil, err
	}
	ids := shareMembers(nodes)
	ownIndex := slices.IndexFunc(nodes, func(n kv.ShareNode) bool { return n.ID == s.cfg.Nodes.ShareID() && n.Remaining > 0 })
	if ownIndex < 0 {
		return nil, nil, fmt.Errorf("suppliers: this process is absent from the live core registry")
	}
	own := &nodes[ownIndex]
	s.shareNodes, s.shareUntil = nodes, asked.Add(min(kv.ShareLease, max(0, own.Remaining-5*time.Second)))
	return own, ids, nil
}

func cloneSnapshot(old *snapshot) *snapshot {
	next := *old
	next.endpoints = maps.Clone(old.endpoints)
	for id, ep := range old.endpoints {
		copyEP := *ep
		copyEP.creds = make([]*credential, len(ep.creds))
		for i, c := range ep.creds {
			copyCred := *c
			copyEP.creds[i] = &copyCred
		}
		next.endpoints[id] = &copyEP
	}
	return &next
}

// share assigns the indivisible remainder by stable node rank. Missing members have no capacity.
func share(limit int32, view []string, id string) int32 {
	rank := slices.Index(view, id)
	if rank < 0 {
		return 0
	}
	n := int64(len(view))
	value := int64(limit) / n
	if int64(rank) < int64(limit)%n {
		value++
	}
	return int32(value) //nolint:gosec // a partition is between zero and the original int32 ceiling
}

func (s *Service) localShare(limit int32, rpm bool) int32 {
	id := s.cfg.Nodes.ShareID()
	ids := shareMembers(s.shareNodes)
	value := share(limit, ids, id)
	for _, n := range s.shareNodes {
		if !rpm && n.Remaining <= 0 {
			continue
		}
		if n.ID == id && n.Remaining > 0 && n.Joining {
			return 0
		}
		view := n.View
		if view == nil {
			view = []string{n.ID}
		} else if len(view) == 0 {
			continue
		}
		value = min(value, share(limit, view, id))
	}
	return value
}

func (s *Service) applyShares(next *snapshot) {
	if s.cfg.Nodes == nil {
		return
	}
	next.until = s.shareUntil
	for _, ep := range next.endpoints {
		for _, c := range ep.creds {
			c.max = s.localShare(c.configuredMax, false)
			c.perMinute = c.configuredMinute
			if c.perMinute != 0 {
				c.perMinute = s.localShare(c.perMinute, true)
				if c.perMinute == 0 {
					c.perMinute = -1
				} // zero already means unlimited in the account window
			}
		}
	}
}

func (s *Service) shareDrained(ids []string) bool {
	id := s.cfg.Nodes.ShareID()
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	// A credential missing from the routable projection can still own calls and an account's old minute.
	for _, c := range s.runtime {
		rt := c.requests
		rt.mu.Lock()
		drained := c.inflight.Load() <= share(c.configuredMax, ids, id)
		if c.configuredMinute != 0 {
			drained = drained && rt.sum(rt.second(s.now())) <= int64(share(c.configuredMinute, ids, id))
		}
		rt.mu.Unlock()
		if !drained {
			return false
		}
	}
	return true
}

func shareMembers(nodes []kv.ShareNode) []string {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.Remaining > 0 {
			ids = append(ids, n.ID)
		}
	}
	return ids
}
