package sub2api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// keyPageSize is how many keys one request lists. Every page is also one request of the panel bucket.
const keyPageSize = 100

// maxPages bounds a listing: a supplier whose pages never end would otherwise keep the task busy.
const maxPages = 100

// progressLife is how long what a listing has read stays good. A listing that takes more requests than a rate bucket
// allows in a window cannot finish in one go; the manager keeps how far it got when a request was refused for want of
// allowance, and the call made again on the same manager continues. The module keeps a manager through the retries of
// a round. What was kept is dropped when the listing finishes, and when it is older than this, since the pages of an
// old listing no longer belong to the new one.
const progressLife = 10 * time.Minute

type manager struct {
	s *session

	mu    sync.Mutex
	keys  *keysProgress
	offer *offerProgress
}

var _ adapter.Manager = (*manager)(nil)

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

type keyDTO struct {
	ID      number `json:"id"`
	Key     string `json:"key"`
	GroupID number `json:"group_id"`
	Status  string `json:"status"`
	Reset5h string `json:"reset_5h_at"`
	Reset1d string `json:"reset_1d_at"`
	Reset7d string `json:"reset_7d_at"`
}

// keysProgress is how far a listing of keys got.
type keysProgress struct {
	at    time.Time
	page  int
	items []keyDTO
	seen  map[number]bool // a key created during the listing shifts the pages, so one may repeat the last of the page before
}

// listKeys reads every page of our keys. The list carries each key's text; it is used here and never kept past the
// call that asked. It is resumable: see progressLife.
func (m *manager) listKeys(ctx context.Context) ([]keyDTO, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.keys
	if p == nil || m.s.now().Sub(p.at) > progressLife {
		p = &keysProgress{page: 1, seen: map[number]bool{}}
	}
	for ; p.page <= maxPages; p.page++ {
		items, pages, err := m.keyPage(ctx, p.page)
		if err != nil {
			p.at, m.keys = m.s.now(), p
			return nil, err
		}
		for _, k := range items {
			if !p.seen[k.ID] {
				p.seen[k.ID] = true
				p.items = append(p.items, k)
			}
		}
		if p.page >= pages || len(items) == 0 {
			m.keys = nil
			return p.items, nil
		}
	}
	m.keys = nil
	return nil, fmt.Errorf("%w: more than %d pages of keys", adapter.ErrUnrecognised, maxPages)
}

func (m *manager) keyPage(ctx context.Context, p int) ([]keyDTO, int, error) {
	resp, err := m.get(ctx, "/api/v1/keys?page="+strconv.Itoa(p)+"&page_size="+strconv.Itoa(keyPageSize), bucketPanel)
	if err != nil {
		return nil, 0, err
	}
	if resp.Status != http.StatusOK {
		return nil, 0, statusError("keys", resp)
	}
	pg, err := open[page[keyDTO]](resp)
	if err != nil {
		return nil, 0, err
	}
	items, pages, err := pg.checked()
	if err != nil {
		return nil, 0, err
	}
	for _, k := range items {
		if !k.ID.present() || k.Status == "" {
			return nil, 0, fmt.Errorf("%w: a key without its id or status", adapter.ErrUnrecognised)
		}
	}
	return items, pages, nil
}

// Keys lists our keys with their state.
func (m *manager) Keys(ctx context.Context) ([]adapter.KeyState, error) {
	keys, err := m.listKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]adapter.KeyState, len(keys))
	for i, k := range keys {
		out[i] = adapter.KeyState{Ref: adapter.KeyRef(k.ID), GroupRef: string(k.GroupID), Status: keyStatus(k.Status)}
		if out[i].Status == adapter.KeyQuotaExhausted {
			out[i].ResetAt = k.resetAt()
		}
	}
	return out, nil
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

