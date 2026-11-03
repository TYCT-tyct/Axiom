package declared

import (
	"math/big"
	"slices"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// TestOfferStateIsBoundToTheDeclaration: a resume position from one declaration must not be applied to another for
// the same kind. Prices are read in the old declaration's scale, so mixing the cached numbers with a new scale would
// misprice every model.
func TestOfferStateIsBoundToTheDeclaration(t *testing.T) {
	t.Parallel()
	first := loadAcme(t)
	spec := acmeSpec(t)
	spec.Offer.Prices.Scale = "1"
	second, err := Compile(spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.offer.fingerprint == second.offer.fingerprint {
		t.Fatal("the changed price scale did not change the declaration fingerprint")
	}
	old := offerState{
		V: offerVersion, Fingerprint: first.offer.fingerprint, GroupsRead: true, PricesRead: true,
		Groups: []offerGroup{{Ref: "g1", GroupValue: "g1"}},
		Prices: map[string]map[string]string{"m": {"input": "2"}},
	}
	got := decodeOffer(encodeOffer(&old), second.offer.fingerprint)
	if got.GroupsRead || got.PricesRead || got.Fingerprint != second.offer.fingerprint {
		t.Fatalf("decoded %+v, want a fresh state for the second declaration", got)
	}
}

// TestCostScalesBeforeTheUnitPriceLimit: the upstream price is per million tokens, so 2000 becomes the valid
// per-token price 0.002 only after scale. Parsing the raw 2000 first would reject the whole model.
func TestCostScalesBeforeTheUnitPriceLimit(t *testing.T) {
	t.Parallel()
	p := offerPlan{prices: &pricePlan{scale: big.NewRat(1, 1_000_000)}}
	schedule, err := p.costOf("m", "1", map[string]map[string]string{"m": {"input": "2000"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, rate := range schedule.View().Rates {
		if rate.Item == catalog.ItemInput && rate.Base {
			if got := rate.Price.String(); got != "0.002" {
				t.Fatalf("input price = %q, want 0.002", got)
			}
			return
		}
	}
	t.Fatal("no input price")
}

// TestOfferInheritsCallsFormats: a declaration may state formats only in calls.formats. Each model then inherits them
// through its group, so persist has a format to project and the models can become callable endpoints.
func TestOfferInheritsCallsFormats(t *testing.T) {
	t.Parallel()
	spec := acmeSpec(t)
	spec.Offer.Groups.Formats = ""
	spec.Offer.Models.Formats = ""
	a, err := Compile(spec)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := assembleOffer(a.offer, offerState{
		V: offerVersion, GroupsRead: true, PricesRead: true,
		Groups: []offerGroup{{Ref: "g1", GroupValue: "g1"}},
		Models: map[string][]offerModel{"g1": {{Name: "m"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Models) != 1 {
		t.Fatalf("groups = %+v", groups)
	}
	want := []protocol.Format{protocol.Chat, protocol.Messages, protocol.Responses}
	if !slices.Equal(groups[0].Models[0].Formats, want) {
		t.Fatalf("formats = %v, want %v", groups[0].Models[0].Formats, want)
	}
}
