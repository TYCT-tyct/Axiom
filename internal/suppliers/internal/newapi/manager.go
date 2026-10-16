package newapi

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// manager is the read-only management side of one account.
//
// A listing that takes more requests than the rate bucket allows in a window (9,000 keys are 91 pages against 90
// requests) cannot finish in one go. The manager keeps how far a listing got when a request was refused for want of
// allowance, so that the call made again on the same manager continues instead of starting over; the module keeps a
// manager through the retries of a round. What it kept is dropped when the listing finishes, and when it is older than
// progressLife, since the pages of an old listing no longer belong to the new one.
type manager struct {
	api api
	now func() time.Time

	mu    sync.Mutex
	keys  *keysProgress
	offer *offerProgress
}

var _ adapter.Manager = (*manager)(nil)

const progressLife = 10 * time.Minute

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

// keysProgress is how far a listing of keys got.
type keysProgress struct {
	at   time.Time
	page int
	keys []adapter.KeyState
	seen map[adapter.KeyRef]bool // a key created during the listing shifts the pages, so a page may repeat the last key of the one before
}

// Keys implements adapter.Manager. new-api's key status is 1 enabled, 2 disabled, 3 expired, 4 exhausted. A key
// with no group follows the account's group, which the empty GroupRef says.
func (m *manager) Keys(ctx context.Context) ([]adapter.KeyState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.keys
	if p == nil || m.now().Sub(p.at) > progressLife {
		p = &keysProgress{page: 1, seen: map[adapter.KeyRef]bool{}}
	}
	for ; p.page <= maxPages; p.page++ {
		page, err := m.keyPage(ctx, p.page)
		if err != nil {
			p.at, m.keys = m.now(), p
			return nil, err
		}
		for _, k := range page {
			if !p.seen[k.Ref] {
				p.seen[k.Ref] = true
				p.keys = append(p.keys, k)
			}
		}
		if len(page) < keyPageSize {
			m.keys = nil
			return p.keys, nil
		}
	}
	m.keys = nil
	return nil, fmt.Errorf("%w: more than %d pages of keys", adapter.ErrUnrecognised, maxPages)
}

func (m *manager) keyPage(ctx context.Context, page int) ([]adapter.KeyState, error) {
	var list struct {
		Data *struct {
			Items []struct {
				ID     int64  `json:"id"`
				Status int    `json:"status"`
				Group  string `json:"group"`
			} `json:"items"`
		} `json:"data"`
	}
	path := "/api/token/?p=" + strconv.Itoa(page) + "&page_size=" + strconv.Itoa(keyPageSize)
	if err := m.api.get(ctx, path, global, &list); err != nil {
		return nil, err
	}
	if list.Data == nil || list.Data.Items == nil {
		return nil, errMissing("list of keys")
	}
	keys := make([]adapter.KeyState, 0, len(list.Data.Items))
	for _, item := range list.Data.Items {
		status, ok := keyStatus[item.Status]
		if !ok || item.ID <= 0 {
			return nil, fmt.Errorf("%w: a key with status %d", adapter.ErrUnrecognised, item.Status)
		}
		keys = append(keys, adapter.KeyState{Ref: adapter.KeyRef(strconv.FormatInt(item.ID, 10)), GroupRef: item.Group, Status: status})
	}
	return keys, nil
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
		return secret.Secret{}, err
	}
	if reveal.Data == nil || reveal.Data.Key == "" {
		return secret.Secret{}, errMissing("key text")
	}
	return secret.New("sk-" + strings.TrimPrefix(reveal.Data.Key, "sk-")), nil
}
