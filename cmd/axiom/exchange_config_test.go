package main

import (
	"strings"
	"testing"

	"axiom/internal/platform/pg"
)

func TestExchangeSourceSetting(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ source, want string }{{"", "manual"}, {"manual", "manual"}, {"ecb", "ecb"}, {"misspelled", ""}} {
		t.Run(tc.source, func(t *testing.T) {
			cfg, err := readSettings([]string{
				pg.DatabaseURLVar + "=postgres://unused:unused@example.com/test",
				masterKeyFileVar + "=unused", keyHashFileVar + "=unused", identityKeyFileVar + "=unused",
				"AXIOM_EXCHANGE_SOURCE=" + tc.source,
			})
			if tc.want == "" {
				if err == nil || !strings.Contains(err.Error(), "AXIOM_EXCHANGE_SOURCE: want manual or ecb") {
					t.Fatalf("error: %v", err)
				}
				return
			}
			if err != nil || cfg.exchangeSource != tc.want {
				t.Fatalf("source: %q %v", cfg.exchangeSource, err)
			}
		})
	}
}
