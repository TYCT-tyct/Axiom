package syncer

import (
	"testing"
	"uuid"

	"axiom/internal/catalog"
)

// names is a catalog of the names a table gives, each with the model it stands for.
type names map[string]struct {
	model catalog.ModelID
	exact bool
}

func (n names) Recognize(name string) (catalog.Recognition, bool) {
	e, ok := n[name]
	return catalog.Recognition{Model: e.model, Exact: e.exact}, ok
}

// Model knows the models of the table by their status, which is the model's id's last byte.
func (names) Model(id catalog.ModelID) (catalog.Model, bool) {
	switch id[15] {
	case 1:
		return catalog.Model{ID: id, Status: catalog.StatusListed}, true
	case 2:
		return catalog.Model{ID: id, Status: catalog.StatusRetired}, true
	}
	return catalog.Model{}, false
}

func id(last byte) catalog.ModelID { return catalog.ModelID(uuid.UUID{15: last}) }

// TestMapUpstream: a name maps by the supplier's alias, then by the catalog's recognition; what no step maps, or maps to a
// model that is not on sale, stays unmapped and so is kept out of routing.
func TestMapUpstream(t *testing.T) {
	t.Parallel()
	c := names{
		"claude-sonnet-4-5-20250929": {id(1), true},
		"claude-sonnet-4-5":          {id(1), false}, // undated: only a candidate
		"gpt-3":                      {id(2), true},  // retired
	}
	alias := id(1)
	retired := id(2)
	unknown := id(3)
	for name, tc := range map[string]struct {
		alias *catalog.ModelID
		name  string
		want  Mapping
	}{
		"dated name":                  {nil, "claude-sonnet-4-5-20250929", Mapping{Model: id(1), Mapped: true}},
		"undated name is a candidate": {nil, "claude-sonnet-4-5", Mapping{Model: id(1), Mapped: true, Unverified: true}},
		"alias":                       {&alias, "sonnet:free", Mapping{Model: id(1), Mapped: true}},
		"alias beats recognition":     {&alias, "gpt-3", Mapping{Model: id(1), Mapped: true}},
		"unknown name":                {nil, "mystery-model", Mapping{Why: Unmapped}},
		"suffix":                      {nil, "claude-sonnet-4-5:free", Mapping{Why: Unmapped}},
		"retired model":               {nil, "gpt-3", Mapping{Why: NotListed}},
		"alias to a retired model":    {&retired, "x", Mapping{Why: NotListed}},
		"alias to a missing model":    {&unknown, "x", Mapping{Why: Unmapped}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := MapUpstream(c, tc.alias, tc.name); got != tc.want {
				t.Errorf("MapUpstream(%q) = %+v, want %+v", tc.name, got, tc.want)
			}
			// what is not mapped is not routable
			facts := EndpointFacts{Normal: true, Wallet: true, Balance: "10", Reserve: "1", GroupSelected: true, Mapped: MapUpstream(c, tc.alias, tc.name).Mapped, CostKnown: true, UsableCredentials: 1}
			if got, want := facts.Check(), map[bool]Reason{true: Routable, false: Unmapped}[tc.want.Mapped]; got != want {
				t.Errorf("Check = %q, want %q", got, want)
			}
		})
	}
}
