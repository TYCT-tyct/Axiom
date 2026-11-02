package catalog

import (
	"testing"
	"uuid"
)

// TestOfficialName: a provider key must send the vendor's own model name, not the catalog's author-prefixed slug. A
// dated model takes its date-bearing name; a floating one takes the short name; catalog-only forms are not chosen.
func TestOfficialName(t *testing.T) {
	t.Parallel()
	dated, floating := ModelID(uuid.NewV7()), ModelID(uuid.NewV7())
	s := &Snapshot{
		models: map[ModelID]Model{
			dated:    {ID: dated, VersionBasis: VersionDated},
			floating: {ID: floating, VersionBasis: VersionFloating},
		},
		names: map[string]nameEntry{
			"gpt-4o-2024-08-06":      {name: "gpt-4o-2024-08-06", model: dated, public: true},
			"gpt-4o":                 {name: "gpt-4o", model: dated, public: true},
			"openai/gpt-4o":          {name: "openai/gpt-4o", model: dated, public: true},
			"gpt-4o@20240806":        {name: "gpt-4o@20240806", model: dated, public: true},
			"gpt-4o-mini":            {name: "gpt-4o-mini", model: floating, public: true},
			"gpt-4o-mini-2024-07-18": {name: "gpt-4o-mini-2024-07-18", model: floating, public: true},
			"gpt-4o-mini:free":       {name: "gpt-4o-mini:free", model: floating, public: true},
		},
	}
	if got, ok := s.OfficialName(dated); !ok || got != "gpt-4o-2024-08-06" {
		t.Errorf("dated name = %q, %v, want gpt-4o-2024-08-06", got, ok)
	}
	if got, ok := s.OfficialName(floating); !ok || got != "gpt-4o-mini" {
		t.Errorf("floating name = %q, %v, want gpt-4o-mini", got, ok)
	}
	if _, ok := s.OfficialName(ModelID(uuid.NewV7())); ok {
		t.Error("an unknown model has a name")
	}
}
