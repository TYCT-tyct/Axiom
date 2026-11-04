package protocol

import (
	"cmp"
	"strconv"
	"strings"
	"time"
)

// What the rewrite of Chat and legacy completions adds or forces: the output cap in the member the endpoint reads, and
// a stream that reports usage.

// chatOutputCap takes a Chat output cap out of the body, to be written again as the one member the endpoint reads
// (addChat). The client's two members cap the same output, and which of them an endpoint reads depends on the endpoint,
// so a cap the endpoint ignores must not stay and be taken for the bound the call was reserved at. The smallest value
// above 0 is the client's cap; null and 0 stand for none (Inspect reads 0 as no cap too).
func (p *rewriter) chatOutputCap() error {
	v, err := p.value()
	p.at = p.offset()
	if err != nil || v.Kind() != '0' {
		return err
	}
	if n, perr := strconv.ParseInt(string(v), 10, 64); perr == nil && n > 0 && (p.chatCap == 0 || n < p.chatCap) {
		p.chatCap = n
	}
	return nil
}

// usesCompletionCap reports whether a Chat endpoint of the model reads max_completion_tokens and refuses max_tokens:
// OpenAI's o1, o3 and o4 reasoning models, GPT-5 and the GPT-6 Astra, Sol and Luna snapshots. It is new-api's rule
// (relaykit/dto/openai_request.go, GetOpenAIChatCapabilities with IsOpenAIReasoningOModel and IsOpenAIGPT5Model,
// 32e5874), which converts the member for them; every other endpoint, DeepSeek and Qwen among them, reads max_tokens
// and ignores the other member. The model is the endpoint's name for it (Target.Model).
func usesCompletionCap(model string) bool {
	if strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4") ||
		model == "gpt-5" || strings.HasPrefix(model, "gpt-5-") || strings.HasPrefix(model, "gpt-5.") {
		return true
	}
	for _, base := range [...]string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		if rest, ok := strings.CutPrefix(model, base); ok && (rest == "" || isDatedSnapshot(rest)) {
			return true
		}
	}
	return false
}

// isDatedSnapshot reports whether s is "-" and a date, YYYY-MM-DD, as OpenAI names a snapshot of a model.
func isDatedSnapshot(s string) bool {
	date, ok := strings.CutPrefix(s, "-")
	if !ok {
		return false
	}
	_, err := time.Parse(time.DateOnly, date)
	return err == nil
}

// streamOptions makes a streaming upstream report usage whatever the client wrote: the gateway meters from the
// upstream's report, and aggregates its stream for a client that asked for none, so include_usage is true and the
// client's other options stay. new-api forces it the same way (relay/compatible_handler.go, ForceStreamOption), and
// sub2api sets this member alone (backend/internal/service/openai_gateway_chat_completions_raw.go,
// ensureOpenAIChatStreamUsage, 8f2cbb5).
func (p *rewriter) streamOptions() error {
	p.optioned = true
	if !p.t.Streams {
		return p.skip()
	}
	if ok, err := p.open('{', "an object"); !ok {
		if err == nil {
			p.replace(len("null"), forcedOptions)
		}
		return err
	}
	if err := p.members(map[string]rule{"include_usage": (*rewriter).forceUsage}, (*rewriter).skip); err != nil {
		return err
	}
	if !p.usageForced {
		if p.out[len(p.out)-1] != '{' { // everything up to here is copied: an object that has no member ends the output
			p.out = append(p.out, ',')
		}
		p.out = append(p.out, `"include_usage":true`...)
	}
	return p.token()
}

func (p *rewriter) forceUsage() error {
	p.usageForced = true
	v, err := p.value()
	if err == nil && v.Kind() != 't' {
		p.replace(len(v), "true")
	}
	return err
}

// addCompletions appends what a legacy completion lacks: the output cap it was quoted at, when the request has none,
// and the stream that reports usage. Its prompt is the client's and is left as it is.
func (p *rewriter) addCompletions() error {
	if !p.capped && p.t.MaxOutput > 0 {
		p.out = strconv.AppendInt(append(p.out, addCap...), p.t.MaxOutput, 10)
	}
	if p.t.Streams && !p.optioned {
		p.out = append(p.out, addStreamOptions...)
	}
	return p.addStream()
}

// addChat appends the output cap in the member the endpoint reads, and what else a Chat request lacks. The cap is the
// client's, else the one the call was quoted at, so that what is sent is what was reserved (I1).
func (p *rewriter) addChat() error {
	limit := cmp.Or(p.chatCap, p.t.MaxOutput)
	if limit > 0 {
		member := addCap
		if usesCompletionCap(p.t.Model) {
			member = addCompletionCap
		}
		p.out = strconv.AppendInt(append(p.out, member...), limit, 10)
	}
	if p.t.Streams && !p.optioned {
		p.out = append(p.out, addStreamOptions...)
	}
	return p.addStream()
}
