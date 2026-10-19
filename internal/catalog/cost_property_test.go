package catalog_test

import (
	"math/big"
	"math/rand/v2"
	"slices"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// FuzzCost checks Cost against an independent math/big model of README.md「条件」 and「计费项与用量」: the service tier's
// own prices, or every item missing when the schedule has none for it; the highest tier whose threshold the basis sum
// (images counted as input when the basis names input but not image_in) strictly exceeds, a tier's prices over the service tier's base prices, reasoning at the output price when it has
// none of its own, every other item with usage and no price reported as missing, and the exact total of the priced
// items, whose tier the whole usage chose, missing items included. Missing must cover whatever Cost finds missing, so a
// schedule that passes the listing threshold never meets an unpriced item. Each input seeds a batch of random
// schedules and usages, so the committed seeds alone exercise thousands of cases.
func FuzzCost(f *testing.F) {
	for seed := range uint64(4) {
		f.Add(seed, ^seed)
	}
	f.Fuzz(func(t *testing.T, seed1, seed2 uint64) {
		r := rand.New(rand.NewPCG(seed1, seed2)) //nolint:gosec // G404: test data, not a secret
		for range 500 {
			c := randomCase(r)
			s := mustParse(t, c.json(t))
			got, missing, err := s.Cost(mustTierUsage(t, c.tier, c.usage))
			want, wantMissing := c.cost()
			if err != nil || got.Rat().Cmp(want) != 0 || !slices.Equal(members(missing), wantMissing) {
				t.Fatalf("Cost of %v at %v under %s = %s, missing %v, %v; want %s, missing %v",
					c.usage, c.tier, c.json(t), got.Rat().FloatString(15), members(missing), err, want.FloatString(15),
					wantMissing)
			}
			if m := s.Missing(c.reportable()); !covers(m, c.tier, missing) {
				t.Fatalf("Missing under %s = %v, %v; Cost of %v at %v found %v missing",
					c.json(t), members(m.Items), serviceTierMembers(m.ServiceTiers), c.usage, c.tier, members(missing))
			}
		}
	})
}

// items lists every billing item in enumeration order.
var items = [...]catalog.Item{in, cacheRead, cacheWrite, cacheWrite1h, audioIn, imageIn, out, reasoning, audioOut, imageOut, webSearch, codeExec}

// allServiceTiers lists every service tier in enumeration order, so a seed draws the same case every run.
var allServiceTiers = [...]catalog.ServiceTier{
	catalog.ServiceTierStandard, catalog.ServiceTierFlex, catalog.ServiceTierPriority, catalog.ServiceTierFast,
}

type (
	prices   map[catalog.Item]*big.Rat
	costCase struct {
		basis  []catalog.Item
		above  []int64                       // strictly increasing
		levels map[catalog.ServiceTier]level // always has the standard tier
		tier   catalog.ServiceTier
		usage  counts
	}
	level struct {
		base  prices
		tiers []prices // one per threshold
	}
)

// reportable is what the case's upstream reported: the items it used and its service tier.
func (c costCase) reportable() catalog.Reportable {
	var r catalog.Reportable
	for it, n := range c.usage {
		if n > 0 {
			r.Items.Add(it)
		}
	}
	if c.tier != catalog.ServiceTierStandard {
		r.ServiceTiers.Add(c.tier)
	}
	return r
}

// covers reports whether m names every item Cost found missing at the service tier: the item itself at the standard
// tier, the service tier otherwise.
func covers(m catalog.Reportable, st catalog.ServiceTier, missing catalog.ItemSet) bool {
	if st == catalog.ServiceTierStandard {
		return missing&^m.Items == 0
	}
	return missing == 0 || m.ServiceTiers.Has(st)
}

// randomCase draws counts and thresholds at one of two scales: up to 10¹⁰, the whole domain, or up to 30, where the
// basis sum often lands exactly on a threshold.
func randomCase(r *rand.Rand) costCase {
	scale := int64(1e10)
	if r.IntN(2) == 0 {
		scale = 30
	}
	c := costCase{tier: allServiceTiers[r.IntN(len(allServiceTiers))], usage: counts{}}
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
		c.above = append(c.above, above)
	}
	c.levels = randomLevels(r, len(c.above))
	for _, it := range items {
		if r.IntN(2) == 0 {
			c.usage[it] = randomAmount(r, scale)
		}
	}
	return c
}

// randomLevels prices the standard tier and each other service tier with even odds.
func randomLevels(r *rand.Rand, thresholds int) map[catalog.ServiceTier]level {
	levels := map[catalog.ServiceTier]level{}
	for _, st := range allServiceTiers {
		if st == catalog.ServiceTierStandard || r.IntN(2) == 0 {
			l := level{base: randomPrices(r)}
			for range thresholds {
				l.tiers = append(l.tiers, randomPrices(r))
			}
			levels[st] = l
		}
	}
	return levels
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
	standard := c.levels[catalog.ServiceTierStandard]
	doc := map[string]any{"v": 1, "currency": "USD", "rates": standard.base.json()}
	if len(c.above) > 0 {
		basis := make([]string, len(c.basis))
		for i, it := range c.basis {
			basis[i] = it.String()
		}
		doc["context_tiers"] = map[string]any{"basis": basis, "method": "whole", "tiers": c.tiersJSON(standard)}
	}
	levels := map[string]any{}
	for st, l := range c.levels {
		if st != catalog.ServiceTierStandard {
			levels[st.String()] = map[string]any{"rates": l.base.json(), "context_tiers": c.tiersJSON(l)}
		}
	}
	doc["service_tiers"] = levels
	b, err := jsonx.MarshalCanonical(doc)
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

func (c costCase) tiersJSON(l level) []map[string]any {
	tiers := []map[string]any{}
	for i, p := range l.tiers {
		tiers = append(tiers, map[string]any{"above": c.above[i], "rates": p.json()})
	}
	return tiers
}

func (p prices) json() map[string]string {
	m := map[string]string{}
	for it, price := range p {
		m[it.String()] = price.FloatString(15)
	}
	return m
}

// cost is the model: the exact cost of the priced items and, in enumeration order, the items with usage and no price.
func (c costCase) cost() (*big.Rat, []catalog.Item) {
	var length int64
	for _, it := range c.basis {
		length += c.usage[it]
	}
	if slices.Contains(c.basis, in) && !slices.Contains(c.basis, imageIn) {
		length += c.usage[imageIn] // images were part of input before they were split out
	}
	l, offered := c.levels[c.tier]
	tier := prices{}
	for i, above := range c.above {
		if offered && length > above {
			tier = l.tiers[i]
		}
	}
	price := func(it catalog.Item) *big.Rat {
		if p, ok := tier[it]; ok {
			return p
		}
		return l.base[it]
	}
	total := new(big.Rat)
	var missing []catalog.Item
	for _, it := range items {
		n := c.usage[it]
		p := price(it)
		if p == nil {
			switch it {
			case reasoning, imageOut:
				p = price(out)
			case imageIn:
				p = price(in)
			}
		}
		switch {
		case p != nil:
			total.Add(total, new(big.Rat).Mul(p, big.NewRat(n, 1)))
		case n > 0:
			missing = append(missing, it)
		}
	}
	return total, missing
}
