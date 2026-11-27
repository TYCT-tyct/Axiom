// Package newapi is the adapter for suppliers that run new-api (QuantumNous/new-api), the system most relays are built
// on, or one-api (songquanpeng/one-api), the system new-api grew from. It manages a new-api 1.0 account with a personal
// access token, which needs no login flow and counts only in the global rate bucket, and an account of new-api before
// 1.0 or of one-api with the system access token and user id those take (system); it reads charges from the user's
// consumption log. Its facts were read from the systems' source and checked against running new-api v1.0.0-rc.51 and
// v0.13.2 and one-api v0.6.10; testdata/ holds the recorded answers it is tested with (internal/suppliers/README.md
// 「new-api 适配器」).
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

// statusData is the part of /api/status that is read. Setup, whether the site was set up, is in new-api's answer
// only, which is how one-api is told from it.
type statusData struct {
	Data struct {
		Version      string `json:"version"`
		QuotaPerUnit number `json:"quota_per_unit"`
		Turnstile    bool   `json:"turnstile_check"`
		Setup        *bool  `json:"setup"`
	} `json:"data"`
	Success bool `json:"success"`
}

// system is the program an account is on, for the reads in which the programs differ.
type system int

const (
	newAPI    system = iota // new-api 1.0, with a personal access token
	newAPI013               // new-api before 1.0, with a system access token
	oneAPI                  // one-api, with a system access token
)

// Detect implements adapter.Adapter. new-api and one-api answer /api/status with their version and put
// X-Oneapi-Request-Id on every answer. new-api's 1.0 line is told by /api/user/login/encryption-key, which only it has;
// of the others, one-api's status has no setup member. Version strings are only what a build was stamped
// with and can be empty, so they are not trusted for anything.
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
	generation := "one-api"
	switch {
	case key.Status == http.StatusOK && key.Header.Get(requestIDHeader) != "":
		generation = "1.0"
	case status.Data.Setup != nil:
		generation = "0.13"
	}
	return adapter.Detection{Generation: generation, Version: status.Data.Version, Captcha: status.Data.Turnstile}, nil
}

// Open implements adapter.Adapter. Neither kind of token changes, so there is nothing to refresh and no state to write
// back; an expired personal access token is reported before a request is spent on it.
func (a *Adapter) Open(ctx context.Context, c adapter.Client, login adapter.LoginStore) (adapter.Manager, error) {
	raw, _, err := login.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("new-api: load the login: %w", err)
	}
	if legacy, err := DecodeLegacyLogin(raw); err == nil {
		return a.openLegacy(ctx, c, legacy)
	}
	l, err := DecodeLogin(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", adapter.ErrAuthRejected, err)
	}
	if !l.ExpiresAt.IsZero() && !a.now().Before(l.ExpiresAt) {
		return nil, fmt.Errorf("%w: ACCESS_TOKEN_EXPIRED", adapter.ErrAuthRejected)
	}
	return &manager{api: api{c: c, header: http.Header{"Authorization": {"Bearer " + l.PAT.Reveal()}}}, now: a.now}, nil
}

// openLegacy learns from /api/status which system the legacy login is for and keeps that for the session: what the
// login can read differs between new-api before 1.0 and one-api, and the status says which.
func (a *Adapter) openLegacy(ctx context.Context, c adapter.Client, l LegacyLogin) (adapter.Manager, error) {
	m := &manager{
		api: api{c: c, header: http.Header{"Authorization": {l.AccessToken.Reveal()}, "New-Api-User": {l.UserID}}},
		now: a.now, sys: newAPI013,
	}
	var status statusData
	if err := m.api.get(ctx, "/api/status", global, &status); err != nil {
		return nil, err
	}
	if status.Data.Setup == nil {
		m.sys = oneAPI
	}
	return m, nil
}

// requestIDHeader is new-api's own request id, on every answer; it is the key charges are matched by.
const requestIDHeader = "X-Oneapi-Request-Id"
