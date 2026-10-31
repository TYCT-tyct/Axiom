// Package catalog holds the models, their names, official specs and official prices, and the price schedule that the
// catalog, supplier cost plans and billing's prices share (README.md).
package catalog

import (
	"fmt"
	"slices"
)

// Item is a billing quantity. OpenAI cache reads and writes may overlap; physical prompt length is kept separately.
// protocol maps each upstream format's usage as README.md「计费项与用量」 tabulates.
type Item uint8

// The billing items, in the order stored lists name them: the token items, the per-use items, then the items counted
// per piece, second or character.
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
	ItemImageInPiece  // input images and video, when the vendor prices them per piece rather than per token
	ItemImageOutPiece // generated images
	ItemAudioInSecond // seconds of audio input
	ItemAudioOutSecond
	ItemCharacterIn // characters of text read aloud
	NumItems
)

// itemNames are the items' names in stored price schedules, so they never change.
var itemNames = [NumItems]string{
	"input", "cache_read", "cache_write", "cache_write_1h", "audio_in", "image_in",
	"output", "reasoning", "audio_out", "image_out", "web_search", "code_execution",
	"image_in_piece", "image_out_piece", "audio_in_second", "audio_out_second", "character_in",
}

func (it Item) String() string { return itemNames[it] }

// Unit is the quantity an item is billed in. A price schedule holds one price per item, so a side a vendor bills both
// per token and per piece has an item for each; an item's count is in whole units.
type Unit uint8

// The units an item is billed in: tokens, uses, whole pieces, whole seconds and whole characters.
const (
	UnitToken Unit = iota
	UnitUse
	UnitPiece
	UnitSecond
	UnitCharacter
	numUnits
)

// unitNames are the units' names in charge details, so they never change.
var unitNames = [numUnits]string{"token", "use", "piece", "second", "character"}

func (u Unit) String() string { return unitNames[u] }

// Unit is the quantity it is billed in: tokens unless it is a per-use, per-piece, per-second or per-character item. Every
// item has a case, so a new one must state its unit here, and the return after the switch is only for the compiler.
func (it Item) Unit() Unit {
	switch it {
	case ItemWebSearch, ItemCodeExecution:
		return UnitUse
	case ItemImageInPiece, ItemImageOutPiece:
		return UnitPiece
	case ItemAudioInSecond, ItemAudioOutSecond:
		return UnitSecond
	case ItemCharacterIn:
		return UnitCharacter
	case ItemInput, ItemCacheRead, ItemCacheWrite, ItemCacheWrite1h, ItemAudioIn, ItemImageIn, ItemOutput, ItemReasoning,
		ItemAudioOut, ItemImageOut, NumItems:
		return UnitToken
	}
	return UnitToken
}

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

// ParseItem returns the item a stored name denotes: the inverse of Item.String.
func ParseItem(name string) (Item, error) { return lookup[Item](itemNames[:], name) }

// ParseServiceTier returns the service tier a stored name denotes: the inverse of ServiceTier.String.
func ParseServiceTier(name string) (ServiceTier, error) {
	return lookup[ServiceTier](serviceTierNames[:], name)
}

// ItemSet is a set of billing items.
type ItemSet = set[Item]

// ServiceTierSet is a set of service tiers.
type ServiceTierSet = set[ServiceTier]

// set holds values of a small enumeration, one bit each.
type set[T ~uint8] uint32

// A thirty-third value would have no bit: Has would report it absent and Add would drop it. This fails to compile
// first.
var _ [32 - NumItems]struct{}

// Has reports whether v is in the set.
func (s set[T]) Has(v T) bool { return s&(1<<v) != 0 }

// Add puts v in the set.
func (s *set[T]) Add(v T) { *s |= 1 << v }

// List lists the set's values in enumeration order.
func (s set[T]) List() []T { return s.members() }

// members lists the set's values in order.
func (s set[T]) members() []T {
	var out []T
	for v := T(0); v < 32; v++ {
		if s.Has(v) {
			out = append(out, v)
		}
	}
	return out
}

// InputSide are the items a prompt is made of, the only ones a context tier's threshold may add up. They are also the
// items an upstream reports before the reply starts.
const InputSide ItemSet = 1<<ItemInput | 1<<ItemCacheRead | 1<<ItemCacheWrite | 1<<ItemCacheWrite1h | 1<<ItemAudioIn |
	1<<ItemImageIn

// OutputSide are the token items a reply is made of, which an upstream's output total adds up (Usage.OutputTotal).
const OutputSide ItemSet = 1<<ItemOutput | 1<<ItemReasoning | 1<<ItemAudioOut | 1<<ItemImageOut

