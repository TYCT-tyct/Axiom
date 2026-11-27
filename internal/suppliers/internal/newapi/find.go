package newapi

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

var _ adapter.KeyFinder = (*manager)(nil)

// maxFound is how many of the account's keys may share the text's mask before the answer is taken as not one: each is
// read whole in the critical bucket, which allows five reads in twenty minutes. Two keys share a mask about once in 62⁸.
const maxFound = 5

// FindKey implements adapter.KeyFinder without sending the text, or any part of it, to new-api: its search takes the
// text in the query string, and new-api logs every request's path and query (middleware/logger.go). The account's own
// listing is read instead, page by page (controller/token.go GetAllTokens); it masks each key (maskKey), so a key whose
// mask is the text's is a candidate, and reading its text (KeySecret) confirms it whole, compared in constant time.
func (m *manager) FindKey(ctx context.Context, text secret.Secret) (adapter.KeyState, bool, error) {
	plain := strings.TrimPrefix(text.Reveal(), "sk-")
	candidates, err := m.masked(ctx, []byte(maskKey(plain)))
	if err != nil {
		return adapter.KeyState{}, false, err
	}
	want := []byte("sk-" + plain)
	for _, c := range candidates {
		found, err := m.KeySecret(ctx, c.Ref)
		if err != nil {
			return adapter.KeyState{}, false, err
		}
		if subtle.ConstantTimeCompare([]byte(found.Reveal()), want) == 1 {
			return c, true, nil
		}
	}
	return adapter.KeyState{}, false, nil
}

// masked is every key of the account's listing whose mask is mask; more than maxFound is no answer.
func (m *manager) masked(ctx context.Context, mask []byte) ([]adapter.KeyState, error) {
	var candidates []adapter.KeyState
	for page := 1; ; page++ {
		if page > maxPages {
			return nil, fmt.Errorf("%w: more than %d pages of keys", adapter.ErrUnrecognised, maxPages)
		}
		tokens, _, err := m.keyPage(ctx, page)
		if err != nil {
			return nil, err
		}
		for _, t := range tokens {
			if subtle.ConstantTimeCompare([]byte(t.masked), mask) == 1 {
				candidates = append(candidates, t.state)
			}
		}
		if len(tokens) < m.keyPageSize() {
			break
		}
	}
	if len(candidates) > maxFound {
		return nil, fmt.Errorf("%w: more than %d keys share the text's mask", adapter.ErrUnrecognised, maxFound)
	}
	return candidates, nil
}

// maskKey is new-api's MaskTokenKey (model/token.go), which its listing shows each key as: the key without "sk-", its
// first and last four characters around ten stars, or less of it when it is short.
func maskKey(key string) string {
	switch {
	case key == "":
		return ""
	case len(key) <= 4:
		return strings.Repeat("*", len(key))
	case len(key) <= 8:
		return key[:2] + "****" + key[len(key)-2:]
	}
	return key[:4] + "**********" + key[len(key)-4:]
}
