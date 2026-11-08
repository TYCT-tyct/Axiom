package accounts

import (
	"maps"
	"slices"
	"testing"

	"axiom/internal/platform/consoleapi"
)

// TestPresetMembersKeepTheirContract: the console contract lists the members a preset may hold (PresetConfig), and
// saving refuses any other, so the list must be the one ValidatePresetConfig allows.
func TestPresetMembersKeepTheirContract(t *testing.T) {
	t.Parallel()
	doc, err := consoleapi.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	allowed := slices.Sorted(maps.Keys(presetFields))
	if named := slices.Sorted(maps.Keys(doc.Components.Schemas["PresetConfig"].Value.Properties)); !slices.Equal(allowed, named) {
		t.Errorf("a preset may hold %v, the contract names %v", allowed, named)
	}
}
