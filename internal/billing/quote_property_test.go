package billing

import (
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"axiom/internal/catalog"
)

// FuzzQuoteCoversWorstCase checks a quote against calls it must cover, priced by catalog's Cost: every sampling's
// prompt at most its bound and split at random between the input items the request may produce, each sampling
// priced at its own band; the output up to the first segment plus the allowance, or up to the limit when the entry is
// not segmentable, split between output and reasoning in any sampling; every server tool up to its most uses; one
// service tier the request may be served at. Random prices make a dearer lower band, a cache write or read dearer
// than input and a reasoning price above output's common; the review's counter-example, a cache read at twice the
// input price, runs first every time. Each seed runs a batch, so the committed seeds alone check thousands.
func FuzzQuoteCoversWorstCase(f *testing.F) {
	for seed := range uint64(4) {
		f.Add(seed, ^seed)
	}
	f.Fuzz(func(t *testing.T, seed1, seed2 uint64) {
		r := rand.New(rand.NewPCG(seed1, seed2)) //nolint:gosec // G404: test data, not a secret
		check := func(c quoteCase) {
			q, err := quoteEntry(c.d, c.e, c.s)
			if err != nil {
				t.Fatalf("quote of %+v: %v", c, err)
			}
			for range 20 {
				if cost := c.call(t, r); q.amount.Rat().Cmp(cost) < 0 {
					t.Fatalf("quote %s below a call costing %s: %+v under %s", q.amount.Rat().RatString(), cost.RatString(), c, c.json)
				}
			}
		}
		dearCache := `{"v":1,"currency":"USD","rates":{"input":"1","cache_write":"1","cache_read":"2","output":"1"}}`
		check(quoteCase{
			d: Demand{TextBytes: 80},
			e: Quotable{Price: mustSchedule(t, dearCache), Model: Model{ContextLength: 1000, Reports: 1 << catalog.ItemCacheRead}, MaxOutput: 1},
			s: segments, json: dearCache,
		})
		for range 200 {
			check(randomQuoteCase(t, r, 4000))
		}
	})
}

// FuzzQuoteArithmetic checks a quote against the same rules computed in math/big without bounds, over counts up to
// math.MaxInt64: equal when every count is below math.MaxInt64, otherwise refused, never wrapped, saturated or
// rounded down.
func FuzzQuoteArithmetic(f *testing.F) {
	for seed := range uint64(4) {
		f.Add(seed, ^seed)
	}
	f.Fuzz(func(t *testing.T, seed1, seed2 uint64) {
		r := rand.New(rand.NewPCG(seed1, seed2)) //nolint:gosec // G404: test data, not a secret
		for range 500 {
			c := randomQuoteCase(t, r, math.MaxInt64)
			q, err := quoteEntry(c.d, c.e, c.s)
			want, fits := c.reference(t)
			if fits != (err == nil) || fits && q.amount.Rat().Cmp(want) != 0 {
				t.Fatalf("quote %s, %v; want %s (fits %v): %+v under %s", q.amount.Rat().RatString(), err, want.RatString(), fits, c, c.json)
			}
		}
	})
}

type quoteCase struct {
	d    Demand
	e    Quotable
	s    Segments
	json string
}

var toolItems = [...]catalog.Item{webSearch, codeExec}

