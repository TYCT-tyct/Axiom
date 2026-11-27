package gateway

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/quality"
	"axiom/internal/routing"
)

// TestOpenRouterEndpointPublishesItsRecentRecord: the endpoints answer fills uptime_last_*, latency_last_30m (ms) and
// throughput_last_30m in OpenRouter's shape from what quality measured, and leaves each null while it is not known.
func TestOpenRouterEndpointPublishesItsRecentRecord(t *testing.T) {
	t.Parallel()
	measured := quality.Recent{
		Uptime: [3]float64{1, 2.0 / 3, 0.995}, UptimeKnown: [3]bool{true, true, true},
		Latency:    [4]time.Duration{250 * time.Millisecond, 350 * time.Millisecond, 480 * time.Millisecond, 850 * time.Millisecond},
		Throughput: [4]float64{45.25, 38.5, 28.3, 15.1},
	}
	body := mustEncodeEndpoints(t, orEntry{ID: "acme/model", Name: "model"}, []routing.Endpoint{
		{ID: routing.EndpointID(uuid.NewV7()), Codename: "g-a", Context: 1, MaxOutput: 1, Recent: measured},
		{ID: routing.EndpointID(uuid.NewV7()), Codename: "g-b", Context: 1, MaxOutput: 1},
	})
	var reply struct {
		Data struct {
			Endpoints []map[string]jsonx.Raw `json:"endpoints"`
		} `json:"data"`
	}
	if err := jsonx.Unmarshal(body, &reply); err != nil || len(reply.Data.Endpoints) != 2 {
		t.Fatalf("%v: %s", err, body)
	}
	for field, want := range map[string]string{
		"uptime_last_5m": `100`, "uptime_last_30m": `66.67`, "uptime_last_1d": `99.5`,
		"latency_last_30m":    `{"p50":250,"p75":350,"p90":480,"p99":850}`,
		"throughput_last_30m": `{"p50":45.3,"p75":38.5,"p90":28.3,"p99":15.1}`,
	} {
		if got := string(reply.Data.Endpoints[0][field]); got != want {
			t.Errorf("measured %s = %s, want %s", field, got, want)
		}
		if got := string(reply.Data.Endpoints[1][field]); got != "null" {
			t.Errorf("unmeasured %s = %s, want null", field, got)
		}
	}
}
