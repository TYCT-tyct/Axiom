package sub2api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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

type manager struct {
	s *session
	// role is the role the account last reported at /auth/me: "admin" for a login that administers the pool. It is kept
	// for the round so the pool task does not ask again what the balance task just read.
	role string
}

var _ adapter.Manager = (*manager)(nil)

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

// offerProgress is the position in the offer listing: the groups and multipliers read when the pass began, the key page
// it has reached, the first active key of each group by its id, and the groups built so far. Not a key's text. A
// position whose groups are no longer the supplier's begins again, or a group removed since would be read for ever, its
// key's list refused.
type offerProgress struct {
	V         int               `json:"v"`
	Groups    []groupDTO        `json:"groups"`
	Overrides map[string]number `json:"overrides"`
	Total     int               `json:"total"`
	Page      int               `json:"page"` // the key page to read next, 0 once every key is read
	Keys      map[string]number `json:"keys"` // group id -> the id of a key in it
	Next      int               `json:"next"` // the group to read next
	Built     []builtGroup      `json:"built"`
}

// builtGroup is a group of the offer as the pass read it; the formats of its models follow from its facts.
type builtGroup struct {
	Ref, Name string
	Ratio     adapter.Amount
	Facts     adapter.GroupFacts
	Models    []string
	// ModelsUnread is set when no key of the group could list its models: they stay as they were, not empty.
	ModelsUnread bool
	// NeedsKey is set when the group can be sold but has no key of ours to read its models with.
	NeedsKey bool
}

func decodeOffer(raw adapter.Listing) offerProgress {
	if len(raw) == 0 {
		return offerProgress{}
	}
	var p offerProgress
	if jsonx.UnmarshalKnown(raw, &p) != nil || p.V != 1 || p.Page < 0 || p.Next < 0 || p.Next > len(p.Groups) {
		return offerProgress{}
	}
	return p
}

// Offer reads the groups, their multipliers and, from one of our keys in each group, the models it lists. Prices are
// not read: sub2api shows none to an ordinary user in a default installation, so the cost of a model stays unknown
// until a person confirms a base price (adapter.ScaleSchedule drafts the figure). It is read from the position in from,
// as far as the panel bucket allows; what it read is in the position it answers with.
func (m *manager) Offer(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.Group], error) {
	p, err := m.offerStart(ctx, from)
	if err != nil {
		return adapter.Page[adapter.Group]{}, err
	}
	for {
		page, more, err := m.offerPage(ctx, &p)
		if err != nil || !more {
			return page, err
		}
	}
}

func (m *manager) offerStart(ctx context.Context, from adapter.Listing) (offerProgress, error) {
	p := decodeOffer(from)
	if p.V == 0 {
		return m.startOffer(ctx, nil)
	}
	groups, err := m.groupList(ctx)
	if err != nil {
		return offerProgress{}, err
	}
	if !sameGroups(groups, p.Groups) {
		return m.startOffer(ctx, groups)
	}
	return p, nil
}

func (m *manager) offerPage(ctx context.Context, p *offerProgress) (page adapter.Page[adapter.Group], more bool, err error) {
	if p.Page != 0 {
		return m.keyPageStep(ctx, p)
	}
	if p.Next < len(p.Groups) {
		return m.groupStep(ctx, p)
	}
	return adapter.Page[adapter.Group]{Items: p.offer(), Done: true}, false, nil
}

func (m *manager) keyPageStep(ctx context.Context, p *offerProgress) (page adapter.Page[adapter.Group], more bool, err error) {
	pos := keysProgress{V: keyListingVersion, Page: p.Page, Total: p.Total}
	for group, id := range p.Keys {
		pos.Keys = append(pos.Keys, listedKey{Ref: adapter.KeyRef(id), GroupRef: group, Status: adapter.KeyActive})
	}
	keys, more, err := m.keysPage(ctx, &pos)
	if err != nil {
		return page, false, err
	}
	p.Page, p.Total = pos.Page, pos.Total
	p.Keys = map[string]number{}
	for _, k := range pos.Keys {
		group := k.GroupRef
		if _, seen := p.Keys[group]; !seen && k.Status == adapter.KeyActive {
			p.Keys[group] = number(k.Ref)
		}
	}
	if keys.Done {
		p.Page, p.Next = 0, 0
		return page, true, nil
	}
	if !more {
		return adapter.Page[adapter.Group]{Next: listing(*p), Until: keys.Until}, false, nil
	}
	return page, true, nil
}

