package routing_test

import (
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/routing"
)

// storedRoute is the JSON the call log keeps, decoded for the assertions.
type storedRoute struct {
	Excluded []struct {
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
		Route:      ids(routing.MaxEntries),
		Attempts:   attempts(platform.MaxAttemptSeq),
		Unmeasured: true,
	}
	for i := 0; i < 1000; i++ {
		record.Excluded = append(record.Excluded, routing.Excluded{
			Endpoint: routing.EndpointID(uuid.NewV7()),
			Reason:   routing.Reason(i%len(reasonNames) + 1),
		})
	}
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

// maxRecorded mirrors routing's unexported cap; the test names the number it expects in the stored route.
const maxRecorded = 16

// reasonNames are all the exclusion reasons, so the summary map has every key.
var reasonNames = []string{
	"no_candidates", "model_not_allowed", "disabled", "breaker_open", "supplier_balance", "missing_capability",
	"context_too_small", "format_not_served", "service_tier_not_served", "preset_filter", "override_filter",
	"not_verified", "no_price", "no_egress", "retry_budget", "full", "unknown_item", "cooling_down", "unquotable_cost",
}

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
