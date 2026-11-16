package catalog

import (
	"errors"
	"testing"
)

func TestAllowFileParse(t *testing.T) {
	t.Parallel()
	withItem, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","file_parse":"0.002"}}`))
	if err != nil {
		t.Fatal(err)
	}
	token, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1","output":"2"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Catalog{}).AllowSchedule(withItem); !errors.Is(err, ErrFileParse) {
		t.Errorf("a file_parse price while off: %v, want ErrFileParse", err)
	}
	if err := (&Catalog{cfg: Config{FileParse: true}}).AllowSchedule(withItem); err != nil {
		t.Errorf("a file_parse price while on: %v", err)
	}
	if err := (&Catalog{}).AllowSchedule(token); err != nil {
		t.Errorf("a token price while off: %v", err)
	}
}

func TestFileSearchSpecRequiresItsItem(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"declared", `{"items":["input","file_search"],"tiers":["standard"],"abilities":["file_search"],"max_uses":{"file_search":3}}`, true},
		{"missing item", `{"items":["input"],"tiers":["standard"],"abilities":["file_search"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSavedSpec([]byte(tc.body))
			if (err == nil) != tc.valid {
				t.Fatalf("parseSavedSpec: %v, want valid %t", err, tc.valid)
			}
		})
	}
}