type groupDTO struct {
	ID           number `json:"id"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	Status       string `json:"status"`
	Subscription string `json:"subscription_type"`
	Peak         bool   `json:"peak_rate_enabled"`
	Multiplier   number `json:"rate_multiplier"`
}

// facts is the part of a group that decides what can be sold from it; Formats reads it back.
type facts struct {
	Platform     string `json:"platform"`
	Status       string `json:"status"`
	Subscription string `json:"subscription_type"`
	Peak         bool   `json:"peak_rate_enabled"`
}

// offerProgress is how far an offer got: what is nil has not been read yet, and built holds the groups finished.
type offerProgress struct {
	at        time.Time
	groups    []groupDTO
	overrides map[string]number
	keys      []keyDTO
	keysRead  bool
	built     []adapter.Group
}

// Offer reads the groups, their multipliers and, from one of our keys in each group, the models it lists. Prices are
// not read: sub2api shows none to an ordinary user in a default installation, so the cost of a model stays unknown
// until a person confirms a base price (adapter.ScaleSchedule drafts the figure). It is resumable: see progressLife.
func (m *manager) Offer(ctx context.Context) (adapter.Offer, error) {
	m.mu.Lock()
	p := m.offer
	if p == nil || m.s.now().Sub(p.at) > progressLife {
		p = &offerProgress{}
	}
	m.mu.Unlock()
	offer, err := m.build(ctx, p)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		p.at, m.offer = m.s.now(), p
		return adapter.Offer{}, err
	}
	m.offer = nil
	return offer, nil
}

func (m *manager) build(ctx context.Context, p *offerProgress) (adapter.Offer, error) {
	var err error
	if p.groups == nil {
		if p.groups, err = m.groupList(ctx); err != nil {
			return adapter.Offer{}, err
		}
	}
	if p.overrides == nil {
		if p.overrides, err = m.userMultipliers(ctx); err != nil {
			return adapter.Offer{}, err
		}
	}
	if !p.keysRead {
		if p.keys, err = m.listKeys(ctx); err != nil {
			return adapter.Offer{}, err
		}
		p.keysRead = true
	}
	for len(p.built) < len(p.groups) {
		g := p.groups[len(p.built)]
		group, err := m.group(ctx, g, p.overrides, firstActiveKey(p.keys, g.ID))
		if err != nil {
			return adapter.Offer{}, err
		}
		p.built = append(p.built, group)
	}
	return adapter.Offer{Groups: p.built}, nil
}

func (m *manager) groupList(ctx context.Context) ([]groupDTO, error) {
	resp, err := m.get(ctx, "/api/v1/groups/available", bucketPanel)
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, statusError("groups", resp)
	}
	groups, err := open[[]groupDTO](resp)
	if err != nil {
		return nil, err
	}
	if groups == nil {
		return nil, fmt.Errorf("%w: no list of groups", adapter.ErrUnrecognised)
	}
	for _, g := range groups {
		if !g.ID.present() || !g.Multiplier.present() || g.Name == "" {
			return nil, fmt.Errorf("%w: a group without its id, name or multiplier", adapter.ErrUnrecognised)
		}
	}
	return groups, nil
}

func firstActiveKey(keys []keyDTO, group number) *keyDTO {
	for i := range keys {
		if keys[i].GroupID == group && keys[i].Status == "active" && keys[i].Key != "" {
			return &keys[i]
		}
	}
	return nil
}

// userMultipliers are the per-user multipliers that replace a group's own, by group id.
func (m *manager) userMultipliers(ctx context.Context) (map[string]number, error) {
	resp, err := m.get(ctx, "/api/v1/groups/rates", bucketPanel)
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, statusError("group multipliers", resp)
	}
	rates, err := open[map[string]number](resp)
	if err != nil {
		return nil, err
	}
	if rates == nil {
		return nil, fmt.Errorf("%w: no map of multipliers", adapter.ErrUnrecognised)
	}
	return rates, nil
}

func (m *manager) group(ctx context.Context, g groupDTO, overrides map[string]number, key *keyDTO) (adapter.Group, error) {
	multiplier := g.Multiplier
	if o, ok := overrides[string(g.ID)]; ok {
		multiplier = o
	}
	f, err := jsonx.MarshalCanonical(facts{Platform: g.Platform, Status: g.Status, Subscription: g.Subscription, Peak: g.Peak})
	if err != nil {
		return adapter.Group{}, err
	}
	out := adapter.Group{Ref: string(g.ID), Name: g.Name, Facts: f, ModelsUnread: true}
	if key != nil {
		// The key's own view is the supplier's final word on the multiplier (it knows the peak hours) and on the models.
		if resolved, ok, err := m.resolvedMultiplier(ctx, key); err != nil {
			return adapter.Group{}, err
		} else if ok {
			multiplier = resolved
		}
		names, ok, err := m.modelNames(ctx, key)
		if err != nil {
			return adapter.Group{}, err
		}
		if ok {
			formats := quirks{}.Formats(f)
			out.ModelsUnread = false
			for _, n := range names {
				out.Models = append(out.Models, adapter.Model{UpstreamName: n, Formats: append([]protocol.Format(nil), formats...)})
			}
		}
	}
	if out.Ratio, err = multiplier.amount(); err != nil {
		return adapter.Group{}, err
	}
	return out, nil
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