// randomQuoteCase draws counts up to limit, often at their edges.
func randomQuoteCase(tb testing.TB, r *rand.Rand, limit int64) quoteCase {
	tb.Helper()
	n := func() int64 {
		switch r.IntN(4) {
		case 0:
			return 0
		case 1:
			return limit
		default:
			return r.Int64N(limit) + 1
		}
	}
	c := quoteCase{json: randomPrices(r)}
	c.e = Quotable{
		Price: mustSchedule(tb, c.json),
		Model: Model{
			ContextLength: max(n(), 1), ImageTokens: n(), PageTokens: n(), FilePages: n() % 8, OutputSpeed: n() % 100_000,
			MaxUses: map[catalog.Item]int64{},
		},
		MaxOutput: n(), Segmentable: r.IntN(2) == 0,
	}
	c.d = Demand{TextBytes: n(), Images: n() % 8, Documents: n() % 8, Choices: n() % 4, Compaction: r.IntN(3) == 0}
	for _, it := range toolItems {
		c.e.Model.MaxUses[it] = n() % 4
		if r.IntN(2) == 0 {
			uses := n() % 4
			if uses == 0 && c.e.Model.MaxUses[it] == 0 {
				uses = 1 // a tool with no limit of its own, on a model with no default, is refused (ErrUnbounded), not quoted
			}
			c.d.ServerTools = append(c.d.ServerTools, ServerTool{Item: it, MaxUses: uses})
		}
	}
	for _, it := range []catalog.Item{cacheWrite, cacheWrite1h} {
		if r.IntN(2) == 0 {
			c.d.InputItems.Add(it)
		}
	}
	if r.IntN(2) == 0 {
		c.e.Model.Reports.Add(catalog.ItemCacheRead)
	}
	c.e.Model.OpenAIUsage = r.IntN(2) == 0
	drawTiers(&c, r)
	c.s = Segments{Output: max(n()%1000, 1), Overshoot: time.Duration(r.Int64N(int64(5*time.Second)) + 1)} // both above 0, or the entry is refused
	if limit == math.MaxInt64 {
		c.s.Overshoot = time.Duration(max(n(), 1)) // so overshoot × speed can pass math.MaxInt64
	}
	return c
}

// pricedItems are the items every random schedule prices at its base, so a quote never meets a missing price.
var pricedItems = [...]string{"input", "cache_read", "cache_write", "cache_write_1h", "output", "reasoning", "web_search", "code_execution"}

