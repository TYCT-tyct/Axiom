package sub2api

import (
	"context"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// listedKey keeps names across interrupted reads so the sync can adopt a key it made before the read stopped.
type listedKey struct {
	Ref       adapter.KeyRef    `json:"ref"`
	GroupRef  string            `json:"group_ref"`
	Name      string            `json:"name"`
	Status    adapter.KeyStatus `json:"status"`
	ResetAt   time.Time         `json:"reset_at,omitzero"`
	ExpiresAt *time.Time        `json:"expires_at,omitzero"`
}

func (k listedKey) state() adapter.KeyState {
	return adapter.KeyState{
		Ref: k.Ref, GroupRef: k.GroupRef, Name: k.Name, Status: k.Status, ResetAt: k.ResetAt,
		Policy: &adapter.KeyPolicy{ExpiresAt: k.ExpiresAt},
	}
}

func listed(k adapter.KeyState) listedKey {
	var expires *time.Time
	if k.Policy != nil {
		expires = k.Policy.ExpiresAt
	}
	return listedKey{Ref: k.Ref, GroupRef: k.GroupRef, Name: k.Name, Status: k.Status, ResetAt: k.ResetAt, ExpiresAt: expires}
}

type keysProgress = adapter.KeyProgress[listedKey]

// Keys lists our keys with their state, from the position in from, as far as the panel bucket allows. The list carries
// each key's text; it is used by the reads that need it and never kept, since a position goes into the supplier's
// state, which holds no credentials (manager).
func (m *manager) Keys(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	p := adapter.DecodeKeys[listedKey](from)
	return adapter.Drain(func() (adapter.Page[adapter.KeyState], bool, error) {
		return m.keysPage(ctx, &p)
	})
}

func (m *manager) keysPage(ctx context.Context, p *keysProgress) (adapter.Page[adapter.KeyState], bool, error) {
	return adapter.KeyStep(p, func() ([]adapter.KeyState, int, bool, error) {
		items, pages, total, err := m.resumeKeyPage(ctx, p)
		keys := make([]adapter.KeyState, len(items))
		for i, k := range items {
			keys[i] = keyState(k)
		}
		return keys, total, p.Page >= pages || len(items) == 0, err
	}, listed, func(k listedKey) adapter.KeyRef { return k.Ref }, listedKey.state)
}

// Charge scans predate durable listing metadata. They keep IDs but no total, so
// validate the preceding page before resuming without changing their stored format.
func (m *manager) resumeKeyPage(ctx context.Context, p *keysProgress) ([]keyDTO, int, int, error) {
	if p.Total >= 0 || p.Page == 1 {
		return m.keyPage(ctx, p.Page)
	}
	items, _, total, err := m.keyPage(ctx, p.Page-1)
	if err != nil {
		return nil, 0, 0, err
	}
	start := (p.Page - 2) * keyPageSize
	same := start+len(items) == len(p.Keys)
	if same {
		for i, k := range items {
			if string(k.ID) != string(p.Keys[start+i].Ref) {
				same = false
				break
			}
		}
	}
	if same {
		p.Total = total
	} else {
		*p = keysProgress{Page: 1}
	}
	return m.keyPage(ctx, p.Page)
}