func (m *manager) groupStep(ctx context.Context, p *offerProgress) (page adapter.Page[adapter.Group], more bool, err error) {
	key, found, err := m.groupKey(ctx, p.Keys[string(p.Groups[p.Next].ID)])
	if err != nil {
		return stopped(err, *p)
	}
	var k *keyDTO
	if found {
		k = &key
	}
	b, err := m.group(ctx, p.Groups[p.Next], p.Overrides, k)
	if err != nil {
		return stopped(err, *p)
	}
	p.Built = append(p.Built, b)
	p.Next++
	return page, true, nil
}

func stopped(err error, p offerProgress) (adapter.Page[adapter.Group], bool, error) {
	if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
		return adapter.Page[adapter.Group]{Next: listing(p), Until: blocked.Until}, false, nil
	}
	return adapter.Page[adapter.Group]{}, false, err
}

func (m *manager) startOffer(ctx context.Context, groups []groupDTO) (offerProgress, error) {
	var err error
	if groups == nil {
		if groups, err = m.groupList(ctx); err != nil {
			return offerProgress{}, err
		}
	}
	overrides, err := m.userMultipliers(ctx)
	if err != nil {
		return offerProgress{}, err
	}
	return offerProgress{V: 1, Groups: groups, Overrides: overrides, Page: 1, Keys: map[string]number{}}, nil
}

func (p offerProgress) offer() []adapter.Group {
	out := make([]adapter.Group, 0, len(p.Built))
	for _, b := range p.Built {
		g := adapter.Group{
			Ref: b.Ref, Name: b.Name, Ratio: b.Ratio, Facts: b.Facts, ModelsUnread: b.ModelsUnread, NeedsKey: b.NeedsKey,
		}
		if !b.ModelsUnread {
			formats := quirks{}.Formats(b.Facts)
			for _, n := range b.Models {
				g.Models = append(g.Models, adapter.Model{UpstreamName: n, Formats: append([]protocol.Format(nil), formats...)})
			}
		}
		out = append(out, g)
	}
	return out
}

func sameGroups(a, b []groupDTO) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Name != b[i].Name {
			return false
		}
	}
	return true
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

// groupKey reads the key a group is listed with: an id the pass took from the key list, whose text the read of the
// group's models and its multiplier needs. found is false when the group has no key, or when it went away since the list
// named it (the list is read before the groups are): the group stays unread for this pass rather than failing the task
// for ever.
func (m *manager) groupKey(ctx context.Context, id number) (key keyDTO, found bool, err error) {
	if !id.present() {
		return key, false, nil
	}
	ref, err := pathID(string(id))
	if err != nil {
		return key, false, err
	}
	resp, err := m.get(ctx, "/api/v1/keys/"+ref, bucketPanel)
	if err != nil {
		return key, false, err
	}
	switch resp.Status {
	case http.StatusNotFound:
		return key, false, nil
	case http.StatusOK:
	default:
		return key, false, statusError("key", resp)
	}
	if key, err = open[keyDTO](resp); err != nil {
		return key, false, err
	}
	if key.Key == "" || string(key.ID) != ref {
		return key, false, fmt.Errorf("%w: a key answer for another key or without text", adapter.ErrUnrecognised)
	}
	return key, true, nil
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

// group reads one group of the offer: its facts and its multiplier, and, when a key of it is given, the multiplier that
// key is billed at and the models it lists.
func (m *manager) group(ctx context.Context, g groupDTO, overrides map[string]number, key *keyDTO) (builtGroup, error) {
	b := builtGroup{Ref: string(g.ID), Name: g.Name, ModelsUnread: true}
	multiplier := g.Multiplier
	if o, ok := overrides[string(g.ID)]; ok {
		multiplier = o
	}
	f, err := jsonx.MarshalCanonical(facts{Platform: g.Platform, Status: g.Status, Subscription: g.Subscription, Peak: g.Peak})
	if err != nil {
		return builtGroup{}, err
	}
	b.Facts = f
	b.NeedsKey = key == nil && len((quirks{}).Formats(f)) > 0
	if key != nil {
		// The key's own view is the supplier's final word on the multiplier (it knows the peak hours) and on the models.
		if resolved, ok, err := m.resolvedMultiplier(ctx, key); err != nil {
			return builtGroup{}, err
		} else if ok {
			multiplier = resolved
		}
		names, ok, err := m.modelNames(ctx, key)
		if err != nil {
			return builtGroup{}, err
		}
		if ok {
			b.ModelsUnread, b.Models = false, names
		}
	}
	ratio, err := multiplier.amount()
	if err != nil {
		return builtGroup{}, err
	}
	b.Ratio = ratio
	return b, nil
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
