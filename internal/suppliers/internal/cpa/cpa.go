// Package cpa is the adapter for CLIProxyAPI (CPA) as a self-hosted pool (internal/suppliers/README.md「CPA」). It
// reads the accounts, the models they serve and our API keys over CPA's management API, and knows how a CPA relay
// answers and how it names a request. CPA's per-call usage is a queue that is consumed once, so the adapter also
// implements adapter.UsageQueue; a resident collector drains it (internal/suppliers/internal/collector).
package cpa

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// The rate buckets. CPA counts management requests per address; the limits are a fraction of what one administrator
// needs, so that Axiom's sync and collector never make CPA's own failed-attempt ban likely.
const (
	bucketDetect = "detect"
	bucketPanel  = "panel"
	bucketUsage  = "usage"
)

// Adapter is the CPA adapter.
type Adapter struct{ now func() time.Time }

// New is the adapter for the program.
func New() *Adapter { return &Adapter{now: time.Now} }

// NewWithClock is New with its own clock, for tests that move time.
func NewWithClock(now func() time.Time) *Adapter { return &Adapter{now: now} }

// Kind is the value of suppliers.kind.
func (*Adapter) Kind() adapter.Kind { return "cpa" }

// Buckets declares the limits that this adapter's requests count against.
func (*Adapter) Buckets() []adapter.BucketSpec {
	return []adapter.BucketSpec{
		{Name: bucketDetect, Per: adapter.CountedByIP, Window: time.Minute, Limit: 10},
		{Name: bucketPanel, Per: adapter.CountedByAccount, Window: time.Minute, Limit: 60},
		{Name: bucketUsage, Per: adapter.CountedByAccount, Window: time.Minute, Limit: 60},
	}
}

// UsesUsageQueue implements adapter.UsageQueueSource.
func (*Adapter) UsesUsageQueue() {}

// Calls returns the call half.
func (*Adapter) Calls() adapter.CallQuirks { return quirks{} }

var allFormats = []protocol.Format{protocol.Messages, protocol.Responses, protocol.Chat}

// Detect recognises CPA by its public root answer. It reads no management endpoint: a wrong management key counts
// towards CPA's five-failure address ban, and detection must not risk that.
func (*Adapter) Detect(ctx context.Context, c adapter.Client) (adapter.Detection, error) {
	resp, err := c.Do(ctx, adapter.Request{Method: http.MethodGet, Path: "/", Buckets: []string{bucketDetect}})
	if err != nil {
		return adapter.Detection{}, fmt.Errorf("cpa: detect: %w", err)
	}
	if resp.Status != http.StatusOK {
		return adapter.Detection{}, adapter.ErrNotRecognised
	}
	var root struct {
		Message   string   `json:"message"`
		Endpoints []string `json:"endpoints"`
	}
	if err := jsonx.Unmarshal(resp.Body, &root); err != nil || root.Message != "CLI Proxy API Server" {
		return adapter.Detection{}, adapter.ErrNotRecognised
	}
	return adapter.Detection{Generation: "v8"}, nil
}

// Open decodes the management key. It does not call CPA: the first management read reports a rejected key as
// ErrAuthRejected, and one such request is cheaper than a probe on every round.
func (a *Adapter) Open(ctx context.Context, c adapter.Client, login adapter.LoginStore) (adapter.Manager, error) {
	raw, _, err := login.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("cpa: load the login: %w", err)
	}
	l, err := DecodeLogin(raw)
	if err != nil {
		return nil, err
	}
	return &manager{c: c, key: l.Key, now: a.now}, nil
}

var (
	_ adapter.Adapter          = (*Adapter)(nil)
	_ adapter.Onboarding       = (*Adapter)(nil)
	_ adapter.UsageQueueSource = (*Adapter)(nil)
)
