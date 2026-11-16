package adapter

import (
	"errors"

	"axiom/internal/platform/jsonx"
)

// ListingOf encodes a position made of names, numbers and statuses.
func ListingOf(p any) Listing {
	b, err := jsonx.MarshalCanonical(p)
	if err != nil {
		panic(err)
	}
	return b
}

// Drain advances a listing until it finishes, is blocked, or fails.
func Drain[T any](step func() (Page[T], bool, error)) (Page[T], error) {
	for {
		page, more, err := step()
		if err != nil || !more {
			return page, err
		}
	}
}

// KeyProgress keeps the adapter's persisted key representation, which must not contain credentials.
// A shrinking list restarts at page one because offset pagination could otherwise skip a key.
type KeyProgress[K any] struct {
	Page  int `json:"page"`
	Total int `json:"total"`
	Keys  []K `json:"keys"`
}

// DecodeKeys restarts an invalid position rather than resuming at an unknown offset.
func DecodeKeys[K any](raw Listing) KeyProgress[K] {
	var p KeyProgress[K]
	if len(raw) == 0 || jsonx.UnmarshalKnown(raw, &p) != nil || p.Page < 1 {
		return KeyProgress[K]{Page: 1}
	}
	return p
}

// KeyStep keeps each adapter's page reader, end condition and stored fields while sharing resume and deduplication.
func KeyStep[K any](p *KeyProgress[K], read func() ([]KeyState, int, bool, error), listed func(KeyState) K, ref func(K) KeyRef, state func(K) KeyState) (page Page[KeyState], more bool, err error) {
	items, total, done, err := read()
	if err != nil {
		if blocked, ok := errors.AsType[Blocked](err); ok {
			return Page[KeyState]{Next: ListingOf(*p), Until: blocked.Until}, false, nil
		}
		return page, false, err
	}
	if p.Page > 1 && total < p.Total {
		*p = KeyProgress[K]{Page: 1}
		return page, true, nil
	}
	p.Total = total
	seen := make(map[KeyRef]bool, len(p.Keys))
	for _, k := range p.Keys {
		seen[ref(k)] = true
	}
	for _, k := range items {
		if !seen[k.Ref] {
			seen[k.Ref] = true
			p.Keys = append(p.Keys, listed(k))
		}
	}
	if done {
		out := make([]KeyState, len(p.Keys))
		for i, k := range p.Keys {
			out[i] = state(k)
		}
		return Page[KeyState]{Items: out, Done: true}, false, nil
	}
	p.Page++
	return page, true, nil
}
