package websearch

import (
	"errors"
	"testing"

	"axiom/internal/platform/secret"
)

func TestSettingsKeyFor(t *testing.T) {
	key := func(s string) *string { return &s }
	old := Config{URL: "https://search.example.com", Key: secret.New("saved")}
	for name, tc := range map[string]struct {
		settings Settings
		old      Config
		want     string
		err      bool
	}{
		"same backend, key omitted":  {Settings{URL: old.URL}, old, "saved", false},
		"same backend, key given":    {Settings{URL: old.URL, Key: key("new")}, old, "new", false},
		"same backend, no saved key": {Settings{URL: old.URL}, Config{URL: old.URL}, "", false},
		"new backend, key given":     {Settings{URL: "https://other.example.com", Key: key("new")}, old, "new", false},
		"new backend, key omitted":   {Settings{URL: "https://other.example.com"}, old, "", true},
		"new backend, no saved key":  {Settings{URL: "https://other.example.com"}, Config{}, "", false},
		"cleared":                    {Settings{Key: key("left")}, old, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := tc.settings.keyFor(tc.old)
			if (err != nil) != tc.err || !errors.Is(err, ErrInvalidConfig) && tc.err {
				t.Fatalf("keyFor() error = %v, want invalid config: %v", err, tc.err)
			}
			if !tc.err && got != tc.want {
				t.Fatalf("keyFor() = %q, want %q", got, tc.want)
			}
		})
	}
}
