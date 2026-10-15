// Package newapi is the adapter for suppliers that run new-api (QuantumNous/new-api), the system most relays are built
// on. It manages the account with a personal access token (1.0 and later), which needs no login flow and counts only
// in the global rate bucket, and reads charges from the user's consumption log. Its facts were read from new-api's
// source and checked against a running v1.0.0-rc.44; testdata/ holds the recorded answers it is tested with
// (internal/suppliers/README.md「new-api 适配器」).
package newapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// Kind is the adapter's name in suppliers.kind.
const Kind adapter.Kind = "newapi"

// Adapter is the new-api adapter.
type Adapter struct{ now func() time.Time }

var _ adapter.Adapter = (*Adapter)(nil)

// New is the adapter, reading the time from the clock.
func New() *Adapter { return &Adapter{now: time.Now} }

// Kind implements adapter.Adapter.
func (*Adapter) Kind() adapter.Kind { return Kind }

// Buckets implements adapter.Adapter: a quarter of new-api's defaults for the 1.0 line.
func (*Adapter) Buckets() []adapter.BucketSpec {
	return []adapter.BucketSpec{
		{Name: bucketGlobal, Per: adapter.CountedByIP, Window: 180 * time.Second, Limit: 90},
		{Name: bucketCritical, Per: adapter.CountedByIP, Window: 1200 * time.Second, Limit: 5},
	}
}

// statusData is the part of /api/status that is read.
type statusData struct {
	Data struct {
		Version      string `json:"version"`
		QuotaPerUnit number `json:"quota_per_unit"`
		Turnstile    bool   `json:"turnstile_check"`
	} `json:"data"`
	Success bool `json:"success"`
}

// Detect implements adapter.Adapter. A new-api answers /api/status with its version and puts X-Oneapi-Request-Id on
// every answer; the generation is told from /api/user/login/encryption-key, which only the 1.0 line has. Version
// strings are only what a build was stamped with and can be empty, so they are not trusted for anything.
func (*Adapter) Detect(ctx context.Context, c adapter.Client) (adapter.Detection, error) {
	resp, err := c.Do(ctx, adapter.Request{Method: http.MethodGet, Path: "/api/status", Buckets: global})
	if err != nil {
		return adapter.Detection{}, err
	}
	var status statusData
	if resp.Header.Get(requestIDHeader) == "" || jsonx.Unmarshal(resp.Body, &status) != nil || !status.Success {
		return adapter.Detection{}, adapter.ErrNotRecognised
	}
	key, err := c.Do(ctx, adapter.Request{Method: http.MethodGet, Path: "/api/user/login/encryption-key", Buckets: global})
	if err != nil {
		return adapter.Detection{}, err
	}
	generation := "0.13"
	if key.Status == http.StatusOK && key.Header.Get(requestIDHeader) != "" {
		generation = "1.0"
	}
	return adapter.Detection{Generation: generation, Version: status.Data.Version, Captcha: status.Data.Turnstile}, nil
}

// Open implements adapter.Adapter. The personal access token never changes, so there is nothing to refresh and no
// state to write back; an expired one is reported before a request is spent on it.
func (a *Adapter) Open(ctx context.Context, c adapter.Client, login adapter.LoginStore) (adapter.Manager, error) {
	raw, _, err := login.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("new-api: load the login: %w", err)
	}
	l, err := DecodeLogin(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", adapter.ErrAuthRejected, err)
	}
	if !l.ExpiresAt.IsZero() && !a.now().Before(l.ExpiresAt) {
		return nil, fmt.Errorf("%w: ACCESS_TOKEN_EXPIRED", adapter.ErrAuthRejected)
	}
	return &manager{api: api{c: c, bearer: "Bearer " + l.PAT.Reveal()}, now: a.now}, nil
}

// requestIDHeader is new-api's own request id, on every answer; it is the key charges are matched by.
const requestIDHeader = "X-Oneapi-Request-Id"
