package sub2api

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

var _ adapter.KeyMaker = (*manager)(nil)

// CreateKey implements adapter.KeyMaker: a key with no quota or expiry in the group the sync names. sub2api requires
// an idempotency key on this write, and the name is the right one: a retry after a lost answer replays the first
// answer instead of making a second key.
func (m *manager) CreateKey(ctx context.Context, groupRef, name string) (adapter.KeyRef, error) {
	group, err := strconv.ParseInt(groupRef, 10, 64)
	if err != nil || group <= 0 {
		return "", fmt.Errorf("%w: group %q", adapter.ErrUnrecognised, groupRef)
	}
	body, err := jsonx.MarshalCanonical(struct {
		Name    string `json:"name"`
		GroupID int64  `json:"group_id"`
	}{Name: name, GroupID: group})
	if err != nil {
		return "", fmt.Errorf("sub2api: encode the key: %w", err)
	}
	resp, err := m.s.authed(ctx, adapter.Request{
		Method: http.MethodPost, Path: "/api/v1/keys", Buckets: []string{bucketPanel}, Body: body,
		Header: http.Header{"Content-Type": {"application/json"}, "Idempotency-Key": {name}},
	})
	if err != nil {
		return "", err
	}
	return createdKey(resp)
}

// createdKey names what sub2api said to a create: the account may not use the group, the login may not manage keys, the
// group is gone, the supplier refuses, or its error is the task's to retry.
func createdKey(resp adapter.Response) (adapter.KeyRef, error) {
	switch {
	case resp.Status == http.StatusForbidden:
		if errorCode(resp.Body) == "GROUP_NOT_ALLOWED" {
			return "", adapter.ErrKeyGroupUnavailable
		}
		return "", adapter.ErrKeyNotPermitted
	case resp.Status == http.StatusNotFound:
		return "", adapter.ErrKeyGroupUnavailable
	case resp.Status == http.StatusOK:
		k, err := open[keyDTO](resp)
		if err != nil {
			return "", err
		}
		if !k.ID.present() {
			return "", fmt.Errorf("%w: a key answer without an id", adapter.ErrUnrecognised)
		}
		return adapter.KeyRef(k.ID), nil
	case resp.Status == http.StatusConflict && idempotencyCodes[errorCode(resp.Body)]:
		// A create of this name is still running, waiting to be retried, or was sent with another body: whether a key
		// came of it is not known, so it is no refusal.
		return "", fmt.Errorf("sub2api: make the key: the create of this name is %s", errorCode(resp.Body))
	case resp.Status == http.StatusBadRequest, resp.Status == http.StatusConflict:
		return "", adapter.ErrKeyRefused
	case resp.Status >= http.StatusInternalServerError:
		return "", fmt.Errorf("sub2api: make the key: status %d", resp.Status)
	default:
		return "", fmt.Errorf("%w: make the key: status %d", adapter.ErrUnrecognised, resp.Status)
	}
}

// KeyNamed implements adapter.KeyMaker with sub2api's key search, which matches part of a name: every page of it is
// read, and only the key whose name and group are the same whole counts. An empty page before the last that sub2api
// counts is an error, not an absence. It is read in one go, so at and next are not used. Of two keys, the oldest
// (lowest id) is the answer.
func (m *manager) KeyNamed(ctx context.Context, groupRef, name string, _ adapter.Listing) (adapter.KeyState, bool, adapter.Listing, error) {
	var named []adapter.KeyState
	for p := 1; ; p++ {
		items, pages, _, err := m.keysAt(ctx, p, name)
		if err != nil {
			return adapter.KeyState{}, false, nil, err
		}
		for _, k := range items {
			if k.Name == name && string(k.GroupID) == groupRef {
				named = append(named, keyState(k))
			}
		}
		if p >= pages {
			key, found := oldest(named)
			return key, found, nil, nil
		}
		if len(items) == 0 {
			return adapter.KeyState{}, false, nil, fmt.Errorf("%w: page %d of %d of the keys named %s is empty", adapter.ErrUnrecognised, p, pages, name)
		}
	}
}

// KeyByRef implements adapter.KeyMaker with sub2api's read of one key.
func (m *manager) KeyByRef(ctx context.Context, ref adapter.KeyRef) (adapter.KeyState, bool, error) {
	k, found, err := m.keyAt(ctx, ref)
	if err != nil || !found {
		return adapter.KeyState{}, false, err
	}
	return keyState(k), true, nil
}

// oldest is the key of the lowest id: sub2api's ids are decimal numbers without leading zeros, so the shorter is the
// lower, and of two as long the first in order.
func oldest(keys []adapter.KeyState) (adapter.KeyState, bool) {
	if len(keys) == 0 {
		return adapter.KeyState{}, false
	}
	return slices.MinFunc(keys, func(a, b adapter.KeyState) int {
		return cmp.Or(cmp.Compare(len(a.Ref), len(b.Ref)), strings.Compare(string(a.Ref), string(b.Ref)))
	}), true
}

// idempotencyCodes are sub2api's answers to a create whose idempotency key, the name, it has seen before.
var idempotencyCodes = map[string]bool{"IDEMPOTENCY_IN_PROGRESS": true, "IDEMPOTENCY_KEY_CONFLICT": true, "IDEMPOTENCY_RETRY_BACKOFF": true}
