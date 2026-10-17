package sub2api

import (
	"context"
	"fmt"
	"net/http"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

type manager struct {
	s *session
}

var (
	_ adapter.Manager   = (*manager)(nil)
	_ adapter.Rescanner = (*manager)(nil)
)

// listing is the position as the module keeps it, opaque bytes of this adapter's own.
func listing(p any) adapter.Listing {
	b, err := jsonx.MarshalCanonical(p)
	if err != nil {
		panic(err) // a position of names, numbers and statuses always encodes
	}
	return b
}

func (m *manager) get(ctx context.Context, path string, buckets ...string) (adapter.Response, error) {
	return m.s.authed(ctx, adapter.Request{Method: http.MethodGet, Path: path, Buckets: buckets})
}

func statusError(what string, resp adapter.Response) error {
	return fmt.Errorf("sub2api: %s: status %d", what, resp.Status)
}

// Account reads the balance and where it is drawn from. An active subscription means some calls are paid from its
// windows, not the wallet; the supplier is then not taken to be wallet-funded.
func (m *manager) Account(ctx context.Context) (adapter.Account, error) {
	resp, err := m.get(ctx, "/api/v1/auth/me", bucketPanel)
	if err != nil {
		return adapter.Account{}, err
	}
	if resp.Status != http.StatusOK {
		return adapter.Account{}, statusError("account", resp)
	}
	me, err := open[struct {
		Balance number `json:"balance"`
	}](resp)
	if err != nil {
		return adapter.Account{}, err
	}
	balance, err := me.Balance.amount()
	if err != nil {
		return adapter.Account{}, err
	}
	resp, err = m.get(ctx, "/api/v1/subscriptions/active", bucketPanel)
	if err != nil {
		return adapter.Account{}, err
	}
	if resp.Status != http.StatusOK {
		return adapter.Account{}, statusError("subscriptions", resp)
	}
	subscriptions, err := open[[]any](resp)
	if err != nil {
		return adapter.Account{}, err
	}
	if subscriptions == nil {
		return adapter.Account{}, fmt.Errorf("%w: no list of subscriptions", adapter.ErrUnrecognised)
	}
	funding := adapter.FundingWallet
	if len(subscriptions) > 0 {
		funding = adapter.FundingOther
	}
	return adapter.Account{Balance: balance, Funding: funding, UnitHint: "USD"}, nil
}

// KeySecret reads one key's text by its id at the supplier.
func (m *manager) KeySecret(ctx context.Context, ref adapter.KeyRef) (secret.Secret, error) {
	id, err := pathID(string(ref))
	if err != nil {
		return secret.Secret{}, err
	}
	resp, err := m.get(ctx, "/api/v1/keys/"+id, bucketPanel)
	if err != nil {
		return secret.Secret{}, err
	}
	if resp.Status != http.StatusOK {
		return secret.Secret{}, statusError("key", resp)
	}
	k, err := open[keyDTO](resp)
	if err != nil {
		return secret.Secret{}, err
	}
	if k.Key == "" || string(k.ID) != id {
		return secret.Secret{}, fmt.Errorf("%w: a key answer for another key or without text", adapter.ErrUnrecognised)
	}
	return secret.New(k.Key), nil
}

// keyGet sends a request as one of our keys, to the relay part of the supplier.
func (m *manager) keyGet(ctx context.Context, key *keyDTO, path string) (adapter.Response, error) {
	resp, err := m.s.c.Do(ctx, adapter.Request{
		Method: http.MethodGet, Path: path,
		Header: http.Header{"Authorization": {"Bearer " + key.Key}}, Buckets: []string{bucketPanel},
	})
	if err != nil {
		return adapter.Response{}, fmt.Errorf("sub2api: %s: %w", path, err)
	}
	if resp.Status == http.StatusTooManyRequests {
		return adapter.Response{}, m.s.blocked(resp)
	}
	return resp, nil
}

// modelNames lists what a key may call. A key that cannot list (the wallet is empty, the key was refused) is not a
// failure of the sync: the models are then unread and the last known ones stay.
func (m *manager) modelNames(ctx context.Context, key *keyDTO) ([]string, bool, error) {
	resp, err := m.keyGet(ctx, key, "/v1/models")
	if err != nil {
		return nil, false, err
	}
	switch {
	case resp.Status == http.StatusOK:
	case resp.Status == http.StatusForbidden && errorCode(resp.Body) == "INSUFFICIENT_BALANCE",
		resp.Status == http.StatusUnauthorized && errorCode(resp.Body) == "INVALID_API_KEY":
		return nil, false, nil
	default:
		return nil, false, statusError("models", resp)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := decode(resp.Body, &list); err != nil {
		return nil, false, err
	}
	if list.Data == nil {
		return nil, false, fmt.Errorf("%w: a model list without data", adapter.ErrUnrecognised)
	}
	names := make([]string, len(list.Data))
	for i, d := range list.Data {
		names[i] = d.ID
	}
	return sortedUnique(names), true, nil
}

// resolvedMultiplier is the multiplier a key is billed at now. The endpoint is missing in the supplier's simple run
// mode; the group's own multiplier then stands.
func (m *manager) resolvedMultiplier(ctx context.Context, key *keyDTO) (number, bool, error) {
	resp, err := m.keyGet(ctx, key, "/v1/sub2api/billing")
	if err != nil {
		return "", false, err
	}
	switch resp.Status {
	case http.StatusNotFound, http.StatusForbidden, http.StatusUnauthorized:
		return "", false, nil
	case http.StatusOK:
	default:
		return "", false, statusError("billing", resp)
	}
	var b struct {
		Resolved number `json:"resolved_rate_multiplier"`
	}
	if err := decode(resp.Body, &b); err != nil || !b.Resolved.present() {
		return "", false, fmt.Errorf("%w: the billing answer lacks the resolved multiplier", adapter.ErrUnrecognised)
	}
	return b.Resolved, true, nil
}
