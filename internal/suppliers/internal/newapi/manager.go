package newapi

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// manager is the read-only management side of one account. It keeps nothing of a listing between reads: the position
// (adapter.Listing) says where the pass is and what it has read, and the module stores it, so a listing the supplier's
// limits stopped, an error or a restart of the node goes on where it stopped.
type manager struct {
	api api
	now func() time.Time
}

var _ adapter.Manager = (*manager)(nil)

// errMissing is the error of a successful answer without a member the adapter needs: an answer in another form than
// the one known, not an answer that there is nothing.
func errMissing(what string) error {
	return fmt.Errorf("%w: the answer has no %s", adapter.ErrUnrecognised, what)
}

func listing(p any) adapter.Listing {
	b, err := jsonx.MarshalCanonical(p)
	if err != nil {
		panic(err) // a position of names, numbers and statuses always encodes
	}
	return b
}

// Account implements adapter.Manager. The balance is the account's remaining quota, an integer in new-api's own unit;
// UnitHint says how many of them make a dollar, for a person to confirm. Funds come from a subscription, not the
// wallet, when the account has an active one, and the balance then says nothing about what calls will draw on.
func (m *manager) Account(ctx context.Context) (adapter.Account, error) {
	var self struct {
		Data *struct {
			Quota number `json:"quota"`
		} `json:"data"`
	}
	if err := m.api.get(ctx, "/api/user/self", global, &self); err != nil {
		return adapter.Account{}, err
	}
	var status statusData
	if err := m.api.get(ctx, "/api/status", global, &status); err != nil {
		return adapter.Account{}, err
	}
	var subs struct {
		Data *struct {
			Subscriptions []struct{} `json:"subscriptions"`
		} `json:"data"`
	}
	if err := m.api.get(ctx, "/api/subscription/self", global, &subs); err != nil {
		return adapter.Account{}, err
	}
	switch {
	case self.Data == nil || !self.Data.Quota.isInteger():
		return adapter.Account{}, errMissing("integer quota")
	case !status.Data.QuotaPerUnit.isNumber():
		return adapter.Account{}, errMissing("quota_per_unit")
	case subs.Data == nil || subs.Data.Subscriptions == nil:
		return adapter.Account{}, errMissing("list of subscriptions")
	}
	funding := adapter.FundingWallet
	if len(subs.Data.Subscriptions) > 0 {
		funding = adapter.FundingOther
	}
	return adapter.Account{
		Balance:  adapter.Amount(self.Data.Quota),
		Funding:  funding,
		UnitHint: "quota_per_unit=" + string(status.Data.QuotaPerUnit),
	}, nil
}

// isInteger reports whether the number is written as an integer, as new-api's quota is.
func (n number) isInteger() bool {
	_, err := strconv.ParseInt(string(n), 10, 64)
	return err == nil
}

// isNumber reports whether the number is a number at all.
func (n number) isNumber() bool { _, ok := n.rat(); return ok }

const keyPageSize = 100

// maxPages bounds a listing: a supplier whose pages never end would otherwise keep the task busy.
const maxPages = 100

const keyListingVersion = 3 // Version 2 stored adapter.KeyState, whose Name was not persisted.

// listedKey is one key read in a resumable listing. It is deliberately not adapter.KeyState: that hides Name from the
// stored state, which older nodes decode strictly, but a resumed listing needs the names of the pages already read so
// the sync can adopt a key it made before the read was interrupted.
type listedKey struct {
	Ref      adapter.KeyRef     `json:"ref"`
	GroupRef string             `json:"group_ref"`
	Name     string             `json:"name"`
	Status   adapter.KeyStatus  `json:"status"`
	Policy   *adapter.KeyPolicy `json:"policy,omitzero"`
}

func (k listedKey) state() adapter.KeyState {
	return adapter.KeyState{Ref: k.Ref, GroupRef: k.GroupRef, Name: k.Name, Status: k.Status, Policy: k.Policy}
}

func listed(k adapter.KeyState) listedKey {
	return listedKey{Ref: k.Ref, GroupRef: k.GroupRef, Name: k.Name, Status: k.Status, Policy: k.Policy}
}

// keysProgress is the position in the keys listing: the page to read next, the keys read so far, and the total the
// supplier stated. A key created during the listing shifts the pages, so one key may be read twice, which the position's
// keys take out. A listing that shrank (a key removed) moved its pages forward, which would skip a key, so it begins
// again: the keys read so far are given up, none is skipped.
type keysProgress struct {
	V     int         `json:"v"`
	Page  int         `json:"page"`
	Total int         `json:"total"`
	Keys  []listedKey `json:"keys"`
}

func decodeKeys(raw adapter.Listing) keysProgress {
	if len(raw) == 0 {
		return keysProgress{V: keyListingVersion, Page: 1}
	}
	var p keysProgress
	if jsonx.UnmarshalKnown(raw, &p) != nil || p.V != keyListingVersion || p.Page < 1 {
		return keysProgress{V: keyListingVersion, Page: 1}
	}
	return p
}

// Keys implements adapter.Manager. new-api's key status is 1 enabled, 2 disabled, 3 expired, 4 exhausted. A key
// with no group follows the account's group, which the empty GroupRef says. The listing is read from the position in
// from, as far as the global bucket allows; what it read is in the position it answers with.
func (m *manager) Keys(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	p := decodeKeys(from)
	for {
		page, more, err := m.keysPage(ctx, &p)
		if err != nil || !more {
			return page, err
		}
	}
}

