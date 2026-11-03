package declared

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// keysState is a key listing as far as it got. It is a pass over all pages: the module commits only the page that ends
// the listing, so every row read is kept here until then.
type keysState struct {
	V        int      `json:"v"`
	Page     int      `json:"page,omitempty"` // the next page to read, 1-based
	Size     int      `json:"size,omitempty"` // the declared page size this pass was read with
	Total    int64    `json:"total,omitempty"`
	HasTotal bool     `json:"has_total,omitempty"`
	Rows     []keyRow `json:"rows,omitempty"`
}

type keyRow struct {
	Ref     adapter.KeyRef     `json:"ref"`
	Group   string             `json:"group,omitempty"`
	Name    string             `json:"name,omitempty"`
	Status  string             `json:"status"`
	ResetAt string             `json:"reset_at,omitempty"` // RFC 3339, empty when not stated
	Policy  *adapter.KeyPolicy `json:"policy,omitempty"`
}

const keysVersion = 1

// timeLayout keeps a stated time to the second and its zone; a supplier's times are not re-derived.
const timeLayout = time.RFC3339Nano

var keyStatusNames = map[adapter.KeyStatus]string{
	adapter.KeyActive: "active", adapter.KeyDisabled: "disabled",
	adapter.KeyQuotaExhausted: "quota_exhausted", adapter.KeyExpired: "expired",
}

// Keys implements adapter.Manager. A path without {page} is one answer; one with it is read page by page, and a limit
// stops the pass with the pages already read kept in the position.
func (m *manager) Keys(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	return m.keys(ctx, from, false)
}

// keys reads the listing, restarting once from the first page if a later page repeats a ref. That is what a supplier
// reordering its listing looks like; a second repeat is an error, so a broken supplier cannot make the pass loop.
func (m *manager) keys(ctx context.Context, from adapter.Listing, restarted bool) (adapter.Page[adapter.KeyState], error) {
	st := decodeKeys(from, m.a.keys.list.pageSize)
	if st.Page == 0 {
		st.Page = 1
	}
	if m.a.keys.list.pageSize == 0 {
		rows, err := m.readKeyPage(ctx, st, nil)
		if err != nil {
			return adapter.Page[adapter.KeyState]{}, err
		}
		if st.add(rows) {
			return adapter.Page[adapter.KeyState]{}, fmt.Errorf("%w: a key is listed twice", adapter.ErrUnrecognised)
		}
		return adapter.Page[adapter.KeyState]{Items: keyStates(st.Rows), Done: true}, nil
	}
	return m.pagedKeys(ctx, st, restarted)
}

// pagedKeys reads pages until the listing ends or a limit stops it. A duplicate ref restarts the pass once; see keys.
func (m *manager) pagedKeys(ctx context.Context, st *keysState, restarted bool) (adapter.Page[adapter.KeyState], error) {
	for {
		beforeTotal, beforeHasTotal := st.Total, st.HasTotal
		rows, err := m.readKeyPage(ctx, st, map[string]string{
			"page": fmt.Sprint(st.Page),
			"size": fmt.Sprint(m.a.keys.list.pageSize),
		})
		if err != nil {
			if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
				return adapter.Page[adapter.KeyState]{Next: encodeKeys(st), Until: blocked.Until}, nil
			}
			return adapter.Page[adapter.KeyState]{}, err
		}
		if beforeHasTotal && st.HasTotal && st.Total != beforeTotal {
			return m.changedKeyTotal(ctx, restarted)
		}
		if st.add(rows) {
			return restartKeys(m, ctx, st, restarted)
		}
		done, err := st.done(len(rows), m.a.keys.list.pageSize)
		if err != nil {
			return adapter.Page[adapter.KeyState]{}, err
		}
		if done {
			return adapter.Page[adapter.KeyState]{Items: keyStates(st.Rows), Done: true}, nil
		}
		st.Page++
	}
}

// changedKeyTotal restarts a pass whose total changed under it: the rows already read may include keys the supplier has
// since removed. A second change in a restarted pass is an error, so a supplier cannot make the pass loop.
func (m *manager) changedKeyTotal(ctx context.Context, restarted bool) (adapter.Page[adapter.KeyState], error) {
	if restarted {
		return adapter.Page[adapter.KeyState]{}, fmt.Errorf("%w: the key total changed during the pass", adapter.ErrUnrecognised)
	}
	return m.keys(ctx, nil, true)
}

