package pg

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestOnlyADiscardIsAlarmed: the last failure of a job is what River discards, and it alerts; a failure with an
// attempt left is not discarded and must not alert.
func TestOnlyADiscardIsAlarmed(t *testing.T) {
	t.Parallel()
	correction := func(attempt, max int) *rivertype.JobRow {
		return &rivertype.JobRow{ID: 42, Kind: "billing.correction", Attempt: attempt, MaxAttempts: max}
	}
	cases := []struct {
		name     string
		job      *rivertype.JobRow
		err      error
		panicVal any
		want     int64
	}{
		{"the last attempt fails", correction(3, 3), errors.New("the attempt has no entry yet"), nil, 1},
		{"an attempt remains", correction(2, 3), errors.New("the attempt has no entry yet"), nil, 0},
		{"the last attempt panics", correction(1, 1), nil, "boom", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			reader := sdkmetric.NewManualReader()
			var logs bytes.Buffer
			h := newDiscardHandler(
				slog.New(slog.NewJSONHandler(&logs, nil)),
				sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"),
			)
			if c.panicVal != nil {
				h.HandlePanic(t.Context(), c.job, c.panicVal, "")
			} else {
				h.HandleError(t.Context(), c.job, c.err)
			}
			if got := discardCount(t, reader); got != c.want {
				t.Errorf("the counter is %d, want %d", got, c.want)
			}
			if got := strings.Contains(logs.String(), discardedEvent); got != (c.want == 1) {
				t.Errorf("logged the discard event: %v, want %v: %s", got, c.want == 1, logs.String())
			}
		})
	}
}

// discardCount is the discard counter for the billing.correction kind, as reader has collected it.
func discardCount(t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != discardedMetric {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T, want an int64 sum", m.Name, m.Data)
			}
			for _, point := range sum.DataPoints {
				if kind, _ := point.Attributes.Value(attribute.Key("kind")); kind.AsString() == "billing.correction" {
					return point.Value
				}
			}
		}
	}
	return 0
}
