package telemetrytest_test

import (
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"axiom/internal/platform/telemetry"
	"axiom/internal/platform/telemetry/telemetrytest"
)

// TestMetricReadsOneSeries: the helper reads a series by its Prometheus name, labels included, and hands back the text
// when there is no such series, so a failing assertion shows what the scrape held.
func TestMetricReadsOneSeries(t *testing.T) {
	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	events, err := tel.Metrics.Meter("axiom/telemetrytest").Int64Counter("axiom_telemetrytest_events")
	if err != nil {
		t.Fatal(err)
	}
	events.Add(t.Context(), 3)
	events.Add(t.Context(), 4, metric.WithAttributes(attribute.String("dependency", "database")))
	for series, want := range map[string]string{
		"axiom_telemetrytest_events_total":                        "3",
		`axiom_telemetrytest_events_total{dependency="database"}`: "4",
	} {
		if got := telemetrytest.Metric(t, tel, series); got != want {
			t.Errorf("Metric(%s) = %q, want %q", series, got, want)
		}
	}
	if got := telemetrytest.Metric(t, tel, "axiom_telemetrytest_absent_total"); !strings.HasPrefix(got, "absent from:\n") {
		t.Errorf("an absent series reads %q, want the metrics text", got)
	}
}

// TestBufferHoldsWhatItsLoggerWrites: the buffer is a log destination the test can read.
func TestBufferHoldsWhatItsLoggerWrites(t *testing.T) {
	logs := &telemetrytest.Buffer{}
	logs.Logger().Warn("the queue is full", "queue", "money")
	got := logs.String()
	for _, want := range []string{"level=WARN", `msg="the queue is full"`, "queue=money"} {
		if !strings.Contains(got, want) {
			t.Errorf("the buffer holds %q, which lacks %q", got, want)
		}
	}
}
