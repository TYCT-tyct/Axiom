// Package billing reserves, meters, extends and settles what a call costs (README.md). This file quotes a
// reservation: what each route entry needs reserved before the upstream is called, and which entries the account can
// afford.
package billing

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// Demand is what the request itself says; what depends on the model is worked out per entry when quoting.
type Demand struct {
	TextBytes, Images, Choices int64
	Documents                  int64        // files, whatever their pages: protocol does not parse them
	ServerTools                []ServerTool // the server tools the request enables, such as web search
	Compaction                 bool         // server-side compaction or context management is on
	InputItems                 catalog.ItemSet
	ServiceTiers               catalog.ServiceTierSet
}

// ServerTool is a server tool and the most uses the request allows it; 0 means the request set none, so the entry's
// model default applies.
type ServerTool struct {
	Item    catalog.Item
	MaxUses int64
}

// Model is the model data a quote needs, filled by the caller from the catalog snapshot.
type Model struct {
	ContextLength           int64
	ImageTokens, PageTokens int64                  // the most input tokens one image or one document page can take
	FilePages               int64                  // the most pages one file can have; 0 when no limit is known
	OutputSpeed             int64                  // the fastest output, in tokens per second
	MaxUses                 map[catalog.Item]int64 // each server tool's default most uses
	Reports                 catalog.ItemSet        // the items the upstream may report (catalog.Reportable)
}

// Quotable is one route entry as billing quotes it: its locked price schedule, its model, the output limit that
// applies to it, and whether it can be reserved in segments (routing decides that per entry and request).
type Quotable struct {
	Price       catalog.Schedule
	Model       Model
	MaxOutput   int64
	Segmentable bool
}

// Segments are the site's settings for reserving in segments.
type Segments struct {
	Output    int64         // output tokens per segment
	Overshoot time.Duration // the extension wait plus the delays until a cancelled upstream stops billing
}

// Source is what an amount available to a reservation is limited by.
type Source uint8

// The sources, in the order a refusal names the first that runs out.
const (
	SourceBalance     Source = iota // the account's balance less its pending reservations, above its floor
	SourceSiteBalance               // the same for a second-level site's owner
	SourceWorkspace                 // the workspace budget
	SourceKey                       // the key's limit
)

// Available is the amount one source still allows.
type Available struct {
	Source Source
	Amount platform.Exact
}

// Insufficient is the refusal when the preferred entry's quote exceeds what a source allows.
type Insufficient struct{ Source Source }

func (e *Insufficient) Error() string {
	return fmt.Sprintf("billing: source %d cannot cover the reservation", e.Source)
}

// Reservation is the outcome of a quote: the amount to reserve and the entries kept, as indexes into the route.
type Reservation struct {
	Amount platform.Exact
	Kept   []int
}

var (
	errUnpriced = errors.New("billing: the entry's schedule does not price what the request may use")
	errOverflow = errors.New("billing: the request's bounds exceed what a reservation can count")
)

// Quote quotes every entry, drops those whose quote exceeds any available amount, and reserves the highest quote
// kept, so a fallback never meets an entry dearer than what was reserved. A dropped preferred entry refuses the
// request with *Insufficient, naming the first source in avail that falls short.
func Quote(d Demand, q []Quotable, s Segments, avail []Available) (Reservation, error) {
	var r Reservation
	for i, e := range q {
		amount, err := quoteEntry(d, e, s)
		if err != nil {
			return Reservation{}, fmt.Errorf("entry %d: %w", i, err)
		}
		if short := shortSource(amount, avail); short >= 0 {
			if i == 0 {
				return Reservation{}, &Insufficient{avail[short].Source}
			}
			continue
		}
		r.Kept = append(r.Kept, i)
		if amount.Cmp(r.Amount) > 0 {
			r.Amount = amount
		}
	}
	return r, nil
}

func shortSource(amount platform.Exact, avail []Available) int {
	for i, a := range avail {
		if amount.Cmp(a.Amount) > 0 {
			return i
		}
	}
	return -1
}

