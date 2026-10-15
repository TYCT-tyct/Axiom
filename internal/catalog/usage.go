// Package catalog holds the models, their names, official specs and official prices, and the price schedule that the
// catalog, supplier cost plans and billing's prices share (README.md).
package catalog

import (
	"fmt"
	"slices"
)

// Item is a billing item. Items never overlap, so a token is counted under exactly one of them; protocol maps each
// upstream format's usage onto them as README.md「计费项与用量」 tabulates.
type Item uint8

// The billing items, in the order stored lists name them: the input side, then the output side, then counts.
const (
	ItemInput Item = iota
	ItemCacheRead
	ItemCacheWrite // a 5-minute cache write, and OpenAI's only kind
	ItemCacheWrite1h
	ItemAudioIn
	ItemImageIn // images and video, when the vendor reports and prices them apart from text
	ItemOutput
	ItemReasoning
	ItemAudioOut
	ItemImageOut
	ItemWebSearch     // counted in searches, not tokens
	ItemCodeExecution // counted in calls, not tokens
	NumItems
)

// itemNames are the items' names in stored price schedules, so they never change.
var itemNames = [NumItems]string{
	"input", "cache_read", "cache_write", "cache_write_1h", "audio_in", "image_in",
	"output", "reasoning", "audio_out", "image_out", "web_search", "code_execution",
}

func (it Item) String() string { return itemNames[it] }

// ServiceTier is the service tier that served a request. It is not called Tier, which is an account's tier.
type ServiceTier uint8

// The service tiers, as protocol reads them from each vendor's response (README.md「计费项与用量」).
const (
	ServiceTierStandard ServiceTier = iota
	ServiceTierFlex
	ServiceTierPriority
	ServiceTierFast // Anthropic's fast mode
	numServiceTiers
)

// serviceTierNames are the service tiers' names in stored price schedules, so they never change.
var serviceTierNames = [numServiceTiers]string{"standard", "flex", "priority", "fast"}

func (st ServiceTier) String() string { return serviceTierNames[st] }

// ItemSet is a set of billing items.
type ItemSet = set[Item]

// ServiceTierSet is a set of service tiers.
type ServiceTierSet = set[ServiceTier]

// set holds values of a small enumeration, one bit each.
type set[T ~uint8] uint16

// A seventeenth value would have no bit: Has would report it absent and Add would drop it. This fails to compile
// first.
var _ [16 - NumItems]struct{}

// Has reports whether v is in the set.
func (s set[T]) Has(v T) bool { return s&(1<<v) != 0 }

// Add puts v in the set.
func (s *set[T]) Add(v T) { *s |= 1 << v }

// inputSide are the items a prompt is made of, the only ones a context tier's threshold may add up.
const inputSide ItemSet = 1<<ItemInput | 1<<ItemCacheRead | 1<<ItemCacheWrite | 1<<ItemCacheWrite1h | 1<<ItemAudioIn |
	1<<ItemImageIn

// maxCount bounds every count, so a tier's sum of up to NumItems counts stays far inside int64.
const maxCount = 1e10

// Usage is how much of each billing item a request used, at the service tier that served it, and what the upstream
// reported that protocol could not map. Its fields are unexported so that NewUsage, which bounds them, is the only way
// to make one, and so that a Usage never changes once made.
type Usage struct {
	q           [NumItems]int64
	serviceTier ServiceTier
	unknown     []UnknownUsage
}

// UnknownUsage is a non-zero usage field the item table does not map, or a service tier value protocol does not
// recognise, as the upstream reported it. Billing handles it by docs/domains/billing.md「价目表缺项」: never free, never
// guessed into a known item.
type UnknownUsage struct {
	Field string // the upstream's own path, such as usage.server_tool_use.<a tool the item table does not know yet>_requests
	Value string // the reported JSON value as written: a count may not be an integer, and a tier is a string
}

// NewUsage checks each count is from 0 to 10¹⁰ and the service tier is one of the enumeration; an item left out counts
// 0.
func NewUsage(counts map[Item]int64, st ServiceTier, unknown []UnknownUsage) (Usage, error) {
	if st >= numServiceTiers {
		return Usage{}, fmt.Errorf("usage: unknown service tier %d", uint8(st))
	}
	u := Usage{serviceTier: st, unknown: slices.Clone(unknown)}
	for it, n := range counts {
		if it >= NumItems {
			return Usage{}, fmt.Errorf("usage: unknown billing item %d", uint8(it))
		}
		if n < 0 || n > maxCount {
			return Usage{}, fmt.Errorf("usage: %s is %d, want 0 to 10¹⁰", it, n)
		}
		u.q[it] = n
	}
	return u, nil
}

// Unknown returns what the upstream reported that protocol could not map, for billing to price at its fallback prices.
func (u Usage) Unknown() []UnknownUsage { return slices.Clone(u.unknown) }

// OutputTotal is the output the zero-output waiver reads, output plus reasoning (docs/domains/billing.md「免单规则」):
// a reply that only reasoned is not an empty reply.
func (u Usage) OutputTotal() int64 { return u.q[ItemOutput] + u.q[ItemReasoning] }

// Count is the usage of one item, for billing's meter to read a count the upstream has reported so far.
func (u Usage) Count(it Item) int64 { return u.q[it] }
