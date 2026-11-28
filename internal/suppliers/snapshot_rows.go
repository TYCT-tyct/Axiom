package suppliers

import (
	"fmt"
	"math/big"
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

// endpointTerms is how calls to an endpoint are made and costed besides its schedule: the precision recorded, and for
// an aggregator's ceiling plan the line's ratio and the provider object its calls carry (ceilingTerms).
type endpointTerms struct {
	quantization Quantization
	ratio        *big.Rat
	provider     []byte
}

// termsOf reads the endpoint's terms from its row, whose schedule parseSchedule has read.
func termsOf(r store.ListRoutableRow, schedules map[uuid.UUID]*catalog.Schedule) (endpointTerms, error) {
	q, err := optionalQuantization(r)
	if err != nil || r.Basis != ceilingBasis {
		return endpointTerms{quantization: q}, err
	}
	ratio, provider, err := ceilingTerms(*schedules[r.CostScheduleID], r.LineRatio)
	if err != nil {
		return endpointTerms{}, fmt.Errorf("endpoint %s: %w", r.EndpointID, err)
	}
	return endpointTerms{quantization: q, ratio: ratio, provider: provider}, nil
}

// relayMode is the mode of a supplier Axiom logs in to: its keys draw on one account there.
const relayMode = "relay"
