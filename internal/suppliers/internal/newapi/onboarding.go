package newapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

var _ adapter.KeyMaker = (*manager)(nil)

// CreateKey implements adapter.KeyMaker: a token with no quota, expiry or model limit, in the group the sync names.
// new-api answers with no id, so the key is found by the name the sync gave it; a lost answer is adopted by the same
// name on the next complete listing instead of making a second key.
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
	// Its successful answer has no id, so a made key is found by the name the sync gave it; a lost answer is adopted by
	// that name on the next complete listing.
	if err := keyWritten(resp, "make the key"); err != nil {
		return "", err
	}
	return m.keyByName(ctx, name, groupRef)
}

// DeleteKey implements adapter.KeyMaker with new-api's delete of one of the account's tokens. A token new-api does not
// have is answered like any refusal, so a key already gone is refused, and the next listing no longer has it.
func (m *manager) DeleteKey(ctx context.Context, ref adapter.KeyRef) error {
	id, err := strconv.ParseInt(string(ref), 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("%w: a token id %q", adapter.ErrUnrecognised, ref)
	}
	resp, err := m.api.send(ctx, http.MethodDelete, "/api/token/"+strconv.FormatInt(id, 10), global, nil)
	if err != nil {
		return err
	}
	return keyWritten(resp, "delete the key")
}

// keyWritten reads what new-api said to a change of a token: nil when it was made.
func keyWritten(resp adapter.Response, what string) error {
	var env envelope
	parsed := jsonx.Unmarshal(resp.Body, &env) == nil
	switch {
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		refused := authError(env, parsed)
		if scopeDenied(refused) {
			return fmt.Errorf("%w: %w", adapter.ErrKeyNotPermitted, refused)
		}
		return refused
	case resp.Status == http.StatusOK && parsed && env.Success:
		return nil
	case resp.Status == http.StatusOK && parsed:
		// A token limit, a group the account cannot use, a token it does not have, or another refusal the supplier
		// states in its words. The words are not kept: they may echo the request, and the class is all the page needs.
		return adapter.ErrKeyRefused
	case resp.Status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: %s: status %d", errServer, what, resp.Status)
	default:
		return fmt.Errorf("%w: %s: status %d", adapter.ErrUnrecognised, what, resp.Status)
	}
}

// keyByName finds the key just made: the create answer has no id, and the search is the only read that names it.
func (m *manager) keyByName(ctx context.Context, name, group string) (adapter.KeyRef, error) {
	var found struct {
		Data *struct {
			Items []struct {
				ID    int64  `json:"id"`
				Name  string `json:"name"`
				Group string `json:"group"`
			} `json:"items"`
		} `json:"data"`
	}
	query := url.Values{"keyword": {name}, "page_size": {"100"}}
	if err := m.api.get(ctx, "/api/token/search?"+query.Encode(), global, &found); err != nil {
		if scopeDenied(err) {
			return "", fmt.Errorf("%w: %w", adapter.ErrKeyNotPermitted, err)
		}
		return "", err
	}
	if found.Data == nil || found.Data.Items == nil {
		return "", errMissing("keys named " + name)
	}
	ref := int64(0)
	for _, item := range found.Data.Items {
		if item.ID > 0 && item.Name == name && item.Group == group && (ref == 0 || item.ID < ref) {
			ref = item.ID
		}
	}
	if ref == 0 {
		// The key may exist but not be visible yet. The next complete listing adopts it by name; a second create now
		// would make a second key.
		return "", fmt.Errorf("new-api: the key made is not listed yet")
	}
	return adapter.KeyRef(strconv.FormatInt(ref, 10)), nil
}
