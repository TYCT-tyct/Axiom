package newapi

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

var _ adapter.KeyMaker = (*manager)(nil)

// CreateKey implements adapter.KeyMaker: a token with no quota, expiry or model limit, in the group the sync names.
// new-api answers with no id, so the key is found by the name the sync gave it.
func (m *manager) CreateKey(ctx context.Context, groupRef, name string) (adapter.KeyRef, error) {
	body, err := jsonx.MarshalCanonical(struct {
		Name               string `json:"name"`
		Group              string `json:"group"`
		UnlimitedQuota     bool   `json:"unlimited_quota"`
		ModelLimitsEnabled bool   `json:"model_limits_enabled"`
		ExpiredTime        int    `json:"expired_time"`
	}{Name: name, Group: groupRef, UnlimitedQuota: true, ModelLimitsEnabled: false, ExpiredTime: -1})
	if err != nil {
		return "", fmt.Errorf("new-api: encode the key: %w", err)
	}
	resp, err := m.api.send(ctx, http.MethodPost, "/api/token/", global, body)
	if err != nil {
		return "", err
	}
	return m.keyCreated(ctx, name, groupRef, resp)
}

// keyCreated reads what new-api said to an add. Its successful answer has no id, so a made key is found by the name the
// sync gave it; any failure to find it then wraps ErrKeyMade, because the key exists whatever the search says.
func (m *manager) keyCreated(ctx context.Context, name, groupRef string, resp adapter.Response) (adapter.KeyRef, error) {
	var env envelope
	parsed := jsonx.Unmarshal(resp.Body, &env) == nil
	switch {
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		refused := authError(env, parsed)
		if scopeDenied(refused) {
			return "", fmt.Errorf("%w: %w", adapter.ErrKeyNotPermitted, refused)
		}
		return "", refused
	case resp.Status == http.StatusOK && parsed && env.Success:
		return m.madeRef(ctx, groupRef, name)
	case resp.Status == http.StatusOK && parsed:
		// A token limit, a group the account cannot use, or another refusal the supplier states in its words. The
		// words are not kept: they may echo the request, and the class is all the page needs.
		return "", adapter.ErrKeyRefused
	case resp.Status >= http.StatusInternalServerError:
		return "", fmt.Errorf("%w: make the key: status %d", errServer, resp.Status)
	default:
		return "", fmt.Errorf("%w: make the key: status %d", adapter.ErrUnrecognised, resp.Status)
	}
}

// madeRef is the id of the key new-api just made, found by its name. A failure to find it wraps ErrKeyMade: the key
// may exist but not be visible yet, and a second create now would make a second key.
func (m *manager) madeRef(ctx context.Context, groupRef, name string) (adapter.KeyRef, error) {
	key, found, err := m.KeyNamed(ctx, groupRef, name)
	switch {
	case err != nil:
		return "", fmt.Errorf("%w: %w", adapter.ErrKeyMade, err)
	case !found:
		return "", fmt.Errorf("%w: new-api does not list it yet", adapter.ErrKeyMade)
	}
	return key.Ref, nil
}

// KeyNamed implements adapter.KeyMaker with new-api's search, which matches the name whole when it holds no % or _:
// every page is read, to the total the search reports, and a search that stops short of it is an error, not an
// absence. Of two keys with the name in the group, the oldest (lowest id) is the answer.
func (m *manager) KeyNamed(ctx context.Context, groupRef, name string) (adapter.KeyState, bool, error) {
	var named []adapter.KeyState
	read := 0
	for page := 1; page <= maxPages; page++ {
		tokens, total, err := m.namedPage(ctx, name, page)
		if err != nil {
			return adapter.KeyState{}, false, err
		}
		for _, t := range tokens {
			if t.state.Name == name && t.state.GroupRef == groupRef {
				named = append(named, t.state)
			}
		}
		if read += len(tokens); read >= total {
			key, found := oldest(named)
			return key, found, nil
		}
		if len(tokens) == 0 {
			return adapter.KeyState{}, false, fmt.Errorf("%w: the search of keys named %s ended before its total", adapter.ErrUnrecognised, name)
		}
	}
	return adapter.KeyState{}, false, fmt.Errorf("%w: more than %d pages of keys named %s", adapter.ErrUnrecognised, maxPages, name)
}

// namedPage is one page of new-api's search of the keys named name.
func (m *manager) namedPage(ctx context.Context, name string, page int) ([]token, int, error) {
	query := url.Values{"keyword": {name}, "p": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(keyPageSize)}}
	tokens, total, err := m.tokens(ctx, "/api/token/search?"+query.Encode())
	if scopeDenied(err) {
		return nil, 0, fmt.Errorf("%w: %w", adapter.ErrKeyNotPermitted, err)
	}
	return tokens, total, err
}

// oldest is the key of the lowest id: new-api's ids are decimal numbers without leading zeros (keyID), so the shorter
// is the lower, and of two as long the first in order.
func oldest(keys []adapter.KeyState) (adapter.KeyState, bool) {
	if len(keys) == 0 {
		return adapter.KeyState{}, false
	}
	return slices.MinFunc(keys, func(a, b adapter.KeyState) int {
		return cmp.Or(cmp.Compare(len(a.Ref), len(b.Ref)), strings.Compare(string(a.Ref), string(b.Ref)))
	}), true
}

// CreateReplay implements adapter.KeyMaker: new-api makes a second key for a second create, whatever its name.
func (*manager) CreateReplay() time.Duration { return 0 }
