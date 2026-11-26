package adapter_test

import (
	"errors"
	"maps"
	"testing"

	"axiom/internal/suppliers/internal/adapter"
)

// twoForms is a cloud platform whose credential is an access key or an API key, with a setting every form takes.
var twoForms = adapter.Type{
	Kind: "two", Mode: adapter.ModeCloud, Name: "Two", Group: adapter.GroupCloud, Families: []adapter.Family{adapter.FamilyAnthropic},
	Entries: []adapter.PriceEntry{{ID: "global", Origin: "https://two.example", Currency: "USD"}},
	Forms: []adapter.Form{
		{Name: "access_key", Fields: []adapter.Field{{Name: "access_key_id", Required: true, Pattern: `^[A-Z0-9]{16}$`}, {Name: "secret_access_key", Required: true}}},
		{Name: "api_key", Fields: []adapter.Field{{Name: "api_key", Required: true}}},
	},
	Fields: []adapter.Field{{Name: "api_version", Pattern: `^[0-9]{4}$`}, {Name: "regions", Required: true}},
}

// TestReadTakesOneFormWhole: a request fills exactly the fields of one form and the type's own, the required ones and
// each in its pattern; the secrets and the settings come back apart, and the settings name the form when the type has
// more than one, so a stored row says which secret it holds. Settings read back are valid; others are not.
func TestReadTakesOneFormWhole(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		form     string
		values   map[string]string
		field    string // the field at fault; empty for none
		secrets  map[string]string
		settings adapter.Settings
	}{
		{
			"the default form", "",
			map[string]string{"access_key_id": "AKIAFAKE00000001", "secret_access_key": "s", "api_version": "2026"},
			"",
			map[string]string{"access_key_id": "AKIAFAKE00000001", "secret_access_key": "s"},
			adapter.Settings{"form": "access_key", "api_version": "2026"},
		},
		{"another form", "api_key", map[string]string{"api_key": "k"}, "", map[string]string{"api_key": "k"}, adapter.Settings{"form": "api_key"}},
		{"half of one form", "", map[string]string{"access_key_id": "AKIAFAKE00000001"}, "secret_access_key", nil, nil},
		{"a field of another form", "api_key", map[string]string{"api_key": "k", "secret_access_key": "s"}, "secret_access_key", nil, nil},
		{"a field out of its pattern", "api_key", map[string]string{"api_key": "k", "api_version": "26"}, "api_version", nil, nil},
		{"a form the type has not", "token", map[string]string{"api_key": "k"}, "form", nil, nil},
	} {
		_, secrets, settings, err := twoForms.Read(tc.form, tc.values)
		var fieldErr *adapter.FieldError
		if tc.field != "" {
			if !errors.As(err, &fieldErr) || fieldErr.Field != tc.field {
				t.Errorf("%s: %v, want a fault in %s", tc.name, err, tc.field)
			}
			continue
		}
		if err != nil || !maps.Equal(secrets, tc.secrets) || !maps.Equal(settings, tc.settings) || !twoForms.Valid(settings) {
			t.Errorf("%s: %v %v %v", tc.name, secrets, settings, err)
		}
	}
	for _, s := range []adapter.Settings{nil, {"api_version": "2026"}, {"form": "api_key", "organization": "o"}, {"form": "token"}} {
		if twoForms.Valid(s) {
			t.Errorf("settings %v are valid", s)
		}
	}
}

// TestCheckRefusesWhatARequestCouldNotBeCheckedAgainst: the registry starts only with descriptions whose entries, fields
// and forms a request can be checked against.
func TestCheckRefusesWhatARequestCouldNotBeCheckedAgainst(t *testing.T) {
	t.Parallel()
	if err := twoForms.Check(); err != nil {
		t.Fatalf("a valid type: %v", err)
	}
	for name, change := range map[string]func(*adapter.Type){
		"an entry named twice": func(t *adapter.Type) { t.Entries = append(t.Entries, t.Entries[0]) },
		"an origin with a path": func(t *adapter.Type) {
			t.Entries = []adapter.PriceEntry{{ID: "x", Origin: "https://two.example/v1", Currency: "USD"}}
		},
		"a field out of the words":   func(t *adapter.Type) { t.Fields = append(t.Fields, adapter.Field{Name: "token"}) },
		"a secret sent in a header":  func(t *adapter.Type) { t.Forms[1].Fields[0].Header = "X-Key" },
		"regions on a direct API":    func(t *adapter.Type) { t.Mode = adapter.ModeDirect },
		"a form without a secret":    func(t *adapter.Type) { t.Forms[1].Fields = nil },
		"a pattern that is not one":  func(t *adapter.Type) { t.Fields[0].Pattern = "(" },
		"no family for its entries":  func(t *adapter.Type) { t.Families = nil },
		"a group the picker has not": func(t *adapter.Type) { t.Group = "other" },
	} {
		bad := twoForms
		bad.Entries = append([]adapter.PriceEntry(nil), twoForms.Entries...)
		bad.Fields = append([]adapter.Field(nil), twoForms.Fields...)
		bad.Forms = []adapter.Form{twoForms.Forms[0], {Name: "api_key", Fields: append([]adapter.Field(nil), twoForms.Forms[1].Fields...)}}
		change(&bad)
		if bad.Check() == nil {
			t.Errorf("%s: checked", name)
		}
	}
}
