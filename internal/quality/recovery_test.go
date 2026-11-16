package quality

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"axiom/internal/platform"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

type signalRecorder struct {
	signals []alert.Signal
	err     error
}

func (r *signalRecorder) Raise(kind, key, severity string, fields map[string]string) error {
	if r.err != nil {
		return r.err
	}
	r.signals = append(r.signals, alert.Signal{Kind: kind, Key: key, Severity: severity, Fields: fields})
	return nil
}

func (r *signalRecorder) RaiseOnce(source, kind, key, severity string, fields map[string]string) error {
	return r.Raise(kind, key, severity, fields)
}

func (r *signalRecorder) Resolve(kind, key string) error {
	if r.err != nil {
		return r.err
	}
	r.signals = append(r.signals, alert.Signal{Kind: kind, Key: key, Resolved: true})
	return nil
}

func TestRecoveryClosesIncidentSignals(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	want := []alert.Signal{
		{Kind: "endpoint_still_down", Key: id.String(), Severity: "warning"},
		{Kind: "endpoint_restored", Key: id.String(), Severity: "info"},
		{Kind: "endpoint_offline", Key: id.String(), Severity: "info", Resolved: true},
		{Kind: "endpoint_removed", Key: id.String(), Severity: "info", Resolved: true},
		{Kind: "owned_endpoint_removed", Key: id.String(), Severity: "info", Resolved: true},
		{Kind: "endpoint_still_down", Key: id.String(), Severity: "info", Resolved: true},
		{Kind: "endpoint_restored", Key: id.String(), Severity: "info", Resolved: true},
	}
	for range 2 {
		removeEndpoint(t, m, id, *now)
		tick(t, l)
		if signals := consumeHealthSignals(t, l); len(signals) != 0 {
			t.Fatalf("a trip alone signalled: %+v", signals)
		}
		*now = now.Add(30 * time.Second)
		tick(t, l)
		f.mu.Lock()
		f.result = suppliers.TestResult{Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}}
		f.mu.Unlock()
		finishProbe(t, l, f)
		*now = now.Add(time.Minute)
		tick(t, l)
		f.mu.Lock()
		f.result = suppliers.TestResult{Streamed: true}
		f.mu.Unlock()
		finishProbe(t, l, f)
		if diff := cmp.Diff(want, consumeHealthSignals(t, l), cmpopts.IgnoreFields(alert.Signal{}, "Source", "Fields")); diff != "" {
			t.Fatal(diff)
		}
		*now = now.Add(11 * time.Minute)
	}
}

