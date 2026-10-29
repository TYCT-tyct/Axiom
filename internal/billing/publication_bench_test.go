//go:build load

package billing

import (
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// Measure both the policy-lock transaction and each node's published-price read at the documented scale.
func BenchmarkAudienceRefresh(b *testing.B) {
	for _, target := range []string{"model", "all", "retired"} {
		b.Run(target, func(b *testing.B) {
			f := audienceRefreshFixture(b, target)
			for _, phase := range []string{"refresh", "read"} {
				b.Run(phase, func(b *testing.B) { benchmarkAudiencePhase(b, f, phase) })
			}
		})
	}
}

func benchmarkAudiencePhase(b *testing.B, f *priceFixture, phase string) {
	snap := f.catalog.snapshot()
	b.ReportAllocs()
	for b.Loop() {
		err := f.tx(func(tx pg.Tx) error {
			var err error
			if phase == "refresh" {
				_, err = RefreshPrices(b.Context(), tx, f.site, usd, snap, f.costs)
			} else {
				_, err = PublishedPrices(b.Context(), tx, f.site)
			}
			return err
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func audienceRefreshFixture(b *testing.B, target string) *priceFixture {
	f := newPrices(b)
	models, audiences := 500, 200
	if target == "all" {
		models, audiences = 100, 10
	}
	for i := range models - 1 {
		model := f.catalog.model(fmt.Sprintf("acme/price-%d", i))
		f.costs = append(f.costs, priceCost(b, model, "0.0000005"))
	}
	for range audiences {
		selector := Selector{Kind: target, ID: f.model.String()}
		if target != "model" {
			selector = Selector{Kind: "all"}
		}
		in := rule(selector, faced("1"))
		in.Audience = Selector{Kind: "account", ID: accounts.AccountID(uuid.NewV7()).TypeID()}
		in.Enabled = target != "retired"
		if _, err := f.create(in); err != nil {
			b.Fatal(err)
		}
	}
	f.refresh()
	return f
}
