// Package catalog holds the models, their names, official specs and official prices, and the price schedule that the
// catalog, supplier cost plans and billing's prices share (README.md).
package catalog

import "fmt"

// Item is a billing item. Items never overlap, so a token is counted under exactly one of them; protocol maps each
// upstream format's usage onto them as README.md「计费项与用量」 tabulates.
type Item uint8

// The billing items, in the order their prices are listed.
const (
	ItemInput Item = iota
	ItemCacheRead
	ItemCacheWrite // a 5-minute cache write, and OpenAI's only kind
	ItemCacheWrite1h
	ItemOutput
	ItemReasoning
	ItemWebSearch // counted in searches, not tokens
	numItems
)

// itemNames are the items' names in stored price schedules, so they never change.
var itemNames = [numItems]string{"input", "cache_read", "cache_write", "cache_write_1h", "output", "reasoning", "web_search"}

func (it Item) String() string { return itemNames[it] }

// maxCount bounds every count, so a tier's sum of up to seven counts stays far inside int64.
const maxCount = 1e10

// Usage is how much of each billing item a request used. Its counts are unexported so that NewUsage, which bounds
// them, is the only way to make one.
type Usage struct{ q [numItems]int64 }

// NewUsage checks each count is from 0 to 10¹⁰; an item left out counts 0.
func NewUsage(counts map[Item]int64) (Usage, error) {
	var u Usage
	for it, n := range counts {
		if it >= numItems {
			return Usage{}, fmt.Errorf("usage: unknown billing item %d", uint8(it))
		}
		if n < 0 || n > maxCount {
			return Usage{}, fmt.Errorf("usage: %s is %d, want 0 to 10¹⁰", it, n)
		}
		u.q[it] = n
	}
	return u, nil
}

// OutputTotal is everything the model generated, reasoning included: a reply that only reasoned is not an empty reply
// to the zero-output waiver (docs/domains/billing.md「免单规则」).
func (u Usage) OutputTotal() int64 { return u.q[ItemOutput] + u.q[ItemReasoning] }
