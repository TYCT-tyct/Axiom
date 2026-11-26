package sub2api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

var _ adapter.KeyFinder = (*manager)(nil)

// findPageSize is how many keys the search may answer in one page. The search matches the text inside a key's name or
// text whatever the case, so it can answer more than the key; a page that holds every match is what tells "no such key"
// from "not read yet".
const findPageSize = 20

// FindKey implements adapter.KeyFinder by sub2api's own list of the account's keys, which an ordinary user may search
// (handler/api_key_handler.go List, its `search` parameter cut to 100 characters; repository/api_key_repo.go matches
// it inside the name or the key with ContainsFold) and which answers each key's whole text (dto.APIKey's `key`). The
// key is the one whose text is the same whole; the others only contain it. When more keys match than one page holds and
// none of those is the key, the answer is not known.
func (m *manager) FindKey(ctx context.Context, text secret.Secret) (adapter.KeyState, bool, error) {
	query := url.Values{"search": {text.Reveal()}, "page": {"1"}, "page_size": {strconv.Itoa(findPageSize)}}
	resp, err := m.get(ctx, "/api/v1/keys?"+query.Encode(), bucketPanel)
	if err != nil {
		return adapter.KeyState{}, false, searchError(err)
	}
	if resp.Status != http.StatusOK {
		return adapter.KeyState{}, false, statusError("search the keys", resp)
	}
	// The answer holds keys' text, so an error reading it says only that it was not understood.
	pg, err := open[page[keyDTO]](resp)
	items, _, checked := pg.checked()
	total, counted := pg.count()
	if err != nil || checked != nil || counted != nil {
		return adapter.KeyState{}, false, fmt.Errorf("%w: the search's answer", adapter.ErrUnrecognised)
	}
	want := []byte(text.Reveal())
	for _, k := range items {
		if subtle.ConstantTimeCompare([]byte(k.Key), want) != 1 {
			continue
		}
		if !k.ID.present() || k.Status == "" {
			return adapter.KeyState{}, false, fmt.Errorf("%w: a key without its id or status", adapter.ErrUnrecognised)
		}
		return keyState(k), true, nil
	}
	if total > len(items) {
		return adapter.KeyState{}, false, fmt.Errorf("%w: more keys match the text than one page holds", adapter.ErrUnrecognised)
	}
	return adapter.KeyState{}, false, nil
}

// searchError is an error of the search's request with only the kind a caller acts on: the request's path holds the
// key's text, which the error of a request quotes.
func searchError(err error) error {
	var blocked adapter.Blocked
	switch {
	case errors.As(err, &blocked):
		return blocked
	case errors.Is(err, adapter.ErrBucketsFull):
		return adapter.ErrBucketsFull
	case errors.Is(err, adapter.ErrAuthRejected):
		return adapter.ErrAuthRejected
	}
	return errors.New("sub2api: the search of the keys was not answered")
}
