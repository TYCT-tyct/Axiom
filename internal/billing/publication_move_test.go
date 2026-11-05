package billing

import (
	"fmt"
	"math/big"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

func TestPriceMove(t *testing.T) {
	text := func(r *big.Rat) string {
		if r == nil {
			return ""
		}
		return r.RatString()
	}
	raw := func(input, output string) jsonx.Raw {
		return jsonx.Raw(fmt.Sprintf(`{"v":1,"currency":"USD","rates":{"input":%q,"output":%q}}`, input, output))
	}
	schedule := func(input, output string) catalog.Schedule {
		t.Helper()
		s, err := catalog.ParseSchedule(raw(input, output))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := uuid.NewV7(), uuid.NewV7()
	head := new(uuid.NewV7())
	for _, c := range []struct {
		name           string
		p              Publication
		change, margin string // "" is nil
	}{
		{"a shared price", Publication{
			Current: head, CurrentSchedule: raw("0.000001", "0.000002"), Schedule: raw("0.0000011", "0.000002"),
			Cost: raw("0.0000005", "0.000001"),
		}, "1/10", "1/2"},
		{"no current version", Publication{Schedule: raw("0.000001", "0.000002"), Cost: raw("0.0000005", "0.000001")}, "", "1/2"},
		{"no cost", Publication{Current: head, CurrentSchedule: raw("0.000001", "0.000002"), Schedule: raw("0.000001", "0.000002")}, "0", ""},
		// Each endpoint moves from its own price and keeps its margin over its own cost; the model envelope hides both.
		{"endpoint prices", Publication{
			Current: head, CurrentSchedule: raw("0.000002", "0.000002"), Schedule: raw("0.000002", "0.000002"),
			CurrentEndpoints: EndpointPrices{a: schedule("0.000001", "0.000001"), b: schedule("0.000002", "0.000002")},
			Endpoints:        EndpointPrices{a: schedule("0.0000015", "0.000001"), b: schedule("0.000002", "0.000002")},
			Costs:            EndpointPrices{a: schedule("0.000001", "0.0000009"), b: schedule("0.000001", "0.000001")},
			Cost:             raw("0.000001", "0.000001"),
		}, "1/2", "1/10"},
		{"an endpoint not priced now", Publication{
			Current: head, CurrentSchedule: raw("0.000001", "0.000001"),
			CurrentEndpoints: EndpointPrices{a: schedule("0.000001", "0.000001")}, Schedule: raw("0.000001", "0.000001"),
			Endpoints: EndpointPrices{a: schedule("0.000001", "0.000001"), b: schedule("0.000001", "0.000001")},
			Costs:     EndpointPrices{a: schedule("0.0000005", "0.0000005")},
		}, "", ""},
		{"a shared price after endpoint prices", Publication{
			Current: head, CurrentSchedule: raw("0.000002", "0.000002"), Schedule: raw("0.000002", "0.000002"),
			CurrentEndpoints: EndpointPrices{a: schedule("0.000001", "0.000002"), b: schedule("0.000002", "0.000002")},
			Cost:             raw("0.000001", "0.000001"),
		}, "1", "1/2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			change, margin, err := PriceMove(c.p)
			if err != nil {
				t.Fatal(err)
			}
			if text(change) != c.change || text(margin) != c.margin {
				t.Fatalf("change %q margin %q, want %q and %q", text(change), text(margin), c.change, c.margin)
			}
		})
	}
}