func (m *manager) keysPage(ctx context.Context, p *keysProgress) (page adapter.Page[adapter.KeyState], more bool, err error) {
	if p.Page > maxPages {
		return page, false, fmt.Errorf("%w: more than %d pages of keys", adapter.ErrUnrecognised, maxPages)
	}
	items, total, err := m.keyPage(ctx, p.Page)
	if err != nil {
		if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
			return adapter.Page[adapter.KeyState]{Next: listing(*p), Until: blocked.Until}, false, nil
		}
		return page, false, err
	}
	if p.Page > 1 && total < p.Total { // the list shrank under the position: it begins again
		*p = keysProgress{V: keyListingVersion, Page: 1}
		return page, true, nil
	}
	p.Total = total
	seen := keyRefs(p.Keys)
	for _, k := range items {
		if !seen[k.Ref] {
			seen[k.Ref] = true
			p.Keys = append(p.Keys, listed(k))
		}
	}
	if len(items) < keyPageSize {
		return adapter.Page[adapter.KeyState]{Items: keyStates(p.Keys), Done: true}, false, nil
	}
	p.Page++
	return page, true, nil
}

func keyRefs(keys []listedKey) map[adapter.KeyRef]bool {
	seen := make(map[adapter.KeyRef]bool, len(keys))
	for _, k := range keys {
		seen[k.Ref] = true
	}
	return seen
}

func keyStates(keys []listedKey) []adapter.KeyState {
	out := make([]adapter.KeyState, len(keys))
	for i, k := range keys {
		out[i] = k.state()
	}
	return out
}

func (m *manager) keyPage(ctx context.Context, page int) ([]adapter.KeyState, int, error) {
	var list struct {
		Data *struct {
			Total *int `json:"total"`
			Items []struct {
				ID            int64   `json:"id"`
				Name          string  `json:"name"`
				Status        int     `json:"status"`
				Group         string  `json:"group"`
				Expires       *int64  `json:"expired_time"`
				LimitsEnabled *bool   `json:"model_limits_enabled"`
				Limits        *string `json:"model_limits"`
			} `json:"items"`
		} `json:"data"`
	}
	path := "/api/token/?p=" + strconv.Itoa(page) + "&page_size=" + strconv.Itoa(keyPageSize)
	if err := m.api.get(ctx, path, global, &list); err != nil {
		return nil, 0, err
	}
	if list.Data == nil || list.Data.Total == nil || list.Data.Items == nil {
		return nil, 0, errMissing("list of keys or how many there are")
	}
	keys := make([]adapter.KeyState, 0, len(list.Data.Items))
	for _, item := range list.Data.Items {
		status, ok := keyStatus[item.Status]
		if !ok || item.ID <= 0 {
			return nil, 0, fmt.Errorf("%w: a key with status %d", adapter.ErrUnrecognised, item.Status)
		}
		policy, err := keyPolicy(item.Expires, item.LimitsEnabled, item.Limits)
		if err != nil {
			return nil, 0, err
		}
		keys = append(keys, adapter.KeyState{
			Ref: adapter.KeyRef(strconv.FormatInt(item.ID, 10)), GroupRef: item.Group, Name: item.Name, Status: status, Policy: policy,
		})
	}
	return keys, *list.Data.Total, nil
}

func keyPolicy(expires *int64, enabled *bool, limits *string) (*adapter.KeyPolicy, error) {
	policy := &adapter.KeyPolicy{Models: []string{}}
	if expires == nil || enabled == nil || (*enabled && limits == nil) {
		return policy, nil
	}
	if *expires < -1 {
		return nil, errMissing("a valid key expiry")
	}
	if *expires != -1 {
		at := time.Unix(*expires, 0)
		policy.ExpiresAt = &at
	}
	if !*enabled {
		policy.Models = nil
	} else if *limits != "" {
		policy.Models = strings.Split(*limits, ",")
	}
	return policy, nil
}

var keyStatus = map[int]adapter.KeyStatus{
	1: adapter.KeyActive, 2: adapter.KeyDisabled, 3: adapter.KeyExpired, 4: adapter.KeyQuotaExhausted,
}

var keyID = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)

// KeySecret implements adapter.Manager: the text of the key with new-api's id ref, which new-api shows without its
// "sk-" prefix. Reading it counts in the critical bucket, which is small, so it is done once per key, when the key is
// bound.
func (m *manager) KeySecret(ctx context.Context, ref adapter.KeyRef) (secret.Secret, error) {
	if !keyID.MatchString(string(ref)) {
		return secret.Secret{}, fmt.Errorf("new-api: %q is not a key id", url.PathEscape(string(ref)))
	}
	var reveal struct {
		Data *struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := m.api.do(ctx, "POST", "/api/token/"+string(ref)+"/key", globalCritical, &reveal); err != nil {
		if errors.Is(err, errScopeDenied) {
			return secret.Secret{}, fmt.Errorf("%w: %w", adapter.ErrKeyNotPermitted, err)
		}
		return secret.Secret{}, err
	}
	if reveal.Data == nil || reveal.Data.Key == "" {
		return secret.Secret{}, errMissing("key text")
	}
	return secret.New("sk-" + strings.TrimPrefix(reveal.Data.Key, "sk-")), nil
}