// TokenItems are the items counted in tokens: the input side and the output side.
const TokenItems ItemSet = InputSide | OutputSide

// CountedItems are the items counted in whole pieces, seconds or characters rather than in tokens or uses.
const CountedItems ItemSet = 1<<ItemImageInPiece | 1<<ItemImageOutPiece | 1<<ItemAudioInSecond |
	1<<ItemAudioOutSecond | 1<<ItemCharacterIn

// maxCount bounds every count, so a tier's sum of up to NumItems counts stays far inside int64.
const maxCount = 1e10

// Usage is how much of each billing item a request used, at the service tier that served it, and what the upstream
// reported that protocol could not map. Its fields are unexported so that NewUsage, which bounds them, is the only way
// to make one, and so that a Usage never changes once made.
type Usage struct {
	q                [NumItems]int64
	serviceTier      ServiceTier
	unknown          []UnknownUsage
	physicalInput    int64
	hasPhysicalInput bool
	native           NativeTokens
	hasNative        bool
}

// NativeTokens are the token splits an upstream reports that have no billing item of their own: video input billed
// with images, and prediction tokens already inside the output total. They are echoed to the client and never priced
// again.
type NativeTokens struct {
	VideoInput         int64 `json:"video_input,omitzero"`
	AcceptedPrediction int64 `json:"accepted_prediction,omitzero"`
	RejectedPrediction int64 `json:"rejected_prediction,omitzero"`
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

// Count is how much of it was used: protocol renders it as the usage the client sees, billing's meter reads it as
// what the upstream has reported so far.
func (u Usage) Count(it Item) int64 { return u.q[it] }

// Unknown returns what the upstream reported that protocol could not map, for billing to price at its fallback prices.
func (u Usage) Unknown() []UnknownUsage { return slices.Clone(u.unknown) }

// OutputTotal is the billable output token count used by reservation metering. Output items are disjoint.
func (u Usage) OutputTotal() int64 {
	return u.q[ItemOutput] + u.q[ItemReasoning] + u.q[ItemAudioOut] + u.q[ItemImageOut]
}

// PhysicalInput returns the upstream prompt total and its presence; an explicit zero is not an absent total.
func (u Usage) PhysicalInput() (int64, bool) { return u.physicalInput, u.hasPhysicalInput }

// NativeTokens returns the provider-reported splits with no billing item of their own.
func (u Usage) NativeTokens() NativeTokens { return u.native }

// HasNativeTokens reports whether the upstream reported any of them, so an absent split is not shown as a zero.
func (u Usage) HasNativeTokens() bool { return u.hasNative }

// WithNativeTokens keeps provider-reported splits for the client echo, checked like every other count.
func (u Usage) WithNativeTokens(n NativeTokens) (Usage, error) {
	for _, c := range [...]int64{n.VideoInput, n.AcceptedPrediction, n.RejectedPrediction} {
		if c < 0 || c > maxCount {
			return Usage{}, fmt.Errorf("usage: native token count %d, want 0 to 10¹⁰", c)
		}
	}
	u.native, u.hasNative = n, true
	return u, nil
}

// WithPhysicalInput keeps the physical prompt length independently of potentially overlapping billing quantities.
func (u Usage) WithPhysicalInput(n int64) (Usage, error) {
	if n < 0 || n > maxCount {
		return Usage{}, fmt.Errorf("usage: physical input is %d, want 0 to 10¹⁰", n)
	}
	u.physicalInput, u.hasPhysicalInput = n, true
	return u, nil
}

// Recount replaces billed quantities without losing the physical prompt used by context bands and client echoes.
func (u Usage) Recount(counts map[Item]int64, unknown []UnknownUsage) (Usage, error) {
	v, err := NewUsage(counts, u.serviceTier, unknown)
	v.physicalInput, v.hasPhysicalInput = u.physicalInput, u.hasPhysicalInput
	v.native, v.hasNative = u.native, u.hasNative
	return v, err
}

// InputTotal is the client's prompt total, falling back to disjoint quantities for formats without a physical total.
func (u Usage) InputTotal() int64 {
	if u.hasPhysicalInput {
		return u.physicalInput
	}
	var total int64
	for it := range NumItems {
		if InputSide.Has(it) {
			total += u.q[it]
		}
	}
	return total
}

// ServiceTier is the service tier that served the request.
func (u Usage) ServiceTier() ServiceTier { return u.serviceTier }
