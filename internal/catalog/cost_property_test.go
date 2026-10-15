package catalog_test

import (
	"encoding/json/v2"
	"errors"
	"math/big"
	"math/rand/v2"
	"testing"

	"axiom/internal/catalog"
)

// FuzzCost checks Cost against an independent math/big model of README.md「条件」 and「计费项与用量」: the highest tier
// whose threshold the basis sum strictly exceeds, a tier's prices over the base prices, reasoning at the output price
// when it has none of its own, ErrUnpriced for any other item with usage and no price, and the exact total. Each input
// seeds a batch of random schedules and usages, so the committed seeds alone exercise thousands of cases.
func FuzzCost(f *testing.F) {
	for seed := range uint64(4) {
		f.Add(seed, ^seed)
	}
	f.Fuzz(func(t *testing.T, seed1, seed2 uint64) {
		r := rand.New(rand.NewPCG(seed1, seed2)) //nolint:gosec // G404: test data, not a secret
		for range 500 {
			c := randomCase(r)
			got, err := mustParse(t, c.json(t)).Cost(mustUsage(t, c.usage))
			want, priced := c.cost()
			if !priced && !errors.Is(err, catalog.ErrUnpriced) || priced && (err != nil || got.Rat().Cmp(want) != 0) {
				t.Fatalf("Cost of %v under %s = %s, %v; want %v, priced %v",
					c.usage, c.json(t), got.Rat().FloatString(15), err, want, priced)
			}
		}
	})
}

// items lists every billing item, the input side first.
var items = [...]catalog.Item{in, cacheRead, cacheWrite, cacheWrite1h, audioIn, imageIn, out, reasoning, audioOut, imageOut, webSearch}

type (
	prices   map[catalog.Item]*big.Rat
	costCase struct {
		base  prices
		basis []catalog.Item
		tiers []tierCase // thresholds strictly increase
		usage counts
	}
	tierCase struct {
		above int64
		rates prices
	}
)

// randomCase draws counts and thresholds at one of two scales: up to 10¹⁰, the whole domain, or up to 30, where the
// basis sum often lands exactly on a threshold.
func randomCase(r *rand.Rand) costCase {
	scale := int64(1e10)
	if r.IntN(2) == 0 {
		scale = 30
	}
	c := costCase{base: randomPrices(r), usage: counts{}}
	for _, it := range items[:6] {
		if r.IntN(2) == 0 {
			c.basis = append(c.basis, it)
		}
	}
	if len(c.basis) == 0 {
		c.basis = items[:1]
	}
	above := r.Int64N(2*scale) - 1
	for range r.IntN(4) {
		above += 1 + r.Int64N(scale)
		tier := tierCase{above, randomPrices(r)}
		for len(tier.rates) == 0 {
			tier.rates = randomPrices(r)
		}
		c.tiers = append(c.tiers, tier)
	}
	for _, it := range items {
		if r.IntN(2) == 0 {
			c.usage[it] = randomAmount(r, scale)
		}
	}
	return c
}

// randomPrices prices each item with even odds, at prices from 0 to 1000 that favour the smallest and the largest.
func randomPrices(r *rand.Rand) prices {
	p := prices{}
	for _, it := range items {
		if r.IntN(2) == 0 {
			p[it] = big.NewRat(randomAmount(r, 1e18), 1e15)
		}
	}
	return p
}

// randomAmount returns 0 … limit, favouring both ends.
func randomAmount(r *rand.Rand, limit int64) int64 {
	switch r.IntN(4) {
	case 0:
		return r.Int64N(3)
	case 1:
		return limit - r.Int64N(3)
	default:
		return r.Int64N(limit + 1)
	}
}

func (c costCase) json(tb testing.TB) string {
	tb.Helper()
	doc := map[string]any{"v": 1, "currency": "USD", "rates": c.base.json()}
	if len(c.tiers) > 0 {
		var tiers []map[string]any
		for _, tier := range c.tiers {
			tiers = append(tiers, map[string]any{"above": tier.above, "rates": tier.rates.json()})
		}
		basis := make([]string, len(c.basis))
		for i, it := range c.basis {
			basis[i] = it.String()
		}
		doc["context_tiers"] = map[string]any{"basis": basis, "method": "whole", "tiers": tiers}
	}
	b, err := json.Marshal(doc)
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

func (p prices) json() map[string]string {
	m := map[string]string{}
	for it, price := range p {
		m[it.String()] = price.FloatString(15)
	}
	return m
}

// cost is the model: the exact cost, or false when an item with usage has no price.
func (c costCase) cost() (*big.Rat, bool) {
	var length int64
	for _, it := range c.basis {
		length += c.usage[it]
	}
	tier := prices{}
	for _, t := range c.tiers {
		if length > t.above {
			tier = t.rates
		}
	}
	price := func(it catalog.Item) *big.Rat {
		if p, ok := tier[it]; ok {
			return p
		}
		return c.base[it]
	}
	total := new(big.Rat)
	for it, n := range c.usage {
		p := price(it)
		if p == nil && it == reasoning {
			p = price(out)
		}
		if p == nil && n > 0 {
			return nil, false
		}
		if p != nil {
			total.Add(total, new(big.Rat).Mul(p, big.NewRat(n, 1)))
		}
	}
	return total, true
}