// randomPrices is a schedule with one or two context tiers over a random basis, each overriding random prices, and
// sometimes a priority tier; prices run up to 1000 with up to 15 decimals.
func randomPrices(r *rand.Rand) string {
	rates := func(all bool) string {
		var parts []string
		for _, it := range pricedItems {
			if all || r.IntN(2) == 0 {
				p := r.Int64N(1e18 + 1)
				parts = append(parts, fmt.Sprintf("%q:%q", it, new(big.Rat).SetFrac64(p, 1e15).FloatString(15)))
			}
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	var above []int64
	for _, a := range []int64{100, 2000} {
		if len(above) == 0 || r.IntN(2) == 0 {
			above = append(above, a)
		}
	}
	tiers := func() string { // the same thresholds for every service tier, as ParseSchedule requires
		var parts []string
		for _, a := range above {
			parts = append(parts, fmt.Sprintf(`{"above":%d,"rates":%s}`, a, rates(false)))
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	basis := `"input"`
	if r.IntN(2) == 0 {
		basis += `,"cache_write"`
	}
	s := fmt.Sprintf(`{"v":1,"currency":"USD","rates":%s,"context_tiers":{"basis":[%s],"method":"whole","tiers":%s}`,
		rates(true), basis, tiers())
	if r.IntN(2) == 0 {
		s += fmt.Sprintf(`,"service_tiers":{"priority":{"rates":%s,"context_tiers":%s}}`, rates(true), tiers())
	}
	return s + "}"
}

// call draws a call the quote must cover and prices it with Cost, one usage per sampling. Counts stay within Usage's
// bound because randomQuoteCase was drawn with a small limit.
func (c quoteCase) call(tb testing.TB, r *rand.Rand) *big.Rat {
	tb.Helper()
	upTo := func(n int64) int64 {
		if n == 0 || r.IntN(2) == 0 {
			return n
		}
		return r.Int64N(n + 1)
	}
	uses, samplings := serverToolUses(c.d, c.e.Model)
	counts := c.prompts(samplings, upTo)
	output := mul(c.e.MaxOutput, max(c.d.Choices, 1))
	if c.e.Segmentable {
		output = min(output, c.s.Output) + ceilDiv(int64(c.s.Overshoot)*c.e.Model.OutputSpeed, int64(time.Second))
	}
	output = upTo(output)
	reasoning := upTo(output)
	at := r.IntN(len(counts))
	counts[at][catalog.ItemOutput] += output - reasoning
	counts[at][catalog.ItemReasoning] += reasoning
	for it, n := range uses {
		counts[r.IntN(len(counts))][catalog.Item(it)] += upTo(n)
	}
	st := catalog.ServiceTierStandard
	if c.servedAt().Has(catalog.ServiceTierPriority) && r.IntN(2) == 0 {
		st = catalog.ServiceTierPriority
	}
	total := new(big.Rat)
	for _, cs := range counts {
		u, err := catalog.NewUsage(cs, st, nil)
		if err != nil {
			tb.Fatal(err)
		}
		cost, missing, err := c.e.Price.Cost(u)
		if err != nil || missing != 0 {
			tb.Fatalf("Cost of %v: missing %v, %v", cs, missing, err)
		}
		total.Add(total, cost.Rat())
	}
	return total
}

// drawTiers lets the request allow priority, and the endpoint be offered at standard alone, at priority alone (the
// quote still prices standard) or at both, when the schedule has a priority tier.
func drawTiers(c *quoteCase, r *rand.Rand) {
	if !strings.Contains(c.json, "priority") {
		return
	}
	if r.IntN(2) == 0 {
		c.d.ServiceTiers.Add(catalog.ServiceTierPriority)
	}
	if r.IntN(2) == 0 {
		c.e.Model.ServiceTiers = [...]catalog.ServiceTierSet{
			1 << catalog.ServiceTierStandard, 1 << catalog.ServiceTierPriority, 1<<catalog.ServiceTierStandard | 1<<catalog.ServiceTierPriority,
		}[r.IntN(3)]
	}
}

// servedAt is the tiers a call can be served at: the request's, within the endpoint's when it is known.
func (c quoteCase) servedAt() catalog.ServiceTierSet {
	if c.e.Model.ServiceTiers == 0 {
		return c.d.ServiceTiers
	}
	return c.d.ServiceTiers & c.e.Model.ServiceTiers
}

// prompts draws each sampling's prompt, the first up to the request's bound and the others up to the context length,
// split between the input items the request may produce.
func (c quoteCase) prompts(samplings int64, upTo func(int64) int64) []map[catalog.Item]int64 {
	inputs := []catalog.Item{in}
	for _, it := range []catalog.Item{cacheWrite, cacheWrite1h} {
		if c.d.InputItems.Has(it) {
			inputs = append(inputs, it)
		}
	}
	if c.e.Model.Reports.Has(catalog.ItemCacheRead) {
		inputs = append(inputs, catalog.ItemCacheRead)
	}
	window := c.e.Model.ContextLength
	file := window // a file of unknown pages may fill the context
	if c.e.Model.FilePages > 0 && c.e.Model.PageTokens > 0 {
		file = c.e.Model.FilePages * c.e.Model.PageTokens
	}
	image := window // an image of unknown size may fill the context
	if c.e.Model.ImageTokens > 0 {
		image = c.e.Model.ImageTokens
	}
	first := min(window, c.d.TextBytes+c.d.Images*image+c.d.Documents*file)
	counts := make([]map[catalog.Item]int64, samplings)
	for j := range counts {
		counts[j] = map[catalog.Item]int64{}
		prompt := upTo(window)
		if j == 0 {
			prompt = upTo(first)
		}
		for _, it := range inputs[1:] { // the rest of the prompt stays input
			part := upTo(prompt)
			counts[j][it] += part
			prompt -= part
		}
		counts[j][in] += prompt
	}
	return counts
}

// imageBound is what one image may take: the model's bound, or the whole context when it has none.
func (c quoteCase) imageBound() *big.Int {
	if c.e.Model.ImageTokens > 0 {
		return big.NewInt(c.e.Model.ImageTokens)
	}
	return big.NewInt(c.e.Model.ContextLength)
}

// reference is the quote computed in math/big from the prices Ceiling gives, and whether every count is below
// math.MaxInt64.
func (c quoteCase) reference(tb testing.TB) (*big.Rat, bool) {
	tb.Helper()
	b := big.NewInt
	window := b(c.e.Model.ContextLength)
	file := window
	if c.e.Model.FilePages > 0 && c.e.Model.PageTokens > 0 {
		file = new(big.Int).Mul(b(c.e.Model.FilePages), b(c.e.Model.PageTokens))
	}
	first := new(big.Int).Add(b(c.d.TextBytes), new(big.Int).Mul(b(c.d.Images), c.imageBound()))
	first.Add(first, new(big.Int).Mul(b(c.d.Documents), file))
	if first.Cmp(window) > 0 {
		first = window
	}
	uses, samplings := c.referenceUses()
	tiers := c.servedAt()
	tiers.Add(catalog.ServiceTierStandard)
	inputs := c.d.InputItems
	inputs.Add(in)
	if c.e.Model.Reports.Has(catalog.ItemCacheRead) {
		inputs.Add(catalog.ItemCacheRead)
	}
	if c.e.Model.OpenAIUsage {
		inputs |= c.e.Model.Reports&(1<<cacheWrite|1<<catalog.ItemAudioIn) | 1<<catalog.ItemImageIn
	}
	longest := first.Int64()
	if samplings.Cmp(b(1)) > 0 || uses[webSearch] != nil {
		longest = c.e.Model.ContextLength
	}
	price := func(items catalog.ItemSet, length int64) *big.Rat {
		p, ok := c.e.Price.Ceiling(items, tiers, length)
		if !ok {
			tb.Fatalf("no price for %v", items)
		}
		rate := p.Rat()
		if c.e.Model.OpenAIUsage && items.Has(catalog.ItemCacheRead) && items.Has(cacheWrite) {
			read, _ := c.e.Price.Ceiling(1<<catalog.ItemCacheRead, tiers, length)
			write, _ := c.e.Price.Ceiling(1<<cacheWrite, tiers, length)
			combined := new(big.Rat).Add(read.Rat(), write.Rat())
			if combined.Cmp(rate) > 0 {
				rate = combined
			}
		}
		return rate
	}
	total, counts := new(big.Rat), []*big.Int(nil)
	term := func(p *big.Rat, n *big.Int) {
		total.Add(total, new(big.Rat).Mul(p, new(big.Rat).SetInt(n)))
		counts = append(counts, n)
	}
	term(price(inputs, first.Int64()), first)
	term(price(inputs, c.e.Model.ContextLength), new(big.Int).Mul(new(big.Int).Sub(samplings, b(1)), window))
	for it, n := range uses {
		term(price(1<<it, c.e.Model.ContextLength), n)
	}
	output, limit, overshoot := c.referenceOutput()
	term(price(1<<catalog.ItemOutput|1<<catalog.ItemReasoning, longest), output)
	fits := overshoot.Cmp(b(math.MaxInt64)) < 0 && limit.Cmp(b(math.MaxInt64)) < 0
	for _, n := range counts[1:] { // the first prompt is capped by the context length
		fits = fits && n.Cmp(b(math.MaxInt64)) < 0
	}
	return total, fits
}

func (c quoteCase) referenceUses() (map[catalog.Item]*big.Int, *big.Int) {
	uses, samplings := map[catalog.Item]*big.Int{}, big.NewInt(1)
	for _, t := range c.d.ServerTools {
		n := t.MaxUses
		if n == 0 {
			n = c.e.Model.MaxUses[t.Item]
		}
		if uses[t.Item] == nil {
			uses[t.Item] = new(big.Int)
		}
		uses[t.Item].Add(uses[t.Item], big.NewInt(n))
		samplings.Add(samplings, big.NewInt(n))
	}
	if c.d.Compaction {
		samplings.Mul(samplings, big.NewInt(2))
	}
	return uses, samplings
}

// referenceOutput is the output tokens to reserve, the allowance's rounded up; the output limit, which must be
// representable even when the first segment is smaller; and the overshoot in token-nanoseconds.
func (c quoteCase) referenceOutput() (output, limit, overshoot *big.Int) {
	b := big.NewInt
	limit, overshoot = new(big.Int).Mul(b(c.e.MaxOutput), b(max(c.d.Choices, 1))), new(big.Int)
	output = new(big.Int).Set(limit)
	if !c.e.Segmentable {
		return output, limit, overshoot
	}
	if output.Cmp(b(c.s.Output)) > 0 {
		output = b(c.s.Output)
	}
	overshoot.Mul(b(int64(c.s.Overshoot)), b(c.e.Model.OutputSpeed))
	allowance := new(big.Int).Neg(new(big.Int).Div(new(big.Int).Neg(overshoot), b(int64(time.Second)))) // Div floors
	return output.Add(output, allowance), limit, overshoot
}
