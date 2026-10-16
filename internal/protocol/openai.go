package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go
	"fmt"
	"slices"

	"axiom/internal/catalog"
)

// openAIUsage is the usage object of OpenAI's two formats, which name the same counts differently: Chat's
// prompt_tokens and completion_tokens, Responses' input_tokens and output_tokens. The input total includes the cached
// and cache-written tokens, the output total the reasoning tokens (OpenAI《Create chat completion》《Responses API
// reference》《Prompt caching》, read 2026-10-14).
type openAIUsage struct {
	Prompt            *int64         `json:"prompt_tokens"`
	Input             *int64         `json:"input_tokens"`
	PromptDetails     *openAIDetails `json:"prompt_tokens_details"`
	InputDetails      *openAIDetails `json:"input_tokens_details"`
	Completion        *int64         `json:"completion_tokens"`
	Output            *int64         `json:"output_tokens"`
	CompletionDetails *openAIDetails `json:"completion_tokens_details"`
	OutputDetails     *openAIDetails `json:"output_tokens_details"`
	// Reported but not billed: the sum, and a relay's price, which is not usage (OpenRouter《Usage accounting》, read
	// 2026-10-14).
	Total       jsontext.Value            `json:"total_tokens"`
	Cost        jsontext.Value            `json:"cost"`
	CostDetails jsontext.Value            `json:"cost_details"`
	IsBYOK      jsontext.Value            `json:"is_byok"`
	Unknown     map[string]jsontext.Value `json:",embed"`
}

// openAIDetails breaks a total down. Prediction tokens are part of the output total and billed with it.
type openAIDetails struct {
	Cached     int64                     `json:"cached_tokens"`
	CacheWrite int64                     `json:"cache_write_tokens"`
	Audio      int64                     `json:"audio_tokens"`
	Reasoning  int64                     `json:"reasoning_tokens"`
	Accepted   jsontext.Value            `json:"accepted_prediction_tokens"`
	Rejected   jsontext.Value            `json:"rejected_prediction_tokens"`
	Unknown    map[string]jsontext.Value `json:",embed"`
}

// items maps the usage as catalog README.md「计费项与用量」 tabulates, at the service tier the reply names, if any.
func (u *openAIUsage) items(tier *string) (r usageReport, err error) {
	in, inDetails, inName := u.Input, u.InputDetails, "input_tokens_details"
	if u.Prompt != nil {
		in, inDetails, inName = u.Prompt, u.PromptDetails, "prompt_tokens_details"
	}
	out, outDetails, outName := u.Output, u.OutputDetails, "output_tokens_details"
	if u.Completion != nil {
		out, outDetails, outName = u.Completion, u.CompletionDetails, "completion_tokens_details"
	}
	if in != nil {
		d := inDetails.orZero()
		if err := r.split(*in, catalog.ItemInput, []catalog.Item{catalog.ItemCacheRead, catalog.ItemCacheWrite, catalog.ItemAudioIn},
			[]int64{d.Cached, d.CacheWrite, d.Audio}); err != nil {
			return r, err
		}
		unknownCounts(&r, "usage."+inName, d.Unknown)
	}
	if out != nil {
		d := outDetails.orZero()
		if err := r.split(*out, catalog.ItemOutput, []catalog.Item{catalog.ItemReasoning, catalog.ItemAudioOut},
			[]int64{d.Reasoning, d.Audio}); err != nil {
			return r, err
		}
		unknownCounts(&r, "usage."+outName, d.Unknown)
	}
	unknownCounts(&r, "usage", u.Unknown)
	if tier != nil {
		r.tier, r.tierSet = openAITier(*tier, &r), true
	}
	r.sortUnknown()
	return r, nil
}

// split sets the parts of a total, and the rest of it as rest. The total and every part must be counts, checked
// before subtracting, and the parts may not add up to more than the total. An audio part is set only when reported,
// as only audio models report it.
func (r *usageReport) split(total int64, rest catalog.Item, parts []catalog.Item, counts []int64) error {
	if !countOK(total) || slices.ContainsFunc(counts, func(n int64) bool { return !countOK(n) }) {
		return fmt.Errorf("usage: %s or a part of it is not from 0 to 10¹⁰", rest)
	}
	left := total
	for _, n := range counts {
		left -= n // no overflow: every count is at most 10¹⁰
	}
	if left < 0 {
		return fmt.Errorf("usage: the parts of %s add up to more than its total %d", rest, total)
	}
	for i, it := range parts {
		if counts[i] > 0 || it != catalog.ItemAudioIn && it != catalog.ItemAudioOut {
			r.set(it, counts[i])
		}
	}
	r.set(rest, left)
	return nil
}

func (d *openAIDetails) orZero() openAIDetails {
	if d == nil {
		return openAIDetails{}
	}
	return *d
}

// openAITier maps OpenAI's service_tier (OpenAI《Create chat completion》, service_tier, read 2026-10-14); a value it
// does not know goes in unknown.
func openAITier(tier string, r *usageReport) catalog.ServiceTier {
	switch tier {
	case "default":
		return catalog.ServiceTierStandard
	case "flex":
		return catalog.ServiceTierFlex
	case "priority":
		return catalog.ServiceTierPriority
	}
	r.unknown = append(r.unknown, catalog.UnknownUsage{Field: "service_tier", Value: tier})
	return catalog.ServiceTierStandard
}

// openAINames are a format's names for the counts.
type openAINames struct{ input, inputDetails, output, outputDetails string }

var (
	chatNames      = openAINames{"prompt_tokens", "prompt_tokens_details", "completion_tokens", "completion_tokens_details"}
	responsesNames = openAINames{"input_tokens", "input_tokens_details", "output_tokens", "output_tokens_details"}
)

// appendOpenAIUsage appends a usage object with u's counts in the format's names and cost, members each led by a
// comma.
func appendOpenAIUsage(dst []byte, n openAINames, u catalog.Usage, cost string) []byte {
	in := u.Count(catalog.ItemInput) + u.Count(catalog.ItemCacheRead) + u.Count(catalog.ItemCacheWrite) +
		u.Count(catalog.ItemCacheWrite1h) + u.Count(catalog.ItemAudioIn)
	out := u.OutputTotal() + u.Count(catalog.ItemAudioOut)
	return fmt.Appendf(dst, `{%q:%d,%q:{"cached_tokens":%d,"cache_write_tokens":%d},%q:%d,%q:{"reasoning_tokens":%d},`+
		`"total_tokens":%d%s}`, n.input, in, n.inputDetails, u.Count(catalog.ItemCacheRead),
		u.Count(catalog.ItemCacheWrite)+u.Count(catalog.ItemCacheWrite1h), n.output, out, n.outputDetails,
		u.Count(catalog.ItemReasoning), in+out, cost)
}

// appendQuoted appends s as a JSON string.
func appendQuoted(dst []byte, s string) []byte {
	dst, _ = jsontext.AppendQuote(dst, s) //nolint:errcheck // invalid UTF-8 is written as U+FFFD, and that is all the error says
	return dst
}