// restartKeys starts a pass over on a repeated ref, unless this pass already restarted: then the supplier's listing is
// broken and retrying would only loop.
func restartKeys(m *manager, ctx context.Context, st *keysState, restarted bool) (adapter.Page[adapter.KeyState], error) {
	if restarted || st.Page == 1 {
		return adapter.Page[adapter.KeyState]{}, fmt.Errorf("%w: a key is listed twice", adapter.ErrUnrecognised)
	}
	return m.keys(ctx, nil, true)
}

// readKeyPage reads one page and parses its rows.
func (m *manager) readKeyPage(ctx context.Context, st *keysState, values map[string]string) ([]keyRow, error) {
	resp, err := m.do(ctx, "keys", http.MethodGet, m.a.keys.list.path, values, m.a.keys.list.bucket, nil)
	if err != nil {
		return nil, err
	}
	if m.a.keys.list.total != nil {
		raw, ok := m.a.keys.list.total.lookup(resp.Body)
		if !ok {
			return nil, missing("key total")
		}
		total, ok := readInteger(raw)
		if !ok {
			return nil, fmt.Errorf("%w: the key total is not a count", adapter.ErrUnrecognised)
		}
		st.Total, st.HasTotal = total, true
	}
	return parseKeys(m.a.keys.list, m.a.keys.policy, resp.Body)
}

// add appends rows, keeping one per ref. A duplicate after a restart is not a supplier's error, but two rows for one
// ref in one pass are: which one is the key would be a guess.
func (st *keysState) add(rows []keyRow) bool {
	seen := make(map[adapter.KeyRef]bool, len(st.Rows)+len(rows))
	for _, r := range st.Rows {
		seen[r.Ref] = true
	}
	for _, r := range rows {
		if seen[r.Ref] {
			return true
		}
		seen[r.Ref] = true
		st.Rows = append(st.Rows, r)
	}
	return false
}

// done reports whether the pass reached the end. A total, when the declaration reads one, is binding: an empty page
// before it, or more rows than it, is an incomplete or inconsistent listing, not a complete one. Without a total an
// empty page ends the pass and a short page is the last page.
func (st *keysState) done(pageLen, pageSize int) (bool, error) {
	if st.HasTotal {
		if int64(len(st.Rows)) > st.Total {
			return false, fmt.Errorf("%w: the key listing has %d rows, more than its total %d", adapter.ErrUnrecognised, len(st.Rows), st.Total)
		}
		if pageLen == 0 {
			if int64(len(st.Rows)) != st.Total {
				return false, fmt.Errorf("%w: the key listing ended at %d of %d rows", adapter.ErrUnrecognised, len(st.Rows), st.Total)
			}
			return true, nil
		}
		return int64(len(st.Rows)) == st.Total, nil
	}
	return pageLen == 0 || pageLen < pageSize, nil
}

