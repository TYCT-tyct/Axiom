package sub2api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// keyPageSize is how many keys one request lists. Every page is also one request of the panel bucket.
const keyPageSize = 100

// maxPages bounds a listing: a supplier whose pages never end would otherwise keep the task busy.
const maxPages = 100

type keyDTO struct {
	ID      number `json:"id"`
	Key     string `json:"key"`
	GroupID number `json:"group_id"`
	Status  string `json:"status"`
	Reset5h string `json:"reset_5h_at"`
	Reset1d string `json:"reset_1d_at"`
	Reset7d string `json:"reset_7d_at"`
}

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

// decodeKeys reads a position; a position this version does not read begins the listing again.
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

// keysPage reads the page the position points at into it. more is true when the page was read and the listing has
// pages after it; otherwise the page answers, either with the whole listing (Done) or with the position it reached when
// the supplier's limits stopped it.
func (m *manager) keysPage(ctx context.Context, p *keysProgress) (page adapter.Page[adapter.KeyState], more bool, err error) {
	if p.Page > maxPages {
		return page, false, fmt.Errorf("%w: more than %d pages of keys", adapter.ErrUnrecognised, maxPages)
	}
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

// keyIDs is the set of the keys read so far, which takes out the one a page repeats.
func keyIDs(keys []adapter.KeyState) map[number]bool {
	seen := make(map[number]bool, len(keys))
	for _, k := range keys {
		seen[number(k.Ref)] = true
	}
	return seen
}

// keyState is one key as the module knows it, without its text: a key spent now and then again reports the soonest
// moment a window of it resets.
func keyState(k keyDTO) adapter.KeyState {
	out := adapter.KeyState{Ref: adapter.KeyRef(k.ID), GroupRef: string(k.GroupID), Status: keyStatus(k.Status)}
	if out.Status == adapter.KeyQuotaExhausted {
		out.ResetAt = k.resetAt()
	}
	return out
}

// keyPage reads one page of keys: the items, how many pages the list has, and how many keys it holds.
func (m *manager) keyPage(ctx context.Context, p int) ([]keyDTO, int, int, error) {
	resp, err := m.get(ctx, "/api/v1/keys?page="+strconv.Itoa(p)+"&page_size="+strconv.Itoa(keyPageSize), bucketPanel)
	if err != nil {
		return nil, 0, 0, err
	}
	if resp.Status != http.StatusOK {
		return nil, 0, 0, statusError("keys", resp)
	}
	pg, err := open[page[keyDTO]](resp)
	if err != nil {
		return nil, 0, 0, err
	}
	items, pages, err := pg.checked()
	if err != nil {
		return nil, 0, 0, err
	}
	total, err := pg.count()
	if err != nil {
		return nil, 0, 0, err
	}
	for _, k := range items {
		if !k.ID.present() || k.Status == "" {
			return nil, 0, 0, fmt.Errorf("%w: a key without its id or status", adapter.ErrUnrecognised)
		}
	}
	return items, pages, total, nil
}

// keyStatus maps the supplier's status; one it does not know is not usable, which is the safe reading.
func keyStatus(s string) adapter.KeyStatus {
	switch s {
	case "active":
		return adapter.KeyActive
	case "quota_exhausted":
		return adapter.KeyQuotaExhausted
	case "expired":
		return adapter.KeyExpired
	default:
		return adapter.KeyDisabled
	}
}

// resetAt is the soonest moment a window of an exhausted key resets, zero if none is stated.
func (k keyDTO) resetAt() (soonest time.Time) {
	for _, text := range []string{k.Reset5h, k.Reset1d, k.Reset7d} {
		if t, err := time.Parse(time.RFC3339, text); err == nil && (soonest.IsZero() || t.Before(soonest)) {
			soonest = t
		}
	}
	return soonest
}
