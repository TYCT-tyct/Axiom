package billing

import (
	"math"
	"math/bits"
	"slices"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// InputBound returns the most input tokens the first sampling of the request can have, and the most any sampling can
// have, which is what bounds the context tier a charge falls in. Quote prices with them, and routing holds a price limit
// against the same bound, so the two never read a request's length differently.
func InputBound(d Demand, m Model) (first, longest int64) {
	_, samplings, _ := serverToolUses(d, m)
	return inputBound(d, m, samplings)
}

// The interim estimate of the input a charge takes when the upstream reported none (docs/domains/billing.md「用量来源与
// 暂估」): text runs about 4 bytes a token, an image is taken at 1600 tokens and a file at 4096, unless the model's bound
// is smaller. The text bytes are the rule only for a request counted in no tokenizer of its family; one that was counted
// (Demand.TextTokens) takes its count instead.
const (
	InputBytesPerToken   = 4
	estimatedImageTokens = 1600
	estimatedFileTokens  = 4096
)

// InputEstimate is the input tokens of the request as a charge takes them when the upstream reported none: the text at
// its own count when the caller counted it and at InputBytesPerToken otherwise, each image and file at the lesser of
// its bound and the estimate, held to the context except for a direct call, whose batch is charged for every input.
// It never exceeds InputBound's first, which the reservation covers: every part is the lesser of the part's bound and
// something, and a token covers a byte or more, so the text's count never passes its bytes. d.TextBytes is bytes.
func InputEstimate(d Demand, m Model) int64 {
	text := min(d.TextTokens, d.TextBytes) // a token covers a byte or more, and nothing counted keeps the rule below
	if text <= 0 {
		text = ceilDiv(d.TextBytes, InputBytesPerToken) // a token begun is charged
	}
	images := mul(d.Images, min(imageTokens(m), estimatedImageTokens))
	files := mul(d.Documents, min(fileTokens(m), estimatedFileTokens))
	if d.Direct || d.InputBytes > 0 {
		return add(text, add(images, files)) // each input fits the context; the batch's total is what is charged
	}
	return min(m.ContextLength, add(text, add(images, files)))
}

func inputBound(d Demand, m Model, samplings int64) (first, longest int64) {
	window := m.ContextLength
	total := add(d.TextBytes, add(mul(d.Images, imageTokens(m)), mul(d.Documents, fileTokens(m))))
	first = min(window, total)
	if d.Direct || d.InputBytes > 0 {
		// A batch is one charge for every input, and the one length every price of it is banded at: the settlement
		// bands its reported total, which can pass any one input's context.
		return total, total
	}
	searchable := slices.ContainsFunc(d.ServerTools, func(t ServerTool) bool { return t.Item == catalog.ItemWebSearch && !t.Flat })
	if samplings > 1 || searchable {
		return first, window // every later sampling, and one a search beyond the quote adds, rereads the whole context
	}
	return first, first
}

// imageTokens bounds one image: the model's most per image, or the whole context when the catalog has none (the model
// spec records no image size yet), the same convention as fileTokens.
func imageTokens(m Model) int64 {
	if m.ImageTokens <= 0 {
		return m.ContextLength
	}
	return m.ImageTokens
}

// fileTokens bounds one file: Documents counts files, not pages (protocol does not parse PDFs), so a file takes its
// pages at most at the page maximum, and the whole context when the model has no known page limit.
func fileTokens(m Model) int64 {
	if m.FilePages <= 0 || m.PageTokens <= 0 {
		return m.ContextLength
	}
	return mul(m.FilePages, m.PageTokens)
}

// boundedServerTools refuses a native tool with neither the request's limit nor a model default: its uses cannot be
// bounded, and a flat tool of the same item must not hide that. A flat tool has one use by construction.
func boundedServerTools(d Demand, m Model) error {
	for _, t := range d.ServerTools {
		if t.Flat {
			continue
		}
		n := t.MaxUses
		if n == 0 {
			n = m.MaxUses[t.Item]
		}
		if n == 0 {
			return &UnboundedError{t.Item}
		}
	}
	return nil
}

// serverToolUses returns each server tool's most uses, the request's or else the model's default, totalled per item so
// a quote adds at most NumItems products however many tools the request lists; and the samplings: one, plus
// one per use, doubled when compaction may summarise and sample again.
func serverToolUses(d Demand, m Model) (uses [catalog.NumItems]int64, samplings, repeats int64) {
	samplings = 1
	for _, t := range d.ServerTools {
		n := t.MaxUses
		if n == 0 {
			n = m.MaxUses[t.Item]
		}
		uses[t.Item] = add(uses[t.Item], n)
		if !d.Direct && !t.Flat { // a direct call's or a flat tool's charge is per use, without sampling the model again
			samplings = add(samplings, n)
			if t.Repeats {
				repeats = add(repeats, n)
			}
		}
	}
	if d.Compaction {
		samplings = mul(samplings, 2)
	}
	return uses, samplings, repeats
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
