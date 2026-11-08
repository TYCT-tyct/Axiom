package routing

import (
	"math"
	"testing"
	"uuid"

	"github.com/getkin/kin-openapi/openapi3"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
)

// TestRecordKeepsItsContract: the stored record is what the admin and operations contracts hand out as a call's
// `route` (CallRoute), so every member it can write, with every reason, override and the largest scope and action,
// must pass that schema; the schema refuses members it does not name, so a member added here and not there fails.
func TestRecordKeepsItsContract(t *testing.T) {
	t.Parallel()
	endpoint := EndpointID(uuid.NewV7())
	var records []Record
	for reason := NoCandidates; int(reason) < len(reasonNames); reason++ {
		// One more exclusion than are named, so excluded_more is written too.
		excluded := make([]Excluded, maxRecordedExclusions+1)
		for i := range excluded {
			excluded[i] = Excluded{Endpoint: endpoint, Reason: reason}
		}
		records = append(records, Record{
			Preset: PresetID(uuid.NewV7()), PresetVersion: 1, Overrides: math.MaxUint16, Unmeasured: true,
			Excluded: excluded, Route: []EndpointID{endpoint},
			Attempts: []Attempt{{Endpoint: endpoint, Failed: true, Scope: ScopeSupplier, Action: ReturnToClient}, {Endpoint: endpoint}},
		})
	}
	records = append(records, Record{})
	for name, spec := range map[string]func() (*openapi3.T, error){"admin": adminapi.GetSwagger, "operations": operationsapi.GetSwagger} {
		doc, err := spec()
		if err != nil {
			t.Fatal(err)
		}
		schema := doc.Components.Schemas["CallRoute"].Value
		for _, r := range records {
			body, err := r.JSON()
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := jsonx.Unmarshal(body, &value); err != nil {
				t.Fatal(err)
			}
			if err := schema.VisitJSON(value); err != nil {
				t.Errorf("the %s contract's CallRoute refuses %s: %v", name, body, err)
			}
		}
	}
}
