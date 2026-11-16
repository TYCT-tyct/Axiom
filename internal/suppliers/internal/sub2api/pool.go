package sub2api

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

const (
	// poolPageSize is how many accounts one page of the directory lists.
	poolPageSize = 100
	// maxPoolPages bounds the directory read: a pool larger than this is estimated from these accounts, in the order the
	// supplier lists them, instead of spending a round on all of them.
	maxPoolPages = 20
	// poolUsageSample is the most accounts one read asks for window use: enough for a mean over a group, few enough that
	// a pool with thousands of accounts costs the supplier a handful of requests rather than a probe of every account.
	poolUsageSample = 100
	// poolBatchSize is how many accounts one usage call asks about.
	poolBatchSize = 100
)

// poolAccount is the part of an account the directory shows that decides a group's capacity. A status the supplier adds
// later is neither usable nor failed: it is not guessed.
type poolAccount struct {
	ID          number   `json:"id"`
	Status      string   `json:"status"`
	Schedulable bool     `json:"schedulable"`
	GroupIDs    []number `json:"group_ids"`
}

// poolWindow is one quota window of an account: how much of it is used, and when it resets.
type poolWindow struct {
	Utilization number     `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at"`
}

// poolUsage is what the supplier reports of one account's windows.
type poolUsage struct {
	FiveHour *poolWindow `json:"five_hour"`
	SevenDay *poolWindow `json:"seven_day"`
}

// batchUsageRequest is the body of the batch usage call.
type batchUsageRequest struct {
	AccountIDs []int64 `json:"account_ids"`
	Force      bool    `json:"force"`
}

var _ adapter.Pool = (*manager)(nil)

// roleOf is the role of the logged-in account, which decides whether the supplier is a pool at all. A stored login of a
// version that does not report a role is not an administrator.
func (m *manager) roleOf(ctx context.Context) (string, error) {
	if m.role != "" {
		return m.role, nil
	}
	me, err := m.me(ctx)
	if err != nil {
		return "", err
	}
	return me.Role, nil
}

// Pool reads the pool's groups from the account directory and the quota windows of a sample of their accounts. A login
// that does not administer the supplier is not a pool: it answers ErrNotPool without asking an administrator endpoint,
// so a relay costs nothing.
func (m *manager) Pool(ctx context.Context) (adapter.PoolReading, error) {
	role, err := m.roleOf(ctx)
	if err != nil {
		return adapter.PoolReading{}, err
	}
	if role != "admin" {
		return adapter.PoolReading{}, adapter.ErrNotPool
	}
	accounts, complete, err := m.poolAccounts(ctx)
	if err != nil {
		return adapter.PoolReading{}, err
	}
	groups := poolGroups(accounts)
	ids, sample := poolSample(groups)
	usage := map[string]*poolUsage{}
	if len(ids) > 0 {
		usage, err = m.poolUsage(ctx, ids)
		if err != nil {
			return adapter.PoolReading{}, err
		}
	}
	out := make([]adapter.GroupCapacity, 0, len(groups))
	for _, ref := range slices.Sorted(maps.Keys(groups)) {
		g := groups[ref]
		if err = g.setHeadroom(usage, sample[ref], m.s.now()); err != nil {
			return adapter.PoolReading{}, err
		}
		out = append(out, adapter.GroupCapacity{
			GroupRef: ref, Accounts: g.accounts, Failed: g.failed, Usable: g.usable, Headroom: g.headroom,
		})
	}
	return adapter.PoolReading{Groups: out, Complete: complete}, nil
}

// poolAccountGroup is a group's counts while the directory is read.
type poolAccountGroup struct {
	accounts, failed, usable int
	ids                      []number // the usable accounts, by id, in the supplier's order
	headroom                 *int
}