// quoteEntry follows docs/domains/billing.md「预留与结算」: the input of every sampling at its bound, every server tool
// at its most uses, and the output, the first segment plus the overshoot allowance or else the whole limit, each at
// the dearest price it can have.
func quoteEntry(d Demand, e Quotable, s Segments) (platform.Exact, error) {
	window := e.Model.ContextLength
	if window <= 0 {
		return platform.Exact{}, errors.New("billing: the model has no context length")
	}
	tiers := d.ServiceTiers
	tiers.Add(catalog.ServiceTierStandard)
	uses, samplings := serverToolUses(d, e.Model)
	first := min(window, add(d.TextBytes, add(mul(d.Images, e.Model.ImageTokens), mul(d.Documents, fileTokens(e.Model)))))
	longest := first // the longest prompt any sampling can have, which bounds the band
	if samplings > 1 {
		longest = window // every later sampling rereads up to the whole context
	}
	rest := mul(samplings-1, window)
	output, overshoot := mul(e.MaxOutput, max(d.Choices, 1)), int64(0) // overshoot in token-nanoseconds
	if e.Segmentable {
		overshoot = mul(int64(s.Overshoot), e.Model.OutputSpeed)
		output = add(min(output, s.Output), ceilDiv(overshoot, int64(time.Second)))
	}
	// A saturated count understates the quote, so one that reached math.MaxInt64 refuses the entry. Only the first
	// prompt may saturate: the context length caps it anyway.
	if max(samplings, rest, output, overshoot) == math.MaxInt64 {
		return platform.Exact{}, errOverflow
	}
	in := d.InputItems | e.Model.Reports&(1<<catalog.ItemCacheRead) // a cache hit is the upstream's, not the request's
	in.Add(catalog.ItemInput)
	firstPrice, ok1 := e.Price.Ceiling(in, tiers, first)
	restPrice, ok2 := e.Price.Ceiling(in, tiers, window)
	outPrice, ok3 := e.Price.Ceiling(1<<catalog.ItemOutput|1<<catalog.ItemReasoning, tiers, longest)
	if !ok1 || !ok2 || !ok3 {
		return platform.Exact{}, errUnpriced
	}
	amount := sum(firstPrice.Mul(first), restPrice.Mul(rest), outPrice.Mul(output))
	for it, n := range uses {
		if n == 0 {
			continue
		}
		price, ok := e.Price.Ceiling(1<<catalog.Item(it), tiers, window)
		if !ok {
			return platform.Exact{}, errUnpriced
		}
		amount = sum(amount, price.Mul(n))
	}
	return amount, nil
}

// fileTokens bounds one file: Documents counts files, not pages (protocol does not parse PDFs), so a file takes its
// pages at most at the page maximum, and the whole context when the model has no known page limit.
func fileTokens(m Model) int64 {
	if m.FilePages <= 0 || m.PageTokens <= 0 {
		return m.ContextLength
	}
	return mul(m.FilePages, m.PageTokens)
}

// serverToolUses returns each server tool's most uses, the request's or else the model's default, totalled per item so
// a quote adds at most NumItems products however many tools the request lists; and the samplings: one, plus
// one per use, doubled when compaction may summarise and sample again.
func serverToolUses(d Demand, m Model) (uses [catalog.NumItems]int64, samplings int64) {
	samplings = 1
	for _, t := range d.ServerTools {
		n := t.MaxUses
		if n == 0 {
			n = m.MaxUses[t.Item]
		}
		uses[t.Item], samplings = add(uses[t.Item], n), add(samplings, n)
	}
	if d.Compaction {
		samplings = mul(samplings, 2)
	}
	return uses, samplings
}

// sum adds amounts that cannot overflow: each is a price below 2⁶⁰ times a count below 2⁶³, so below 2¹²³, and a quote
// adds at most NumItems + 3 of them, below 2¹²⁷. A failure is a broken bound, so it panics rather than return an
// error nobody can act on.
func sum(amounts ...platform.Exact) (total platform.Exact) {
	for _, a := range amounts {
		var err error
		if total, err = total.Add(a); err != nil {
			panic(err)
		}
	}
	return total
}

// mul and add saturate at math.MaxInt64 rather than wrap; their operands are counts, never negative.
func mul(a, b int64) int64 {
	hi, lo := bits.Mul64(uint64(a), uint64(b)) //nolint:gosec // non-negative counts
	if hi != 0 || lo > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(lo)
}

func add(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func ceilDiv(a, b int64) int64 {
	if a%b != 0 {
		return a/b + 1
	}
	return a / b
}
