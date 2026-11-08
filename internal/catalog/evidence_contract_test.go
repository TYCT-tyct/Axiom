package catalog_test

import (
	"maps"
	"slices"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
)

// TestOfficialEvidenceKeepsItsContract: official evidence is read strictly as PageEvidence, every member required, so
// the admin contract's OfficialPriceEvidence must name and require exactly its members.
func TestOfficialEvidenceKeepsItsContract(t *testing.T) {
	t.Parallel()
	doc, err := adminapi.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	body, err := jsonx.MarshalCanonical(catalog.PageEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	var members map[string]jsonx.Raw
	if err := jsonx.Unmarshal(body, &members); err != nil {
		t.Fatal(err)
	}
	read := slices.Sorted(maps.Keys(members))
	schema := doc.Components.Schemas["OfficialPriceEvidence"].Value
	if named := slices.Sorted(maps.Keys(schema.Properties)); !slices.Equal(read, named) {
		t.Errorf("official evidence has %v, the contract names %v", read, named)
	}
	if required := slices.Sorted(slices.Values(schema.Required)); !slices.Equal(read, required) {
		t.Errorf("official evidence needs %v, the contract requires %v", read, required)
	}
}
