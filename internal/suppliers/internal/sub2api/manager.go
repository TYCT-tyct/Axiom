package sub2api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// keyPageSize is how many keys one request lists. Every page is also one request of the panel bucket.
const keyPageSize = 100

// maxPages bounds a listing: a supplier whose pages never end would otherwise keep the task busy.
const maxPages = 100

type manager struct {
	s *session
	// role is the role the account last reported at /auth/me: "admin" for a login that administers the pool. It is kept
	// for the round so the pool task does not ask again what the balance task just read.
	role string
}

var _ adapter.Manager = (*manager)(nil)

func (m *manager) get(ctx context.Context, path string, buckets ...string) (adapter.Response, error) {
	return m.s.authed(ctx, adapter.Request{Method: http.MethodGet, Path: path, Buckets: buckets})
}

func statusError(what string, resp adapter.Response) error {
	return fmt.Errorf("sub2api: %s: status %d", what, resp.Status)
}

// meDTO is the part of the logged-in account that decides the balance and whether it administers the pool.
type meDTO struct {
	Balance number `json:"balance"`
	Role    string `json:"role"`
}

// me reads the logged-in account. The role is kept for the round, so the pool read after the balance read costs no
// request of its own.
func (m *manager) me(ctx context.Context) (meDTO, error) {
	resp, err := m.get(ctx, "/api/v1/auth/me", bucketPanel)
	if err != nil {
		return meDTO{}, err
	}
	if resp.Status != http.StatusOK {
		return meDTO{}, statusError("account", resp)
	}
	me, err := open[meDTO](resp)
	if err != nil {
		return meDTO{}, err
	}
	m.role = me.Role
	return me, nil
}

// Account reads the balance and where it is drawn from. An active subscription means some calls are paid from its
// windows, not the wallet; the supplier is then not taken to be wallet-funded.
func (m *manager) Account(ctx context.Context) (adapter.Account, error) {
	me, err := m.me(ctx)
	if err != nil {
		return adapter.Account{}, err
	}
	balance, err := me.Balance.amount()
	if err != nil {
		return adapter.Account{}, err
	}
	resp, err := m.get(ctx, "/api/v1/subscriptions/active", bucketPanel)
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

func (m *manager) keyPage(ctx context.Context, p int) ([]keyDTO, int, int, error) {
	if p > maxPages {
		return nil, 0, 0, fmt.Errorf("%w: more than %d pages of keys", adapter.ErrUnrecognised, maxPages)
	}
	resp, err := m.get(ctx, "/api/v1/keys?page="+strconv.Itoa(p)+"&page_size="+strconv.Itoa(keyPageSize), bucketPanel)
	if err != nil {
		return nil, 0, 0, err
	}
	if resp.Status != http.StatusOK {
		return nil, 0, 0, statusError("keys", resp)
	}
	pg, err := open[page[keyDTO]](resp)
	if err != nil {
		return nil, 0, 0, err
	}
	items, pages, err := pg.checked()
	if err != nil {
		return nil, 0, 0, err
	}
	total, err := pg.count()
	if err != nil {
		return nil, 0, 0, err
	}
	for _, k := range items {
		if !k.ID.present() || k.Status == "" {
			return nil, 0, 0, fmt.Errorf("%w: a key without its id or status", adapter.ErrUnrecognised)
		}
	}
	return items, pages, total, nil
}

// keyStatus maps the supplier's status; one it does not know is not usable, which is the safe reading.
func keyStatus(s string) adapter.KeyStatus {
	switch s {
	case "active":
		return adapter.KeyActive
	case "quota_exhausted":
		return adapter.KeyQuotaExhausted
	case "expired":
		return adapter.KeyExpired
	default:
		return adapter.KeyDisabled
	}
}

// resetAt is the soonest moment a window of an exhausted key resets, zero if none is stated.
func (k keyDTO) resetAt() (soonest time.Time) {
	for _, text := range []string{k.Reset5h, k.Reset1d, k.Reset7d} {
		if t, err := time.Parse(time.RFC3339, text); err == nil && (soonest.IsZero() || t.Before(soonest)) {
			soonest = t
		}
	}
	return soonest
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
