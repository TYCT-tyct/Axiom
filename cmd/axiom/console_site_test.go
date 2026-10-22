package main

import (
	"net/http"
	"testing"
)

func TestSiteAPIBase(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, statement string
		present         bool
		want            any
	}{
		{"enabled API domain", "SELECT 1", true, "https://api.main.example"},
		{"no enabled API domain", "UPDATE accounts.site_domains SET disabled_at = now() WHERE surface = 'api'", false, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newConsoleFixture(t)
			f.run(tt.statement)
			f = f.restarted()
			var body map[string]any
			rec := f.do(http.MethodGet, "/site", "")
			expect(t, "the site", rec, http.StatusOK, "")
			decode(t, rec, &body)
			value, present := body["api_base_url"]
			if present != tt.present || value != tt.want {
				t.Errorf("API base = %v (present %t), want %v (present %t)", value, present, tt.want, tt.present)
			}
		})
	}
}
