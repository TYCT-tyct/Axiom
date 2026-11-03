package cpa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

const (
	authFilesPageSize = 500
	maxAuthFilesPages = 40
	maxModelsPerOffer = 20
	groupRef          = "default"
)

var errFileGone = errors.New("cpa: the auth file is gone")

type manager struct {
	c   adapter.Client
	key secret.Secret
	now func() time.Time

	mu       sync.Mutex
	cache    *authFiles
	cachedAt time.Time
}

type authFiles struct {
	files    []authFile
	complete bool
}

type authFile struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Type           string    `json:"type"`
	Provider       string    `json:"provider"`
	Status         string    `json:"status"`
	Disabled       bool      `json:"disabled"`
	Unavailable    bool      `json:"unavailable"`
	NextRetryAfter time.Time `json:"next_retry_after"`
	Quota          struct {
		ObservedAt time.Time         `json:"observed_at"`
		Signals    map[string]string `json:"signals"`
	} `json:"quota"`
}

type authFilesPage struct {
	Files   *[]authFile `json:"files"`
	HasMore bool        `json:"has_more"`
}

// Account reads the number of accounts that can serve now. A CPA has no cash balance; the native unit is an account,
// and the unit hint is 1 so that a person confirms the only value that keeps the routing gate meaningful. The all-zero
// cost schedule makes the unit value irrelevant to a call's cost.
func (m *manager) Account(ctx context.Context) (adapter.Account, error) {
	files, _, err := m.list(ctx)
	if err != nil {
		return adapter.Account{}, err
	}
	n := 0
	for _, f := range files {
		if usable(f, m.now()) {
			n++
		}
	}
	return adapter.Account{Balance: adapter.Amount(strconv.Itoa(n)), Funding: adapter.FundingWallet, UnitHint: "1"}, nil
}

// list reads the account directory, cached for the round so that Account, Offer and Pool do not read it three times.
func (m *manager) list(ctx context.Context) ([]authFile, bool, error) {
	m.mu.Lock()
	if m.cache != nil && m.now().Sub(m.cachedAt) < 30*time.Second {
		files, complete := m.cache.files, m.cache.complete
		m.mu.Unlock()
		return files, complete, nil
	}
	m.mu.Unlock()

	var all []authFile
	complete := false
	for page := 1; page <= maxAuthFilesPages; page++ {
		var out authFilesPage
		if err := m.read(ctx, fmt.Sprintf("/v0/management/auth-files?page=%d&page_size=%d", page, authFilesPageSize), bucketPanel, &out); err != nil {
			return nil, false, err
		}
		if out.Files == nil {
			return nil, false, fmt.Errorf("%w: auth-files without a files list", adapter.ErrUnrecognised)
		}
		all = append(all, (*out.Files)...)
		if !out.HasMore {
			complete = true
			break
		}
		if len(*out.Files) == 0 {
			break // a page that says there is more but lists nothing would loop forever
		}
	}
	m.mu.Lock()
	m.cache, m.cachedAt = &authFiles{files: all, complete: complete}, m.now()
	m.mu.Unlock()
	return all, complete, nil
}

// usable reports whether the pool may schedule the account now. A status the adapter does not know is neither usable
// nor failed: it is not guessed into either count.
func usable(f authFile, now time.Time) bool {
	if f.Disabled || f.Status == "disabled" || f.Status == "error" {
		return false
	}
	if f.Status != "active" {
		return false
	}
	if f.Unavailable && (f.NextRetryAfter.IsZero() || f.NextRetryAfter.After(now)) {
		return false
	}
	return f.NextRetryAfter.IsZero() || !f.NextRetryAfter.After(now)
}

// Offer reads the single group of a CPA pool and the union of the models its accounts serve. CPA has one global API key
// list, not a key per group, so splitting the models by provider would leave all but one group without a key.
func (m *manager) Offer(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.Group], error) {
	files, complete, err := m.list(ctx)
	if err != nil {
		return adapter.Page[adapter.Group]{}, err
	}
	if !complete {
		return adapter.Page[adapter.Group]{}, fmt.Errorf("%w: more than %d pages of accounts", adapter.ErrUnrecognised, maxAuthFilesPages)
	}
	names := offerNames(files, m.now())
	if len(names) == 0 && len(files) > 0 {
		return adapter.Page[adapter.Group]{Items: []adapter.Group{keptGroup()}, Done: true}, nil
	}

	s, err := decodeOffer(from, names)
	if err != nil {
		return adapter.Page[adapter.Group]{}, err
	}
	if !slices.Equal(s.Files, names) {
		s = offerState{V: 1, Files: names}
	}
	return m.advance(ctx, s)
}

