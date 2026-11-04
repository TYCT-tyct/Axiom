package sub2api

import (
	"context"
	"errors"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

const keyListingVersion = 3 // Version 2 stored adapter.KeyState, whose Name was not persisted.

// listedKey is one key read in a resumable listing. It is deliberately not adapter.KeyState: that hides Name from the
// stored state, which older nodes decode strictly, but a resumed listing needs the names of the pages already read so
// the sync can adopt a key it made before the read was interrupted.
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

// keysProgress is the position in the keys listing: the page to read next, the keys read so far, and the total the
// supplier stated. A key created during the listing shifts the pages, so one key may be read twice, which the position's
// keys take out. A listing that shrank (a key removed) moved its pages forward, which would skip a key, so it begins
// again: the keys read so far are given up, none is skipped.
type keysProgress struct {
	V     int         `json:"v"`
	Page  int         `json:"page"`
	Total int         `json:"total"`
	Keys  []listedKey `json:"keys"`
}

func decodeKeys(raw adapter.Listing) keysProgress {
	if len(raw) == 0 {
		return keysProgress{V: keyListingVersion, Page: 1}
	}
	var p keysProgress
	if jsonx.UnmarshalKnown(raw, &p) != nil || p.V != keyListingVersion || p.Page < 1 {
		return keysProgress{V: keyListingVersion, Page: 1}
	}
	return p
}

// Keys lists our keys with their state, from the position in from, as far as the panel bucket allows. The list carries
// each key's text; it is used by the reads that need it and never kept, since a position goes into the supplier's
// state, which holds no credentials (manager).
func (m *manager) Keys(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	p := decodeKeys(from)
	for {
		page, more, err := m.keysPage(ctx, &p)
		if err != nil || !more {
			return page, err
		}
	}
}

func (m *manager) keysPage(ctx context.Context, p *keysProgress) (page adapter.Page[adapter.KeyState], more bool, err error) {
	items, pages, total, err := m.resumeKeyPage(ctx, p)
	if err != nil {
		if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
			return adapter.Page[adapter.KeyState]{Next: listing(*p), Until: blocked.Until}, false, nil
		}
		return page, false, err
	}
	if p.Page > 1 && total < p.Total { // the list shrank under the position: it begins again
		*p = keysProgress{V: keyListingVersion, Page: 1}
		return page, true, nil
	}
	p.Total = total
	seen := keyIDs(p.Keys)
	for _, k := range items {
		if !seen[k.ID] {
			seen[k.ID] = true
			p.Keys = append(p.Keys, listed(keyState(k)))
		}
	}
	if p.Page >= pages || len(items) == 0 {
		return adapter.Page[adapter.KeyState]{Items: keyStates(p.Keys), Done: true}, false, nil
	}
	p.Page++
	return page, true, nil
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
		*p = keysProgress{V: keyListingVersion, Page: 1}
	}
	return m.keyPage(ctx, p.Page)
}
