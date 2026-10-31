package quality

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

var t0 = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// TestJudge is H-I4: only the outcomes the table calls failures count as failures, and what it does not count stays
// out of health altogether. Each row is one row of docs/domains/quality.md「什么算一次失败」, across the cause, the scope,
// whether the client had left, the stream's fault and whether the input was over the timeout threshold.
func TestJudge(t *testing.T) {
	endpoint := suppliers.ScopeEndpoint
	small, large := int64(slowMaxInput), int64(slowMaxInput+1)
	committed := t0.Add(time.Second)
	cases := []struct {
		name   string
		o      Observation
		want   verdict
		metric string // the counter it adds one to, if any
	}{
		{"normal end", Observation{Done: true, FirstToken: committed}, success, ""},
		{"normal end, the client gone after the commit", Observation{Done: true, Gone: true, FirstToken: committed, Delivered: true}, success, ""},
		{"broken after the commit, the client gone", Observation{Fault: protocol.FaultBroken, Gone: true, FirstToken: committed, Delivered: true}, success, ""},
		{"content after the client left, then broken", Observation{Fault: protocol.FaultBroken, Gone: true, FirstToken: committed}, uncounted, ""},
		{"content after the client left, then a normal end", Observation{Done: true, Gone: true, FirstToken: committed}, success, ""},
		{"compact reply read whole", Observation{Done: true}, success, ""},
		{"5xx", Observation{Failure: &suppliers.Failure{Scope: endpoint, Cause: suppliers.UpstreamError{Status: 502}}}, failure, ""},
		{"overloaded", Observation{Failure: &suppliers.Failure{Scope: endpoint, Cause: suppliers.Overloaded{}}}, failure, ""},
		{"model not found", Observation{Failure: &suppliers.Failure{Scope: endpoint, Cause: suppliers.ModelNotFound{}}}, failure, ""},
		{"unknown 500", Observation{Failure: &suppliers.Failure{Scope: endpoint, Cause: suppliers.Unknown{Status: 500}}}, failure, ""},
		{"unknown 413", Observation{Failure: &suppliers.Failure{Scope: endpoint, Cause: suppliers.Unknown{Status: 413}}}, uncounted, "axiom.quality.unclassified"},
		{"transport after writing", Observation{Failure: &suppliers.Failure{Scope: endpoint, Cause: suppliers.Transport{}}}, failure, ""},
		{"header wait, short input", Observation{Failure: &suppliers.Failure{Scope: endpoint, Cause: suppliers.Transport{}}, Cut: HeaderWait, InputTokens: small}, auxiliary, ""},
		{"header wait, long input", Observation{Failure: &suppliers.Failure{Scope: endpoint, Cause: suppliers.Transport{}}, Cut: HeaderWait, InputTokens: large}, uncounted, ""},
		{"idle before the commit, short input", Observation{Cut: Idle, InputTokens: small}, auxiliary, ""},
		{"idle before the commit, long input", Observation{Cut: Idle, InputTokens: large}, uncounted, ""},
		{"idle after the commit", Observation{Cut: Idle, InputTokens: large, FirstToken: committed}, failure, ""},
		{"200 without content", Observation{Done: true, Empty: true}, failure, ""},
		{"broken stream", Observation{Fault: protocol.FaultBroken}, failure, ""},
		{"broken after the commit", Observation{Fault: protocol.FaultBroken, FirstToken: committed}, failure, ""},
		{"server error event", Observation{Fault: protocol.FaultServer}, failure, ""},
		{"rate limit error event", Observation{Fault: protocol.FaultRateLimited}, uncounted, ""},
		{"context error event", Observation{Fault: protocol.FaultContextTooLong}, uncounted, ""},
		{"moderation error event", Observation{Fault: protocol.FaultModeration}, uncounted, ""},
		{"request error event", Observation{Fault: protocol.FaultBadRequest}, uncounted, ""},
		{"unrecognised error event", Observation{Fault: protocol.FaultUnrecognised}, uncounted, ""},
		{"tool input not JSON", Observation{Fault: protocol.FaultToolInput}, uncounted, ""},
		{"rate limited", Observation{Failure: &suppliers.Failure{Scope: endpoint, Cause: suppliers.RateLimited{}}}, uncounted, ""},
		{"context too long", Observation{Failure: &suppliers.Failure{Scope: suppliers.ScopeRequest, Cause: suppliers.ContextTooLong{}}}, uncounted, ""},
		{"moderation", Observation{Failure: &suppliers.Failure{Scope: suppliers.ScopeRequest, Cause: suppliers.ContentRejected{}}}, uncounted, ""},
		{"bad request", Observation{Failure: &suppliers.Failure{Scope: suppliers.ScopeRequest, Cause: suppliers.BadRequest{}}}, uncounted, ""},
		{"credential rejected", Observation{Failure: &suppliers.Failure{Scope: suppliers.ScopeCredential, Cause: suppliers.CredentialRejected{}}}, uncounted, ""},
		{"credential quota", Observation{Failure: &suppliers.Failure{Scope: suppliers.ScopeCredential, Cause: suppliers.CredentialQuotaExhausted{}}}, uncounted, ""},
		{"not permitted", Observation{Failure: &suppliers.Failure{Scope: suppliers.ScopeCredential, Cause: suppliers.NotPermitted{}}}, uncounted, ""},
		{"supplier balance", Observation{Failure: &suppliers.Failure{Scope: suppliers.ScopeSupplier, Cause: suppliers.SupplierBalanceLow{}}}, uncounted, ""},
		{"a 5xx at the group's scope", Observation{Failure: &suppliers.Failure{Scope: suppliers.ScopeGroup, Cause: suppliers.UpstreamError{Status: 502}}}, uncounted, ""},
		{"a 5xx at the supplier's scope", Observation{Failure: &suppliers.Failure{Scope: suppliers.ScopeSupplier, Cause: suppliers.UpstreamError{Status: 502}}}, uncounted, ""},
		{"a 5xx at the credential's scope", Observation{Failure: &suppliers.Failure{Scope: suppliers.ScopeCredential, Cause: suppliers.UpstreamError{Status: 502}}}, uncounted, ""},
		{"egress transport", Observation{Failure: &suppliers.Failure{Scope: suppliers.ScopeEgress, Cause: suppliers.Transport{}}}, uncounted, ""},
		{"suspected egress block", Observation{Failure: &suppliers.Failure{Scope: suppliers.ScopeEgress, Cause: suppliers.Unknown{Status: 403, SuspectedEgressBlock: true}}}, uncounted, "axiom.quality.suspected_block"},
		{"5xx with the client gone", Observation{Failure: &suppliers.Failure{Scope: endpoint, Cause: suppliers.UpstreamError{Status: 502}}, Gone: true}, uncounted, ""},
		{"broken with the client gone", Observation{Fault: protocol.FaultBroken, Gone: true}, uncounted, ""},
		{"200 without content, the client gone", Observation{Done: true, Empty: true, Gone: true}, uncounted, ""},
		{"transport on our own deadline", Observation{Failure: &suppliers.Failure{Scope: endpoint, Cause: suppliers.Transport{}}, Cut: Ours}, uncounted, ""},
		{"broken by our own cut", Observation{Fault: protocol.FaultBroken, Cut: Ours, FirstToken: committed}, uncounted, ""},
		{"out of the node's memory", Observation{Fault: protocol.FaultNone}, uncounted, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, counts := counted(t)
			if got := m.judge(t.Context(), &c.o); got != c.want {
				t.Errorf("judge = %d, want %d", got, c.want)
			}
			for name, n := range counts() {
				want := int64(0)
				if name == c.metric {
					want = 1
				}
				if n != want {
					t.Errorf("%s = %d, want %d", name, n, want)
				}
			}
		})
	}
}

