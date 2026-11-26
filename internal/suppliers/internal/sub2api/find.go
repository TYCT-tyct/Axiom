package sub2api

import (
	"context"
	"crypto/subtle"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

var _ adapter.KeyFinder = (*manager)(nil)

// FindKey implements adapter.KeyFinder without sending the text to sub2api: its list of the account's keys can search
// by text, but only in the query string, which access logs keep. The list is read page by page instead, as the sync
// reads it (keyPage); it answers each key's whole text (dto.APIKey's `key`), and the key is the one whose text is the
// same whole, compared in constant time.
func (m *manager) FindKey(ctx context.Context, text secret.Secret) (adapter.KeyState, bool, error) {
	want := []byte(text.Reveal())
	for p := 1; ; p++ {
		items, pages, _, err := m.keyPage(ctx, p)
		if err != nil {
			return adapter.KeyState{}, false, err
		}
		for _, k := range items {
			if subtle.ConstantTimeCompare([]byte(k.Key), want) == 1 {
				return keyState(k), true, nil
			}
		}
		if p >= pages || len(items) == 0 {
			return adapter.KeyState{}, false, nil
		}
	}
}