// poolGroups counts the directory by group. An account is failed when the pool marks it so, usable when it is active and
// schedulable: an inactive or unschedulable account is neither a failure nor capacity.
func poolGroups(accounts []poolAccount) map[string]*poolAccountGroup {
	groups := map[string]*poolAccountGroup{}
	for _, a := range accounts {
		for _, ref := range a.GroupIDs {
			g := groups[string(ref)]
			if g == nil {
				g = &poolAccountGroup{}
				groups[string(ref)] = g
			}
			g.accounts++
			switch {
			case a.Status == "error":
				g.failed++
			case a.Status == "active" && a.Schedulable:
				g.usable++
				g.ids = append(g.ids, a.ID)
			}
		}
	}
	return groups
}

// poolSample picks the accounts whose windows are asked for: up to poolUsageSample in total, spread over each group so
// that one large group does not hide a small one.
func poolSample(groups map[string]*poolAccountGroup) ([]number, map[string][]number) {
	if len(groups) == 0 {
		return nil, nil
	}
	per, budget := max(1, poolUsageSample/len(groups)), poolUsageSample
	sample := map[string][]number{}
	var ids []number
	for _, ref := range slices.Sorted(maps.Keys(groups)) {
		g := groups[ref]
		n := min(per, len(g.ids), budget)
		for i := range n {
			at := i * len(g.ids) / n
			sample[ref] = append(sample[ref], g.ids[at])
			ids = append(ids, g.ids[at])
		}
		budget -= n
	}
	return ids, sample
}

// setHeadroom sets the group's headroom from its sampled windows: the mean remaining percent of the tighter window,
// rounded down, so an estimate never says there is more room than the accounts reported.
func (g *poolAccountGroup) setHeadroom(usage map[string]*poolUsage, sampled []number, now time.Time) error {
	total, n := 0, 0
	for _, id := range sampled {
		u := usage[string(id)]
		if u == nil {
			continue
		}
		room, ok, err := u.remaining(now)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		total += room
		n++
	}
	if n == 0 {
		return nil
	}
	headroom := total / n
	g.headroom = &headroom
	return nil
}

// remaining is the percent left of the tighter of the account's windows. A window whose reset has passed is reset: its
// utilization is no longer what is left. ok is false when the supplier reported neither window; a utilization that is
// not a number is refused rather than read as spent, which would halve a healthy group's weight.
func (u poolUsage) remaining(now time.Time) (int, bool, error) {
	left, seen := 100, false
	for _, w := range []*poolWindow{u.FiveHour, u.SevenDay} {
		if w == nil || !w.Utilization.present() {
			continue
		}
		used, err := w.used()
		if err != nil {
			return 0, false, err
		}
		if w.ResetsAt != nil && !w.ResetsAt.After(now) {
			used = 0
		}
		left = min(left, max(0, int(100-used)))
		seen = true
	}
	if !seen {
		return 0, false, nil
	}
	return left, true, nil
}

// used is the share of the window the supplier reports as used. Only a JSON number is accepted: a quoted one would
// otherwise read as spent and halve a healthy group.
func (w *poolWindow) used() (float64, error) {
	v, err := strconv.ParseFloat(string(w.Utilization), 64)
	if err != nil {
		return 0, fmt.Errorf("%w: a window utilization that is not a number", adapter.ErrUnrecognised)
	}
	return v, nil
}

// validate refuses a window utilization that is not a JSON number, even for an account this read does not sample: the
// supplier changed the shape, and a later sample must not read it as spent.
func (u poolUsage) validate() error {
	for _, w := range []*poolWindow{u.FiveHour, u.SevenDay} {
		if w == nil || !w.Utilization.present() {
			continue
		}
		if _, err := w.used(); err != nil {
			return err
		}
	}
	return nil
}

// poolAccounts reads the account directory, page by page, up to maxPoolPages. A page that lacks its list or page count is
// not an empty page. complete is false when the directory had more pages than the cap: the accounts are then a prefix,
// and a caller that diffed them would clear the groups it did not reach.
func (m *manager) poolAccounts(ctx context.Context) (accounts []poolAccount, complete bool, err error) {
	for n := 1; n <= maxPoolPages; n++ {
		items, pages, err := m.poolPage(ctx, n)
		if err != nil {
			return nil, false, err
		}
		accounts = append(accounts, items...)
		if n >= pages {
			return accounts, true, nil
		}
	}
	return accounts, false, nil
}