func TestProbeBudgetSignalsFollowCapacityAndNodeIdentity(t *testing.T) {
	sink := &signalRecorder{}
	l := &Loop{node: "core-a", alerts: sink, active: DefaultProbeSettings().Concurrency}
	for range 5 {
		if err := l.schedule(t.Context(), []*State{{}}, nil, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	l.active = 0
	sink.err = errors.New("buffer full")
	if err := l.schedule(t.Context(), nil, nil, time.Now()); err == nil {
		t.Fatal("lost alert recovery error")
	}
	sink.err = nil
	if err := l.schedule(t.Context(), nil, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(sink.signals) != 2 || !sink.signals[1].Resolved {
		t.Fatalf("recovery: %+v", sink.signals)
	}
	restarted := &Loop{node: "core-a", alerts: sink, owner: uuid.NewV7()}
	if err := restarted.schedule(t.Context(), nil, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	want := []alert.Signal{
		{Kind: "probe_budget_exhausted", Key: "quality:core-a", Severity: "warning"},
		{Kind: "probe_budget_exhausted", Key: "quality:core-a", Resolved: true},
		{Kind: "probe_budget_exhausted", Key: "quality:core-a", Resolved: true},
	}
	if diff := cmp.Diff(want, sink.signals); diff != "" {
		t.Fatal(diff)
	}
}

func TestModelUnavailableOnlyCountsPlatformEndpoints(t *testing.T) {
	m := New(nil)
	m.mode = Enforce
	sink := &signalRecorder{}
	l := &Loop{monitor: m, alerts: sink, unavailable: map[uuid.UUID]bool{}}
	model, ownedModel := uuid.NewV7(), uuid.NewV7()
	endpoints := []suppliers.Routable{
		{ID: uuid.NewV7(), Model: model},
		{ID: uuid.NewV7(), Model: model, Account: uuid.NewV7()},
		{ID: uuid.NewV7(), Model: ownedModel, Site: uuid.NewV7()},
	}
	m.of(endpoints[0].ID).record.Opened = 1
	m.of(endpoints[2].ID).record.Opened = 1
	if err := l.checkModels(endpoints); err != nil {
		t.Fatal(err)
	}
	if len(sink.signals) != 1 || sink.signals[0].Key != model.String() || sink.signals[0].Severity != "critical" {
		t.Fatalf("platform outage: %+v", sink.signals)
	}
	m.of(endpoints[0].ID).record.Opened = 0
	if err := l.checkModels(endpoints); err != nil {
		t.Fatal(err)
	}
	if len(sink.signals) != 2 || !sink.signals[1].Resolved {
		t.Fatalf("platform recovery: %+v", sink.signals)
	}
}

func TestPrepareOnlyRestoresCollectedEndpoints(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	kept := addProbeEndpoint(f)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.Prepare(canceled, f.endpoints); err != nil {
		t.Fatalf("fresh endpoint caused a database read: %v", err)
	}
	keptState := m.of(kept)
	removeEndpoint(t, m, id, *now)
	if err := l.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(25 * time.Hour)
	if err := l.collect(t.Context(), f.endpoints[1:], *now); err != nil {
		t.Fatal(err)
	}
	if m.of(kept) != keptState {
		t.Fatal("collection discarded a present endpoint")
	}
	if err := m.Prepare(canceled, f.endpoints); err == nil {
		t.Fatal("collected endpoint did not require restoration")
	}
	if err := m.Prepare(t.Context(), f.endpoints); err != nil {
		t.Fatal(err)
	}
	if opened, _ := m.Live(id, 1).Routing(*now); opened.IsZero() {
		t.Fatal("collected endpoint lost durable breaker")
	}
}

func TestProbeDurationExcludesUnsentAttempts(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.WithoutCancel(t.Context())) }) //nolint:errcheck // the test is over
	m := New(nil)
	now := time.Now()
	l := &Loop{monitor: m, now: func() time.Time { return now }, logger: slog.Default()}
	l.instrument(provider.Meter("quality"))
	for i, request := range []platform.RequestID{{}, platform.RequestID(uuid.NewV7())} {
		r := healthRecord{ID: uuid.NewV7(), Epoch: 1, Opened: now.Add(-time.Hour).UnixNano(), Lease: now.Add(time.Minute).UnixNano(), Owner: uuid.NewV7()}
		m.of(r.ID).record = r
		l.finish(t.Context(), probeEnd{claim: r, result: suppliers.TestResult{Request: request, Total: time.Second}, err: context.DeadlineExceeded})
		count := probeDurationCount(t, reader)
		if count != uint64(i) {
			t.Fatalf("duration count = %d, want %d", count, i)
		}
	}
}

func TestParseMode(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    Mode
		invalid bool
	}{
		{"", Observe, false}, {"observe", Observe, false}, {"enforce", Enforce, false}, {"wrong", "", true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			mode, err := ParseMode(tc.value)
			if mode != tc.want || (err != nil) != tc.invalid {
				t.Fatalf("ParseMode(%q) = %q, %v", tc.value, mode, err)
			}
		})
	}
}

func TestFundsDeferralDoesNotConsumeProbeBudget(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	balance, err := platform.ParseExact("-1")
	if err != nil {
		t.Fatal(err)
	}
	f.endpoints[0].Funds = &suppliers.Funds{Balance: balance}
	removeEndpoint(t, m, id, *now)
	*now = now.Add(time.Minute)
	tick(t, l)
	tick(t, l)
	if l.budget.count() != 0 || l.active != 0 {
		t.Fatalf("funds pause consumed probes: used=%d active=%d", l.budget.count(), l.active)
	}
	f.endpoints[0].Funds.Balance = platform.Exact{}
	tick(t, l)
	if l.budget.count() != 1 || l.active != 1 {
		t.Fatal("funds recovery did not resume due probe")
	}
	finishProbe(t, l, f)
}

func consumeHealthSignals(t *testing.T, l *Loop) []alert.Signal {
	t.Helper()
	var signals []alert.Signal
	if err := l.db.Tx(t.Context(), l.scope, func(tx pg.Tx) error {
		return alert.Consume(t.Context(), tx, func(s alert.Signal) error { signals = append(signals, s); return nil })
	}); err != nil {
		t.Fatal(err)
	}
	return signals
}

func probeDurationCount(t *testing.T, reader *sdkmetric.ManualReader) (count uint64) {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	for _, scope := range data.ScopeMetrics {
		for _, v := range scope.Metrics {
			if v.Name != "axiom.quality.probe.duration" {
				continue
			}
			for _, p := range v.Data.(metricdata.Histogram[float64]).DataPoints {
				count += p.Count
			}
		}
	}
	return count
}
