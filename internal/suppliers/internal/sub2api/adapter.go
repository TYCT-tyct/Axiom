// Package sub2api is the adapter for sub2api (Wei-Shaw/sub2api) as a supplier: it logs in as an ordinary user and reads
// the account, the groups, our keys and the charge records, and knows how a sub2api relay answers and how it names a
// request. A login that administers the system also reads the pool's accounts and their quota windows, which decide a
// group's weight (pool.go). It never writes at the supplier, except to log in and refresh
// (internal/suppliers/README.md 「sub2api」).
package sub2api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// The rate buckets. sub2api counts logins and refreshes per address and the panel and the usage list per account;
// the limits are a quarter of its defaults (login 20 and refresh 30 per minute, panel 240, usage 60), so that other
// users of the same address cannot push us over them.
const (
	bucketLogin   = "login"
	bucketRefresh = "refresh"
	bucketPanel   = "panel"
	bucketUsage   = "usage"
	bucketPool    = "pool"
)

// Adapter is the sub2api adapter.
type Adapter struct{ now func() time.Time }

// New is the adapter for the program.
func New() *Adapter { return &Adapter{now: time.Now} }

// NewWithClock is New with its own clock, for tests that move time.
func NewWithClock(now func() time.Time) *Adapter { return &Adapter{now: now} }

// Kind is the value of suppliers.kind.
func (*Adapter) Kind() adapter.Kind { return "sub2api" }

// Buckets declares the limits that this adapter's requests count against.
func (*Adapter) Buckets() []adapter.BucketSpec {
	return []adapter.BucketSpec{
		{Name: bucketLogin, Per: adapter.CountedByIP, Window: time.Minute, Limit: 5},
		{Name: bucketRefresh, Per: adapter.CountedByIP, Window: time.Minute, Limit: 7},
		{Name: bucketPanel, Per: adapter.CountedByAccount, Window: time.Minute, Limit: 60},
		{Name: bucketUsage, Per: adapter.CountedByAccount, Window: time.Minute, Limit: 15},
		{Name: bucketPool, Per: adapter.CountedByAccount, Window: time.Minute, Limit: 30},
	}
}

// Calls returns the call half.
func (*Adapter) Calls() adapter.CallQuirks { return quirks{} }

// Detect recognises a sub2api by its public settings, which also say whether the login is behind a captcha that a
// program cannot pass. It holds no credentials.
func (*Adapter) Detect(ctx context.Context, c adapter.Client) (adapter.Detection, error) {
	resp, err := c.Do(ctx, adapter.Request{Method: http.MethodGet, Path: "/api/v1/settings/public", Buckets: []string{bucketPanel}})
	if err != nil {
		return adapter.Detection{}, fmt.Errorf("sub2api: detect: %w", err)
	}
	if resp.Status != http.StatusOK {
		return adapter.Detection{}, adapter.ErrNotRecognised
	}
	settings, err := open[struct {
		Version   string `json:"version"`
		Turnstile bool   `json:"turnstile_enabled"`
		Tencent   bool   `json:"tencent_captcha_enabled"`
		Aliyun    bool   `json:"aliyun_captcha_enabled"`
	}](resp)
	if err != nil || settings.Version == "" {
		return adapter.Detection{}, adapter.ErrNotRecognised
	}
	return adapter.Detection{
		Generation: "v1", Version: settings.Version,
		Captcha: settings.Turnstile || settings.Tencent || settings.Aliyun,
	}, nil
}

// Open loads the stored login and makes sure its access token works; the returned manager keeps it fresh.
func (a *Adapter) Open(ctx context.Context, c adapter.Client, login adapter.LoginStore) (adapter.Manager, error) {
	s := &session{c: c, store: login, now: a.now}
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	return &manager{s: s}, nil
}

// sortedUnique returns the distinct names in order.
func sortedUnique(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := names[:0:0]
	for _, n := range names {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
