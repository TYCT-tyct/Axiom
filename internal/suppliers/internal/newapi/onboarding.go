package newapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
		key, found, err := m.KeyNamed(ctx, groupRef, name)
		switch {
		case err != nil:
			return "", fmt.Errorf("%w: %w", adapter.ErrKeyMade, err)
		case !found:
			// The key may exist but not be visible yet. A second create now would make a second key.
			return "", fmt.Errorf("%w: new-api does not list it yet", adapter.ErrKeyMade)
		}
		return key.Ref, nil
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

// KeyNamed implements adapter.KeyMaker with new-api's search, which matches the name whole when it holds no % or _:
// every page is read, to the total the search reports, and a search that stops short of it is an error, not an
// absence. Of two keys with the name in the group, the oldest (lowest id) is the answer.
func (m *manager) KeyNamed(ctx context.Context, groupRef, name string) (adapter.KeyState, bool, error) {
	var best adapter.KeyState
	bestID, read := int64(0), 0
	for page := 1; ; page++ {
		if page > maxPages {
			return adapter.KeyState{}, false, fmt.Errorf("%w: more than %d pages of keys named %s", adapter.ErrUnrecognised, maxPages, name)
		}
		query := url.Values{"keyword": {name}, "p": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(keyPageSize)}}
		tokens, total, err := m.tokens(ctx, "/api/token/search?"+query.Encode())
		if err != nil {
			if scopeDenied(err) {
				return adapter.KeyState{}, false, fmt.Errorf("%w: %w", adapter.ErrKeyNotPermitted, err)
			}
			return adapter.KeyState{}, false, err
		}
		for _, t := range tokens {
			id, _ := strconv.ParseInt(string(t.state.Ref), 10, 64) //nolint:errcheck // tokens made the ref from an int64
			if t.state.Name == name && t.state.GroupRef == groupRef && (bestID == 0 || id < bestID) {
				best, bestID = t.state, id
			}
		}
		read += len(tokens)
		switch {
		case read >= total:
			return best, bestID != 0, nil
		case len(tokens) == 0:
			return adapter.KeyState{}, false, fmt.Errorf("%w: the search of keys named %s ended before its total", adapter.ErrUnrecognised, name)
		}
	}
}

// CreateReplay implements adapter.KeyMaker: new-api makes a second key for a second create, whatever its name.
func (*manager) CreateReplay() time.Duration { return 0 }
