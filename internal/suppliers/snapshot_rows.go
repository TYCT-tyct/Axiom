package suppliers

import (
	"fmt"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/suppliers/internal/store"
)

// What Reload reads from an endpoint's row besides the endpoint itself: its cost schedule, its owner and its
// quantization.

// parseSchedule reads the endpoint's cost schedule into schedules, once a schedule.
func parseSchedule(schedules map[uuid.UUID]*catalog.Schedule, r store.ListRoutableRow) error {
	if _, known := schedules[r.CostScheduleID]; known {
		return nil
	}
	sched, err := catalog.ParseSchedule(r.Schedule)
	if err != nil {
		return fmt.Errorf("endpoint %s: its cost schedule %s: %w", r.EndpointID, r.CostScheduleID, err)
	}
	schedules[r.CostScheduleID] = &sched
	return nil
}

// owner is the tenant a nullable column names, the zero id for an endpoint of the platform's own.
func owner(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.UUID{}
	}
	return *id
}

// optionalQuantization parses the level an admin recorded, or the empty level when none is recorded: absence is a level
// no request matches, not a synonym for the level OpenRouter names 'unknown'.
func optionalQuantization(r store.ListRoutableRow) (Quantization, error) {
	if r.Quantization == nil {
		return "", nil
	}
	quantization, err := ParseQuantization(*r.Quantization)
	if err != nil {
		return "", fmt.Errorf("endpoint %s: %w", r.EndpointID, err)
	}
	return quantization, nil
}

// relayMode is the mode of a supplier Axiom logs in to: its keys draw on one account there.
const relayMode = "relay"
