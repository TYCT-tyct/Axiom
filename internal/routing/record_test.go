package routing_test

import (
	"math"
	"slices"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/routing"
)

// storedRoute is the JSON the call log keeps, decoded for the assertions.
type storedRoute struct {
	Preset *struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	} `json:"preset"`
	Overrides []string `json:"overrides"`
	Excluded  []struct {
		Endpoint string `json:"endpoint"`
		Reason   string `json:"reason"`
	} `json:"excluded"`
	ExcludedMore    int            `json:"excluded_more"`
	ExcludedReasons map[string]int `json:"excluded_reasons"`
	Route           []string       `json:"route"`
	Attempts        []struct {
		Endpoint string `json:"endpoint"`
	} `json:"attempts"`
}

// TestRecordIsBounded: the largest record a request can produce, one that weighs many candidates as openrouter/auto
// does, with the most route entries and attempts, must fit the call log's route column: 8192 bytes
// of jsonb text, which prints a space after every ':' and ','. The named exclusions stop at the cap; the reason counts
// and the selected route remain complete.
func TestRecordIsBounded(t *testing.T) {
	t.Parallel()
	record := routing.Record{
		Preset: routing.PresetID(uuid.NewV7()), PresetVersion: math.MaxInt64, Overrides: math.MaxUint16,
		Route:      ids(routing.MaxEntries),
		Attempts:   attempts(platform.MaxAttemptSeq),
		Unmeasured: true,
	}
	record.Excluded = excluded(1000)
	body, err := record.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(body) + strings.Count(string(body), ":") + strings.Count(string(body), ","); n > 8192 {
		t.Errorf("route is %d bytes as jsonb text, want at most the column's 8192", n)
	}
	var stored storedRoute
	if err := jsonx.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Excluded) != maxRecorded {
		t.Errorf("%d excluded candidates named, want %d", len(stored.Excluded), maxRecorded)
	}
	if stored.ExcludedMore != 1000-maxRecorded {
		t.Errorf("excluded_more = %d, want %d", stored.ExcludedMore, 1000-maxRecorded)
	}
	total := 0
	for _, n := range stored.ExcludedReasons {
		total += n
	}
	if total != 1000 {
		t.Errorf("the reason counts add up to %d, want all 1000 candidates", total)
	}
	if len(stored.Route) != routing.MaxEntries {
		t.Errorf("route has %d entries, want the %d selected ones", len(stored.Route), routing.MaxEntries)
	}
	if len(stored.Attempts) != platform.MaxAttemptSeq {
		t.Errorf("%d attempts recorded, want %d", len(stored.Attempts), platform.MaxAttemptSeq)
	}
}

// TestRecordNamesThePresetAndTheOverrides: a plan's record says which preset and version routed the call, and names the
// provider members that changed the route on top of it, in a fixed order, and not those written to no effect.
func TestRecordNamesThePresetAndTheOverrides(t *testing.T) {
	t.Parallel()
	p := compiled(t, allowed)
	p.Version = 7
	h := presetHarness(t, p)
	cheapest := routing.Cheapest
	for _, tc := range []struct {
		name string
		over routing.Override
		want []string
	}{
		{"no provider object", routing.Override{}, nil},
		{"members with no effect", routing.Override{OnlyNames: []string{}, Quantizations: nil}, nil},
		{"filters and an order", routing.Override{
			OrderNames: []string{"g1"}, NoFallbacks: true, Sort: &cheapest, Policies: routing.AllPolicies,
			MaxLatencyPercentiles: routing.Cutoffs{Set: 1 << routing.P90}, RequireParameters: true,
		}, []string{"order", "allow_fallbacks", "sort", "data_collection", "preferred_max_latency", "require_parameters"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := presetInput()
			in.Override = tc.over
			body, err := h.plan(in).Record.JSON()
			if err != nil {
				t.Fatal(err)
			}
			var stored storedRoute
			if err := jsonx.Unmarshal(body, &stored); err != nil {
				t.Fatal(err)
			}
			if stored.Preset == nil || stored.Preset.ID != uuid.UUID(pre).String() || stored.Preset.Version != 7 {
				t.Errorf("preset = %+v, want %s at version 7", stored.Preset, uuid.UUID(pre))
			}
			if !slices.Equal(stored.Overrides, tc.want) {
				t.Errorf("overrides = %v, want %v", stored.Overrides, tc.want)
			}
		})
	}
}

// maxRecorded mirrors routing's unexported cap; the test names the number it expects in the stored route.
const maxRecorded = 16

// TestRecordKeepsTheSelectedRoute: a short exclusion list is stored in full, with every reason counted, so a trace can
// still say why each candidate was left out.
func TestRecordKeepsTheSelectedRoute(t *testing.T) {
	t.Parallel()
	route := ids(2)
	excluded := []routing.Excluded{
		{Endpoint: routing.EndpointID(uuid.NewV7()), Reason: routing.Full},
		{Reason: routing.NoCandidates},
	}
	record := routing.Record{Route: route, Excluded: excluded}
	body, err := record.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var stored storedRoute
	if err := jsonx.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Excluded) != 2 || stored.ExcludedMore != 0 {
		t.Errorf("excluded = %+v, more = %d, want both named and no remainder", stored.Excluded, stored.ExcludedMore)
	}
	if stored.ExcludedReasons["full"] != 1 || stored.ExcludedReasons["no_candidates"] != 1 {
		t.Errorf("reason counts = %v, want one of each", stored.ExcludedReasons)
	}
	if len(stored.Route) != len(route) {
		t.Fatalf("route = %v, want %v", stored.Route, route)
	}
	for i, id := range route {
		if stored.Route[i] != uuid.UUID(id).String() {
			t.Errorf("route[%d] = %q, want %q", i, stored.Route[i], uuid.UUID(id).String())
		}
	}
}

func ids(n int) []routing.EndpointID {
	out := make([]routing.EndpointID, n)
	for i := range out {
		out[i] = routing.EndpointID(uuid.NewV7())
	}
	return out
}

func attempts(n int) []routing.Attempt {
	out := make([]routing.Attempt, n)
	for i := range out {
		out[i] = routing.Attempt{
			Endpoint: routing.EndpointID(uuid.NewV7()), Failed: true, Scope: 255, Action: 255,
		}
	}
	return out
}

// excluded is n candidates left out, cycling through every reason.
func excluded(n int) []routing.Excluded {
	out := make([]routing.Excluded, 0, n)
	for reason := routing.NoCandidates; len(out) < n; reason++ {
		if reason > routing.UnquotableCost {
			reason = routing.NoCandidates
		}
		out = append(out, routing.Excluded{Endpoint: routing.EndpointID(uuid.NewV7()), Reason: reason})
	}
	return out
}