// TestUncountedChangesNothing: what the table does not count moves health neither way, so it neither hides a failing
// endpoint nor resets one: health after a failure stays where the failure put it.
func TestUncountedChangesNothing(t *testing.T) {
	m := New(nil)
	id := suppliers.EndpointID{1}
	m.Observe(t.Context(), Observation{Endpoint: id, Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{Status: 500}}, Ended: t0})
	for _, o := range []Observation{
		{Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.RateLimited{}}},
		{Failure: &suppliers.Failure{Scope: suppliers.ScopeRequest, Cause: suppliers.BadRequest{}}},
		{Fault: protocol.FaultToolInput},
		{Done: true, Empty: true, Gone: true},
	} {
		o.Endpoint, o.Ended = id, t0
		m.Observe(t.Context(), o)
		if got := measured(m, id, t0).Health; got != 0.8 {
			t.Fatalf("after %+v health = %v, want 0.8", o, got)
		}
	}
}

// counted is a Monitor whose counters the test reads.
func counted(t *testing.T) (*Monitor, func() map[string]int64) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.WithoutCancel(t.Context())) }) //nolint:errcheck // the test is over
	m := New(provider.Meter("quality"))
	return m, func() map[string]int64 {
		var data metricdata.ResourceMetrics
		if err := reader.Collect(t.Context(), &data); err != nil {
			t.Fatal(err)
		}
		counts := map[string]int64{"axiom.quality.unclassified": 0, "axiom.quality.suspected_block": 0}
		for _, scope := range data.ScopeMetrics {
			for _, metric := range scope.Metrics {
				counts[metric.Name] += total(metric)
			}
		}
		return counts
	}
}

func total(m metricdata.Metrics) (n int64) {
	if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
		for _, p := range sum.DataPoints {
			n += p.Value
		}
	}
	return n
}
