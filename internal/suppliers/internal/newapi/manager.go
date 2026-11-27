package newapi

import (
	"context"
	"fmt"
	"net/http"
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
	subscribed, err := m.subscribed(ctx)
	if err != nil {
		return adapter.Account{}, err
	}
	switch {
	case self.Data == nil || !self.Data.Quota.isInteger():
		return adapter.Account{}, errMissing("integer quota")
	case !status.Data.QuotaPerUnit.isNumber():
		return adapter.Account{}, errMissing("quota_per_unit")
	}
	funding := adapter.FundingWallet
	if subscribed {
		funding = adapter.FundingOther
	}
	return adapter.Account{
		Balance:  adapter.Amount(self.Data.Quota),
		Funding:  funding,
		UnitHint: "quota_per_unit=" + string(status.Data.QuotaPerUnit),
	}, nil
}

// subscribed reports whether the account has an active subscription (the "subscriptions" of /api/subscription/self;
// "all_subscriptions" holds the expired ones too).
func (m *manager) subscribed(ctx context.Context) (bool, error) {
	var subs struct {
		Data *struct {
			Subscriptions []struct{} `json:"subscriptions"`
		} `json:"data"`
	}
	if err := m.api.get(ctx, "/api/subscription/self", global, &subs); err != nil {
		return false, err
	}
	if subs.Data == nil || subs.Data.Subscriptions == nil {
		return false, errMissing("list of subscriptions")
	}
	return len(subs.Data.Subscriptions) > 0, nil
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

// listedKey keeps names across interrupted reads so the sync can adopt a key it made before the read stopped.
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

type keysProgress = adapter.KeyProgress[listedKey]

// Keys implements adapter.Manager. new-api's key status is 1 enabled, 2 disabled, 3 expired, 4 exhausted. A key
// with no group follows the account's group, which the empty GroupRef says. The listing is read from the position in
// from, as far as the global bucket allows; what it read is in the position it answers with.
func (m *manager) Keys(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	p := adapter.DecodeKeys[listedKey](from)
	return adapter.Drain(func() (adapter.Page[adapter.KeyState], bool, error) {
		return m.keysPage(ctx, &p)
	})
}

func (m *manager) keysPage(ctx context.Context, p *keysProgress) (adapter.Page[adapter.KeyState], bool, error) {
	return adapter.KeyStep(p, func() ([]adapter.KeyState, int, bool, error) {
		if p.Page > maxPages {
			return nil, 0, false, fmt.Errorf("%w: more than %d pages of keys", adapter.ErrUnrecognised, maxPages)
		}
		tokens, total, err := m.keyPage(ctx, p.Page)
		items := make([]adapter.KeyState, len(tokens))
		for i, t := range tokens {
			items[i] = t.state
		}
		return items, total, len(items) < keyPageSize, err
	}, listed, func(k listedKey) adapter.KeyRef { return k.Ref }, listedKey.state)
}

// token is one key of the listing: its state, and its text as the listing masks it (maskKey).
type token struct {
	state  adapter.KeyState
	masked string
}

func (m *manager) keyPage(ctx context.Context, page int) ([]token, int, error) {
	return m.tokens(ctx, "/api/token/?p="+strconv.Itoa(page)+"&page_size="+strconv.Itoa(keyPageSize))
}

// tokens reads one page of keys, from the listing or from a search, which answer in the same form, with how many there
// are in all.
func (m *manager) tokens(ctx context.Context, path string) ([]token, int, error) {
	var list struct {
		Data *struct {
			Total *int        `json:"total"`
			Items []tokenItem `json:"items"`
		} `json:"data"`
	}
	if err := m.api.get(ctx, path, global, &list); err != nil {
		return nil, 0, err
	}
	if list.Data == nil || list.Data.Total == nil || list.Data.Items == nil {
		return nil, 0, errMissing("list of keys or how many there are")
	}
	keys := make([]token, 0, len(list.Data.Items))
	for _, item := range list.Data.Items {
		t, err := item.token()
		if err != nil {
			return nil, 0, err
		}
		keys = append(keys, t)
	}
	return keys, *list.Data.Total, nil
}

// tokenItem is one key as new-api's listing, its search and its read of one key show it.
type tokenItem struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Key           string  `json:"key"`
	Status        int     `json:"status"`
	Group         string  `json:"group"`
	Expires       *int64  `json:"expired_time"`
	LimitsEnabled *bool   `json:"model_limits_enabled"`
	Limits        *string `json:"model_limits"`
}

func (item tokenItem) token() (token, error) {
	status, ok := keyStatus[item.Status]
	if !ok || item.ID <= 0 {
		return token{}, fmt.Errorf("%w: a key with status %d", adapter.ErrUnrecognised, item.Status)
	}
	policy, err := keyPolicy(item.Expires, item.LimitsEnabled, item.Limits)
	if err != nil {
		return token{}, err
	}
	return token{state: adapter.KeyState{
		Ref: adapter.KeyRef(strconv.FormatInt(item.ID, 10)), GroupRef: item.Group, Name: item.Name, Status: status, Policy: policy,
	}, masked: item.Key}, nil
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
	if err := m.api.do(ctx, http.MethodPost, "/api/token/"+string(ref)+"/key", globalCritical, &reveal); err != nil {
		if scopeDenied(err) {
			return secret.Secret{}, fmt.Errorf("%w: %w", adapter.ErrKeyNotPermitted, err)
		}
		return secret.Secret{}, err
	}
	if reveal.Data == nil || reveal.Data.Key == "" {
		return secret.Secret{}, errMissing("key text")
	}
	return secret.New("sk-" + strings.TrimPrefix(reveal.Data.Key, "sk-")), nil
}

// missingRecord is new-api's answer that the account has no key with the id asked for: its handler passes the database's
// not-found error on as the message of a failed answer, and no other failure says these words.
func missingRecord(resp adapter.Response) bool {
	var failed struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	return resp.Status == http.StatusOK && jsonx.Unmarshal(resp.Body, &failed) == nil && !failed.Success &&
		failed.Message == "record not found"
}