// poolPage reads one page of the directory: its accounts and how many pages there are.
func (m *manager) poolPage(ctx context.Context, n int) ([]poolAccount, int, error) {
	resp, err := m.get(ctx, "/api/v1/admin/accounts?page="+strconv.Itoa(n)+"&page_size="+strconv.Itoa(poolPageSize)+"&lite=1", bucketPool)
	if err != nil {
		return nil, 0, err
	}
	if resp.Status == http.StatusForbidden {
		// A login that is not an administrator: the supplier is a relay, or the account's role was taken away.
		return nil, 0, adapter.ErrNotPool
	}
	if resp.Status != http.StatusOK {
		return nil, 0, statusError("accounts", resp)
	}
	pg, err := open[page[poolAccount]](resp)
	if err != nil {
		return nil, 0, err
	}
	items, pages, err := pg.checked()
	if err != nil {
		return nil, 0, err
	}
	if err = poolAccountsValid(items); err != nil {
		return nil, 0, err
	}
	return items, pages, nil
}

// poolAccountsValid checks the identifiers the counts and the window sample are keyed by: a quoted number would key a
// different group, silently dropping an account or clearing a group's reading, so it is refused as unrecognised.
func poolAccountsValid(items []poolAccount) error {
	for _, a := range items {
		if _, err := a.ID.int64(); err != nil {
			return fmt.Errorf("%w: an account without its id", adapter.ErrUnrecognised)
		}
		for _, ref := range a.GroupIDs {
			if n, err := ref.int64(); err != nil || n <= 0 {
				return fmt.Errorf("%w: an account with a group id that is not a positive integer", adapter.ErrUnrecognised)
			}
		}
	}
	return nil
}

// poolUsage asks the supplier for the windows of ids, a batch at a time. An account the supplier reported an error for,
// or did not report, has no window: it does not make the group's headroom unknown.
func (m *manager) poolUsage(ctx context.Context, ids []number) (map[string]*poolUsage, error) {
	out := map[string]*poolUsage{}
	for start := 0; start < len(ids); start += poolBatchSize {
		batch := ids[start:min(start+poolBatchSize, len(ids))]
		got, err := m.poolUsageBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		maps.Copy(out, got)
	}
	return out, nil
}

// poolUsageBatch asks for one batch of accounts at a time, under the batch size the supplier allows.
func (m *manager) poolUsageBatch(ctx context.Context, ids []number) (map[string]*poolUsage, error) {
	request, err := batchRequest(ids)
	if err != nil {
		return nil, err
	}
	body, err := jsonx.MarshalCanonical(request)
	if err != nil {
		return nil, err
	}
	resp, err := m.s.authed(ctx, adapter.Request{Method: http.MethodPost, Path: "/api/v1/admin/accounts/usage/batch", Body: body, Buckets: []string{bucketPool}})
	if err != nil {
		return nil, err
	}
	if resp.Status == http.StatusForbidden {
		return nil, adapter.ErrNotPool
	}
	if resp.Status != http.StatusOK {
		return nil, statusError("account usage", resp)
	}
	usage, err := open[struct {
		Usage map[string]poolUsage `json:"usage"`
	}](resp)
	if err != nil {
		return nil, err
	}
	if usage.Usage == nil {
		return nil, fmt.Errorf("%w: a usage answer without its accounts", adapter.ErrUnrecognised)
	}
	out := make(map[string]*poolUsage, len(usage.Usage))
	for id := range usage.Usage {
		u := usage.Usage[id]
		if err = u.validate(); err != nil {
			return nil, err
		}
		out[id] = &u
	}
	return out, nil
}

func batchRequest(ids []number) (batchUsageRequest, error) {
	r := batchUsageRequest{AccountIDs: make([]int64, len(ids))}
	for i, id := range ids {
		n, err := id.int64()
		if err != nil {
			return batchUsageRequest{}, err
		}
		r.AccountIDs[i] = n
	}
	return r, nil
}
