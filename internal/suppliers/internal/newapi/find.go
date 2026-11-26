package newapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

var _ adapter.KeyFinder = (*manager)(nil)

// maxFound is how many keys a search by text may answer before it is taken as not an answer: the search matches with
// LIKE, so a text with a wildcard in it matches more than its own key, and each candidate costs a read of its text.
const maxFound = 5

// FindKey implements adapter.KeyFinder by new-api's search of the account's keys by their text (controller/token.go
// SearchTokens, its `token` parameter; model/token.go SearchUserTokens strips "sk-" and matches with LIKE), then reads
// the text of each key it answers and keeps the one that is the same whole. The search answers masked keys, so the
// read is what confirms it.
func (m *manager) FindKey(ctx context.Context, text secret.Secret) (adapter.KeyState, bool, error) {
	plain := strings.TrimPrefix(text.Reveal(), "sk-")
	query := url.Values{"token": {plain}, "page_size": {strconv.Itoa(maxFound + 1)}}
	items, err := m.searchKeys(ctx, "/api/token/search?"+query.Encode())
	if err != nil {
		return adapter.KeyState{}, false, err
	}
	if len(items) > maxFound {
		return adapter.KeyState{}, false, fmt.Errorf("%w: more than %d keys match the text", adapter.ErrUnrecognised, maxFound)
	}
	want := []byte("sk-" + plain)
	for _, item := range items {
		status, ok := keyStatus[item.Status]
		if !ok || item.ID <= 0 {
			return adapter.KeyState{}, false, fmt.Errorf("%w: a key with status %d", adapter.ErrUnrecognised, item.Status)
		}
		ref := adapter.KeyRef(strconv.FormatInt(item.ID, 10))
		found, err := m.KeySecret(ctx, ref)
		if err != nil {
			return adapter.KeyState{}, false, err
		}
		if subtle.ConstantTimeCompare([]byte(found.Reveal()), want) == 1 {
			return adapter.KeyState{Ref: ref, GroupRef: item.Group, Name: item.Name, Status: status}, true, nil
		}
	}
	return adapter.KeyState{}, false, nil
}

// foundKey is one key a search answered.
type foundKey struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Group  string `json:"group"`
	Status int    `json:"status"`
}

// searchKeys sends the search. Its path holds the key's text, which an error of the request could quote, so an error
// passes on only the kinds a caller acts on and never the request's own words.
func (m *manager) searchKeys(ctx context.Context, path string) ([]foundKey, error) {
	resp, err := m.api.send(ctx, http.MethodGet, path, global, nil)
	if err != nil {
		return nil, searchError(err)
	}
	return foundKeys(resp)
}

// searchError is an error of the search's request with only the kind a caller acts on.
func searchError(err error) error {
	var blocked adapter.Blocked
	switch {
	case errors.As(err, &blocked):
		return blocked
	case errors.Is(err, adapter.ErrBucketsFull):
		return adapter.ErrBucketsFull
	}
	return errors.New("new-api: the search of the keys was not answered")
}

// foundKeys reads the search's answer.
func foundKeys(resp adapter.Response) ([]foundKey, error) {
	var env envelope
	parsed := jsonx.Unmarshal(resp.Body, &env) == nil
	switch {
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		return nil, authError(env, parsed)
	case resp.Status >= http.StatusInternalServerError:
		return nil, fmt.Errorf("%w: status %d", errServer, resp.Status)
	case resp.Status != http.StatusOK || !parsed || !env.Success:
		return nil, fmt.Errorf("%w: search the keys: status %d", adapter.ErrUnrecognised, resp.Status)
	}
	var found struct {
		Data *struct {
			Items []foundKey `json:"items"`
		} `json:"data"`
	}
	if err := jsonx.Unmarshal(resp.Body, &found); err != nil || found.Data == nil || found.Data.Items == nil {
		return nil, errMissing("list of keys that match")
	}
	return found.Data.Items, nil
}