func offerNames(files []authFile, now time.Time) []string {
	names := make([]string, 0, len(files))
	for _, f := range files {
		if !usable(f, now) {
			continue // a dead account's models are not served; listing them would create an endpoint with no account
		}
		if name := authFileName(f); name != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// keptGroup is the group whose models are left as they are: the pool has accounts, but none can serve now, and a
// temporary failure must not delist every endpoint.
func keptGroup() adapter.Group {
	return adapter.Group{Ref: groupRef, Name: groupRef, Ratio: "1", Facts: adapter.GroupFacts(`{"v":1}`), ModelsUnread: true}
}

func (m *manager) advance(ctx context.Context, s offerState) (adapter.Page[adapter.Group], error) {
	for range maxModelsPerOffer {
		if s.Next >= len(s.Files) {
			return m.offerPage(s), nil
		}
		models, err := m.models(ctx, s.Files[s.Next])
		if errors.Is(err, errFileGone) {
			s.Next++
			s.Read++
			continue
		}
		if err != nil {
			if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
				next, encErr := s.encode()
				if encErr != nil {
					return adapter.Page[adapter.Group]{}, encErr
				}
				return adapter.Page[adapter.Group]{Next: next, Until: blocked.Until}, nil
			}
			return adapter.Page[adapter.Group]{}, err
		}
		s.Models = append(s.Models, models...)
		s.Next++
		s.Read++
	}
	next, err := s.encode()
	if err != nil {
		return adapter.Page[adapter.Group]{}, err
	}
	return adapter.Page[adapter.Group]{Next: next, Until: m.now()}, nil
}

func (*manager) offerPage(s offerState) adapter.Page[adapter.Group] {
	slices.Sort(s.Models)
	s.Models = slices.Compact(s.Models)
	g := adapter.Group{
		Ref: groupRef, Name: groupRef, Ratio: "1", Facts: adapter.GroupFacts(`{"v":1}`),
		ModelsUnread: len(s.Files) > 0 && s.Read == 0,
	}
	if !g.ModelsUnread {
		g.Models = make([]adapter.Model, 0, len(s.Models))
		for _, name := range s.Models {
			g.Models = append(g.Models, adapter.Model{
				UpstreamName: name, Formats: slices.Clone(allFormats), Cost: zeroCost,
			})
		}
	}
	return adapter.Page[adapter.Group]{Items: []adapter.Group{g}, Done: true}
}

func authFileName(f authFile) string {
	if name := strings.TrimSpace(f.Name); name != "" {
		return name
	}
	return strings.TrimSpace(f.ID)
}

// models reads one account's models. CPA lists them per auth file; there is no bulk endpoint that also says which
// account serves what.
func (m *manager) models(ctx context.Context, name string) ([]string, error) {
	var out struct {
		Models *[]struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	path := "/v0/management/auth-files/models?name=" + url.QueryEscape(name)
	resp, err := m.get(ctx, path, bucketPanel)
	if err != nil {
		return nil, err
	}
	switch resp.Status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errFileGone
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, adapter.ErrAuthRejected
	default:
		return nil, fmt.Errorf("cpa: read the models of %q: status %d", name, resp.Status)
	}
	if err := jsonx.Unmarshal(resp.Body, &out); err != nil || out.Models == nil {
		return nil, fmt.Errorf("%w: the models of %q", adapter.ErrUnrecognised, name)
	}
	models := make([]string, 0, len(*out.Models))
	for _, model := range *out.Models {
		if id := strings.TrimSpace(model.ID); id != "" {
			models = append(models, id)
		}
	}
	return models, nil
}

type offerState struct {
	V      int      `json:"v"`
	Files  []string `json:"files"`
	Next   int      `json:"next"`
	Read   int      `json:"read"`
	Models []string `json:"models,omitempty"`
}

func decodeOffer(raw adapter.Listing, files []string) (offerState, error) {
	if len(raw) == 0 {
		return offerState{V: 1, Files: files}, nil
	}
	var s offerState
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil || s.V != 1 || s.Next < 0 || s.Next > len(s.Files) || s.Read < 0 || s.Read > s.Next {
		return offerState{}, fmt.Errorf("%w: the offer position", adapter.ErrUnrecognised)
	}
	return s, nil
}

func (s offerState) encode() (adapter.Listing, error) {
	b, err := jsonx.MarshalCanonical(s)
	return adapter.Listing(b), err
}

// Keys reads CPA's global API keys. A key's ref is the first 128 bits of its SHA-256, never the key itself.
func (m *manager) Keys(ctx context.Context, _ adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	keys, err := m.apiKeys(ctx)
	if err != nil {
		return adapter.Page[adapter.KeyState]{}, err
	}
	items := make([]adapter.KeyState, 0, len(keys))
	seen := map[string]bool{}
	for _, key := range keys {
		ref := string(keyRef(key))
		if seen[ref] {
			continue
		}
		seen[ref] = true
		items = append(items, adapter.KeyState{Ref: adapter.KeyRef(ref), GroupRef: groupRef, Status: adapter.KeyActive})
	}
	slices.SortFunc(items, func(a, b adapter.KeyState) int { return strings.Compare(string(a.Ref), string(b.Ref)) })
	return adapter.Page[adapter.KeyState]{Items: items, Done: true}, nil
}

// KeySecret reads one key's text by its ref. CPA has no per-key read, so the whole list is read and the ref matched.
func (m *manager) KeySecret(ctx context.Context, ref adapter.KeyRef) (secret.Secret, error) {
	keys, err := m.apiKeys(ctx)
	if err != nil {
		return secret.Secret{}, err
	}
	for _, key := range keys {
		if keyRef(key) == ref {
			return secret.New(key), nil
		}
	}
	return secret.Secret{}, fmt.Errorf("%w: the key is not in CPA's list", adapter.ErrKeyRefused)
}

func (m *manager) apiKeys(ctx context.Context) ([]string, error) {
	var out struct {
		Keys *[]string `json:"api-keys"`
	}
	if err := m.read(ctx, "/v0/management/api-keys", bucketPanel, &out); err != nil {
		return nil, err
	}
	if out.Keys == nil {
		return nil, fmt.Errorf("%w: api-keys without a key list", adapter.ErrUnrecognised)
	}
	keys := make([]string, 0, len(*out.Keys))
	for _, key := range *out.Keys {
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// Charges implements adapter.Manager. A CPA queue cannot be re-read, so the sync engine skips this method for a
// UsageQueue manager; it returns an empty Done page only because the interface requires one.
func (m *manager) Charges(context.Context, adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	return adapter.ChargePage{Done: true, Through: m.now()}, nil, nil
}

func keyRef(key string) adapter.KeyRef {
	sum := sha256.Sum256([]byte(key))
	return adapter.KeyRef(hex.EncodeToString(sum[:16]))
}

func (m *manager) get(ctx context.Context, path string, bucket string) (adapter.Response, error) {
	h := make(http.Header)
	h.Set("Accept", "application/json")
	h.Set("Authorization", "Bearer "+m.key.Reveal())
	resp, err := m.c.Do(ctx, adapter.Request{Method: http.MethodGet, Path: path, Header: h, Buckets: []string{bucket}})
	if err != nil {
		return adapter.Response{}, err
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		return adapter.Response{}, adapter.ErrAuthRejected
	}
	return resp, nil
}

func (m *manager) read(ctx context.Context, path, bucket string, v any) error {
	resp, err := m.get(ctx, path, bucket)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("cpa: read %s: status %d", path, resp.Status)
	}
	if err := jsonx.Unmarshal(resp.Body, v); err != nil {
		return fmt.Errorf("%w: the answer to %s", adapter.ErrUnrecognised, path)
	}
	return nil
}

var (
	_ adapter.Manager    = (*manager)(nil)
	_ adapter.Pool       = (*manager)(nil)
	_ adapter.UsageQueue = (*manager)(nil)
)
