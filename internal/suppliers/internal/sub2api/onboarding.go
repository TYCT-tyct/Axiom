package sub2api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

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

// DeleteKey implements adapter.KeyMaker with sub2api's delete of one of the account's keys. A key sub2api does not have
// (404) is refused like any other, and the next listing no longer has it.
func (m *manager) DeleteKey(ctx context.Context, ref adapter.KeyRef) error {
	id, err := pathID(string(ref))
	if err != nil {
		return err
	}
	resp, err := m.s.authed(ctx, adapter.Request{Method: http.MethodDelete, Path: "/api/v1/keys/" + id, Buckets: []string{bucketPanel}})
	switch {
	case err != nil:
		return err
	case resp.Status == http.StatusOK:
		return nil
	case resp.Status == http.StatusForbidden:
		return adapter.ErrKeyNotPermitted
	case resp.Status == http.StatusNotFound, resp.Status == http.StatusBadRequest:
		return adapter.ErrKeyRefused
	case resp.Status >= http.StatusInternalServerError:
		return statusError("delete the key", resp)
	default:
		return fmt.Errorf("%w: delete the key: status %d", adapter.ErrUnrecognised, resp.Status)
	}
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
	case resp.Status == http.StatusBadRequest, resp.Status == http.StatusConflict:
		return "", adapter.ErrKeyRefused
	case resp.Status >= http.StatusInternalServerError:
		return "", fmt.Errorf("sub2api: make the key: status %d", resp.Status)
	default:
		return "", fmt.Errorf("%w: make the key: status %d", adapter.ErrUnrecognised, resp.Status)
	}
}
