package protocol

import "axiom/internal/catalog"

// UsageContextOf is the metering context a relay of r needs. The gateway also uses it when replaying a cached upstream
// reply, so the replay sees the same request facts as the attempt that stored it.
func UsageContextOf(r *Request) UsageContext { return usageContext(r) }

func usageContext(r *Request) UsageContext {
	u := UsageContext{HidesReasoning: r.HidesReasoning, CacheTTL1h: r.InputItems.Has(catalog.ItemCacheWrite1h), LegacyFunctions: r.legacy && !r.modernTools}
	u.ResponseHealing = r.ResponseHealing && !r.Stream && r.Format == Chat && r.Needs.Has(StructuredOutput)
	n := 0
	for _, st := range [...]catalog.ServiceTier{
		catalog.ServiceTierStandard, catalog.ServiceTierFlex, catalog.ServiceTierPriority, catalog.ServiceTierFast,
	} {
		if r.ServiceTiers.Has(st) {
			u.Tier, n = st, n+1
		}
	}
	if n != 1 {
		u.Tier = catalog.ServiceTierStandard
	}
	return u
}

type rule func(*rewriter) error

// formatRules are one format's rewrites: members to rewrite, and the members to add when the body lacks them.
type formatRules struct {
	members map[string]rule
	add     rule
	// room is the most bytes the rewrites add besides the model and the identifiers; identities is how many
	// identifiers there can be.
	room, identities int
}

var (
	rewriteRules = [...]formatRules{
		Messages: {
			members: withRouting(map[string]rule{
				"model": (*rewriter).model, "metadata": object(metadataRules), "stream": (*rewriter).stream,
				"max_tokens": (*rewriter).outputCap,
			}),
			add: (*rewriter).addMessages, identities: 1,
			room: len(addCap) + len(maxInt64) + len(addStream),
		},
		Responses: {
			members: withRouting(map[string]rule{
				"model": (*rewriter).model, "previous_response_id": (*rewriter).previous, "input": (*rewriter).input,
				"store": (*rewriter).store, "include": (*rewriter).include, "prompt_cache_key": (*rewriter).identity,
				"user": (*rewriter).identity, "safety_identifier": (*rewriter).identity,
				"max_output_tokens": (*rewriter).outputCap, "stream": (*rewriter).stream,
			}),
			add: (*rewriter).addResponses, identities: 3,
			room: len(addStore) + len(addInclude) + len(addInput) + len(wrapInput) + len("[]}") + len(addOutputCap) +
				len(maxInt64) + len(addStream),
		},
		// Chat's rewrites are the identity members and the output cap the other formats' are, and the usage the gateway
		// meters from: stream_options.include_usage. The dialect of the model (its reasoning parameters, its cache
		// markers) is the client's: what it wrote reaches the endpoint as it wrote it (README.md「同格式改写」).
		Chat: {
			members: withRouting(map[string]rule{
				"model": (*rewriter).model, "user": (*rewriter).identity, "prompt_cache_key": (*rewriter).identity,
				"safety_identifier": (*rewriter).identity, "max_tokens": (*rewriter).chatOutputCap,
				"max_completion_tokens": (*rewriter).chatOutputCap, "stream": (*rewriter).stream,
				"stream_options": (*rewriter).streamOptions,
			}),
			add: (*rewriter).addChat, identities: 3,
			room: len(addStream) + len(addStreamOptions) + len(addCompletionCap) + len(maxInt64),
		},
	}
	// compactRules rewrite a compaction request: the members that must not reach an upstream as they are — the
	// endpoint's model name, the identifiers, the routing parameters — and nothing else. A generation's rewrite adds
	// what a compaction request does not take (store, include, the output cap, a stream) and rewrites its input and
	// history, which compaction has neither (docs/domains/gateway.md「转发规则」).
	compactRules = [...]formatRules{
		Responses: {
			members: withRouting(map[string]rule{
				"model": (*rewriter).model, "prompt_cache_key": (*rewriter).identity,
				"user": (*rewriter).identity, "safety_identifier": (*rewriter).identity,
			}),
			identities: 3,
		},
		Chat: {}, // spelled out so that the array covers every format, as rewriteRules does: rulesFor never indexes past it
	}
	metadataRules = map[string]rule{"user_id": (*rewriter).userID}
)

