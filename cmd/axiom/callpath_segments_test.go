package main

import (
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/gateway"
)

// TestSegmentSettingsComeFromOneSource: routing and billing are configured apart, in startCallPath and startLedger, and
// the failure #229 found was one of them taking the segment settings while the other did not: an entry marked
// segmentable with no sizes to reserve it by. Both read the one live policy the ledger loaded.
func TestSegmentSettingsComeFromOneSource(t *testing.T) {
	want := billing.Segments{Output: 321, Overshoot: 7 * time.Second}
	policy := billing.NewSegmentPolicy(want)
	gate := gateway.DefaultConfig()
	if got := routingConfig(callDeps{gate: gate}, policy).Segments; got != policy {
		t.Errorf("routing's policy = %p, want the same policy billing gets %p", got, policy)
	}
	if got := ledgerConfig(gate, policy, nil, nil, nil).Segments; got != policy {
		t.Errorf("billing's policy = %p, want the same policy routing reads %p", got, policy)
	}
}
