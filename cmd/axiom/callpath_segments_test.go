package main

import (
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/gateway"
)

// TestSegmentSettingsComeFromOneSource: routing and billing are configured apart, in startCallPath and startLedger, and
// the failure #229 found was one of them taking the segment settings while the other did not: an entry marked
// segmentable with no sizes to reserve it by. Both build their configuration from gateway.Config.Segments().
func TestSegmentSettingsComeFromOneSource(t *testing.T) {
	gate := gateway.DefaultConfig()
	gate.SegmentOutput, gate.SegmentOvershoot = 321, 7*time.Second
	want := billing.Segments{Output: 321, Overshoot: 7 * time.Second}
	if got := routingConfig(gate).Segments; got != want {
		t.Errorf("routing's segments = %+v, want %+v from gateway.Config.Segments()", got, want)
	}
	if got := ledgerConfig(gate, nil, nil, nil).Segments; got != want {
		t.Errorf("billing's segments = %+v, want %+v from gateway.Config.Segments()", got, want)
	}
}