func parseKeys(p keyListPlan, policy *keyPolicyPlan, body []byte) ([]keyRow, error) {
	raw, ok := p.items.lookup(body)
	if !ok {
		return nil, missing("keys")
	}
	items, err := readArray(raw, "keys")
	if err != nil {
		return nil, err
	}
	out := make([]keyRow, 0, len(items))
	for _, item := range items {
		row, err := parseKey(p, policy, item)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func parseKey(p keyListPlan, policy *keyPolicyPlan, item jsonx.Raw) (keyRow, error) {
	ref, err := readTextAt(p.ref, item, "key ref")
	if err != nil {
		return keyRow{}, err
	}
	if ref == "" {
		return keyRow{}, fmt.Errorf("%w: a key has no ref", adapter.ErrUnrecognised)
	}
	row := keyRow{Ref: adapter.KeyRef(ref)}
	if p.group != nil {
		if row.Group, err = readTextAt(*p.group, item, "key group"); err != nil {
			return keyRow{}, err
		}
	}
	if p.name != nil {
		if row.Name, err = readTextAt(*p.name, item, "key name"); err != nil {
			return keyRow{}, err
		}
	}
	status, err := p.keyStatus(item)
	if err != nil {
		return keyRow{}, err
	}
	row.Status = keyStatusNames[status]
	if status == adapter.KeyQuotaExhausted {
		row.ResetAt = keyResetAt(p.resetAt, item)
	}
	if row.Policy, err = keyPolicy(policy, item); err != nil {
		return keyRow{}, err
	}
	return row, nil
}

func (p keyListPlan) keyStatus(item jsonx.Raw) (adapter.KeyStatus, error) {
	if p.status == nil {
		return adapter.KeyActive, nil
	}
	text, err := readTextAt(*p.status, item, "key status")
	if err != nil {
		return adapter.KeyActive, err
	}
	if s, ok := p.statuses[text]; ok {
		return s, nil
	}
	if len(p.statuses) == 0 {
		if s, ok := keyStatuses[text]; ok {
			return s, nil
		}
	}
	return adapter.KeyActive, fmt.Errorf("%w: the key status %q is not one the declaration maps", adapter.ErrUnrecognised, text)
}

// keyResetAt reads when an exhausted key resets. A declaration that does not read it, or an answer that omits it,
// leaves the zero time; the status alone still says the key cannot serve.
func keyResetAt(p *path, item jsonx.Raw) string {
	if p == nil {
		return ""
	}
	raw, ok := p.lookup(item)
	if !ok {
		return ""
	}
	at, ok := readTime(raw)
	if !ok {
		return ""
	}
	return at.UTC().Format(timeLayout)
}

func keyPolicy(p *keyPolicyPlan, item jsonx.Raw) (*adapter.KeyPolicy, error) {
	if p == nil {
		// An empty policy with a nil model list means unrestricted. Returning nil here would make persist read the
		// listing as one that could not prove permission, which denies every model.
		return &adapter.KeyPolicy{}, nil
	}
	raw, ok := p.models.lookup(item)
	if !ok {
		// A row without the field permits nothing; nil would mean unrestricted, which is the unsafe reading.
		return &adapter.KeyPolicy{Models: []string{}}, nil
	}
	var values []jsonx.Raw
	if err := jsonx.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("%w: the key's models are not an array", adapter.ErrUnrecognised)
	}
	models := make([]string, 0, len(values))
	for _, v := range values {
		name, ok := readText(v)
		if !ok {
			return nil, fmt.Errorf("%w: a key's model is not text", adapter.ErrUnrecognised)
		}
		models = append(models, name)
	}
	policy := &adapter.KeyPolicy{Models: models}
	if p.expiresAt != nil {
		if raw, ok := p.expiresAt.lookup(item); ok {
			at, ok := readTime(raw)
			if !ok {
				return nil, fmt.Errorf("%w: a key's expiry is not a time", adapter.ErrUnrecognised)
			}
			policy.ExpiresAt = &at
		}
	}
	return policy, nil
}

func keyStates(rows []keyRow) []adapter.KeyState {
	out := make([]adapter.KeyState, 0, len(rows))
	for _, r := range rows {
		s := adapter.KeyState{Ref: r.Ref, GroupRef: r.Group, Name: r.Name, Policy: r.Policy}
		for status, name := range keyStatusNames {
			if name == r.Status {
				s.Status = status
			}
		}
		if r.ResetAt != "" {
			if at, err := time.Parse(timeLayout, r.ResetAt); err == nil {
				s.ResetAt = at
			}
		}
		out = append(out, s)
	}
	return out
}

func encodeKeys(st *keysState) adapter.Listing {
	st.V = keysVersion
	raw, err := jsonx.MarshalCanonical(st)
	if err != nil {
		return nil
	}
	return raw
}

func decodeKeys(from adapter.Listing, size int) *keysState {
	fresh := func() *keysState { return &keysState{V: keysVersion, Page: 1, Size: size} }
	var st keysState
	if len(from) == 0 || jsonx.UnmarshalKnown(from, &st) != nil || st.V != keysVersion || st.Page < 0 || st.Size < 0 || st.Size != size {
		return fresh()
	}
	for _, r := range st.Rows {
		if _, ok := keyStatuses[r.Status]; !ok {
			return fresh()
		}
	}
	return &st
}
