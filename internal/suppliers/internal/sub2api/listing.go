package sub2api

import (
	"context"
	"errors"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// keysProgress is the position in the keys listing: the page to read next, the keys read so far, and the total the
// supplier stated. A key created during the listing shifts the pages, so one key may be read twice, which the position's
// keys take out. A listing that shrank (a key removed) moved its pages forward, which would skip a key, so it begins
// again: the keys read so far are given up, none is skipped.
type keysProgress struct {
	V     int                `json:"v"`
	Page  int                `json:"page"`
	Total int                `json:"total"`
	Keys  []adapter.KeyState `json:"keys"`
}

func decodeKeys(raw adapter.Listing) keysProgress {
	if len(raw) == 0 {
		return keysProgress{V: 1, Page: 1}
	}
	var p keysProgress
	if jsonx.UnmarshalKnown(raw, &p) != nil || p.V != 1 || p.Page < 1 {
		return keysProgress{V: 1, Page: 1}
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
	items, pages, total, err := m.keyPage(ctx, p.Page)
	if err != nil {
		if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
			return adapter.Page[adapter.KeyState]{Next: listing(*p), Until: blocked.Until}, false, nil
		}
		return page, false, err
	}
	if p.Page > 1 && total < p.Total { // the list shrank under the position: it begins again
		*p = keysProgress{V: 1, Page: 1}
		return page, true, nil
	}
	p.Total = total
	seen := keyIDs(p.Keys)
	for _, k := range items {
		if !seen[k.ID] {
			seen[k.ID] = true
			p.Keys = append(p.Keys, keyState(k))
		}
	}
	if p.Page >= pages || len(items) == 0 {
		return adapter.Page[adapter.KeyState]{Items: p.Keys, Done: true}, false, nil
	}
	p.Page++
	return page, true, nil
}