// rulesFor is how a request of format f is rewritten for the endpoint t: a compaction request is the one that is not a
// generation, and a format without compaction has no rules for it.
func rulesFor(f Format, t Target) formatRules {
	if t.Compact {
		return compactRules[f]
	}
	if f > Chat { // the formats that rewrite in place, never converted: direct.go
		return directRewriteRules[f]
	}
	return rewriteRules[f]
}

// withRouting adds the members that are OpenRouter's routing parameters or our extensions, which the gateway has
// used and no upstream defines: an official endpoint refuses a member it does not know (gateway.md「路由参数」).
func withRouting(m map[string]rule) map[string]rule {
	for _, name := range routingParameters {
		m[name] = (*rewriter).drop
	}
	return m
}

var routingParameters = []string{"provider", "models", "preset", "fallbacks", "route", "transforms", "plugins", "usage", "session_id", "axiom"}

// encrypted asks Responses for reasoning items that can be sent again, the only way when the upstream stores
// nothing (OpenAI《Create a model response》, include, read 2026-10-14).
const encrypted = "reasoning.encrypted_content"

// What the rewrites add to a body: the members it lacks, and the array that wraps a string input in the history.
const (
	addStore   = `,"store":false`
	addInclude = `,"include":["` + encrypted + `"]`
	addInput   = `,"input":[`
	wrapInput  = `,{"type":"message","role":"user","content":`
	// addOutputCap and its number: the cap a request without one is sent with (Target.MaxOutput).
	addOutputCap = `,"max_output_tokens":`
	maxInt64     = "9223372036854775807"
	// addCap is max_tokens, the member Chat and Messages write their cap in; addCompletionCap is Chat's other member
	// (usesCompletionCap), addStreamOptions the option that makes the upstream report usage; forcedOptions stands for a
	// null stream_options.
	addCap           = `,"max_tokens":`
	addCompletionCap = `,"max_completion_tokens":`
	addStreamOptions = `,"stream_options":` + forcedOptions
	forcedOptions    = `{"include_usage":true}`
	// addStream asks for the upstream's stream when the endpoint streams one, since the gateway reads every call as a
	// stream and a client that wants no stream gets the events aggregated (docs/domains/gateway.md「转发规则」).
	addStream = `,"stream":true`
)

// rewriter copies a body to out, rewriting members on the way. body[:at] is settled: copied to out or dropped.
type rewriter struct {
	reader
	body, out                             []byte
	src                                   decoderBuffer
	at                                    int
	base, end                             int // offsets of an indexed top-level value; zero on the streaming path
	consumed                              bool
	t                                     Target
	w                                     Rewrites
	continues, stored, included, hasInput bool            // the Responses members seen
	streamed                              bool            // the stream member seen
	ending                                bool            // the event is the one that carries the final usage
	capped                                bool            // the output cap member was given a value
	chatCap                               int64           // the smallest of Chat's output caps above 0, 0 for none
	optioned, usageForced                 bool            // Chat's stream_options seen, and its include_usage
	usageAt                               int             // where the last usage object the relay rewrote closes in out
	usageMembers                          map[string]rule // the usage members the relay keeps
	replayAssistant, replayEmpty          bool
	replayMeta                            *aggBase
	legacy                                *legacyRequest
	hidesReasoning                        bool
	chatPayload, chatOpaque               bool
	legacyDelta                           *legacyReplyDelta
	cached, zeroUsage                     bool
	cacheCreated                          string
}

func (p *rewriter) copyTo(off int) {
	p.out = append(p.out, p.body[p.at:off]...)
	p.at = off
}

// replace puts s in place of the n-byte value just read.
func (p *rewriter) replace(n int, s string) {
	p.copyTo(p.offset() - n)
	p.out = append(p.out, s...)
	p.at = p.offset()
}
