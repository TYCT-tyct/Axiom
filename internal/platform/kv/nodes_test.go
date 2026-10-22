package kv

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestMoreLiveNodesThanConfigured: a second node alive beside a node configured as the only one is visible at once in the
// count login's fallback divides by and in the gauge, and is an error in the log only when it has held for longer than a
// rolling deploy's overlap, and then once (README「配置与启动」「节点心跳」).
func TestMoreLiveNodesThanConfigured(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(db.Close)
	scope := pg.PlatformScope()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	var logs strings.Builder
	reader := sdkmetric.NewManualReader()
	node := &Client{
		config: 1, beatTimeout: defaultHeartbeatTimeout, now: func() time.Time { return now }, logger: slog.New(slog.NewTextHandler(&logs, nil)),
		m: newMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")), hear: newHeartbeat(db, scope, "this"),
	}
	node.live.Store(1)
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error { return newHeartbeat(db, scope, "other").write(t.Context(), tx) }); err != nil {
		t.Fatalf("write the other node's row: %v", err)
	}

	var over since
	node.heartbeat(t.Context(), &over)
	if got := node.Nodes(); got != 2 {
		t.Fatalf("Nodes = %d with two nodes alive and one configured, want the larger", got)
	}
	checkGauge(t, reader, nodesMetric, 2)
	if logs.Len() != 0 {
		t.Fatalf("a rolling deploy's overlap was reported at once: %s", logs.String())
	}

	now = now.Add(overFor)
	node.heartbeat(t.Context(), &over)
	node.heartbeat(t.Context(), &over)
	if got := strings.Count(logs.String(), "more core nodes are alive"); got != 1 {
		t.Fatalf("a count that stayed above N was reported %d times, want once: %s", got, logs.String())
	}
}

func checkGauge(t *testing.T, reader *sdkmetric.ManualReader, name string, want int64) {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatalf("collect the metrics: %v", err)
	}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if gauge, ok := m.Data.(metricdata.Gauge[int64]); ok && m.Name == name && len(gauge.DataPoints) > 0 {
				if got := gauge.DataPoints[len(gauge.DataPoints)-1].Value; got != want {
					t.Fatalf("%s = %d, want %d", name, got, want)
				}
				return
			}
		}
	}
	t.Fatalf("%s was not written", name)
}
