package protocol

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: Prepare walks the body token by token
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"axiom/internal/catalog"
)

// Target is the endpoint an attempt is sent to.
type Target struct {
	// Format is the endpoint's call format.
	Format Format
	// Model is the endpoint's name for the model.
	Model string
	// The model's side of the rewrites and conversions is what its spec says (catalog.Dialect), which the gateway
	// copies here: data, never guessed from the model's name. A claim the spec does not make is catalog.ClaimUnset
	// and stands for neither answer: a request that needs the answer is refused (*NotSupported), and one that only
	// profits from a yes is written as for a no (README.md「跨格式转换」).
	//
	// EncryptedReasoning is yes when the model returns its reasoning encrypted on request, as OpenAI's reasoning
	// models do. A model without it refuses a request that asks (README.md「同格式改写」).
	EncryptedReasoning catalog.Claim
	//
	// MaxOutput is the output cap a request that sets none is sent with, 0 for none: the one it was quoted at, which
	// the gateway takes from the route's entry. Messages requires one; Responses gets it as max_output_tokens, whether
	// the request is converted or rewritten.
	MaxOutput int64
	// Streams is set when the endpoint streams a response. The request sent asks for one whatever the client asked,
	// since the gateway reads every answer as a stream and aggregates it for a client that wants none
	// (docs/domains/gateway.md「转发规则」).
	Streams bool
	// Compact is set for a compaction request, the one request that is not a generation: it is rewritten by the
	// members that must not reach an upstream as they are — the model, the identifiers, the routing parameters — and
	// every other byte is copied as the client wrote it (README.md「同格式改写」).
	Compact bool
	// Thinking is how the model is asked to reason.
	Thinking catalog.Thinking
	// MidSystem is yes when the model takes a system message after the conversation has started, and
	// UntrustedSystemAfterTools when it reads one right after tool results as text it may not trust (Anthropic
	// 《Mid-conversation system messages》, read 2026-10-13).
	MidSystem, UntrustedSystemAfterTools catalog.Claim
	// DefaultSamplingOnly is yes when the model refuses a temperature or top_p other than the default, as Claude Opus
	// 4.7 and later do. ForcedToolChoice is yes when it takes a tool choice that forces a call, which Claude Opus 5.5,
	// Sonnet 5.5 and Fable 5.1 refuse; the vendor decides the two apart.
	DefaultSamplingOnly, ForcedToolChoice catalog.Claim
	// CacheBreakpoints is yes for a Responses model that takes explicit cache breakpoints, as OpenAI's from GPT-5.6
	// on do (OpenAI《Prompt caching》, read 2026-10-16).
	CacheBreakpoints catalog.Claim
}

// Rewrites are the gateway's values for one request.
type Rewrites struct {
	// Identity maps a client's session, prompt cache or end-user identifier to the opaque value sent upstream in its
	// place (docs/domains/gateway.md「转发规则」).
	Identity func(string) string
	// Device is the value that stands for the client's machine in Anthropic's metadata.user_id, whose session member is
	// Identity's value for the same request: a relay of this deployment reads the session out of it to pin a
	// conversation to one account (docs/domains/gateway.md「转发规则」). It is written into the object as it is, so it
	// must be an opaque identifier, as the ones Identity returns are. Both components are JSON-encoded.
	Device string
	// IdentityLen is the most bytes a derived identifier may take before outer JSON escaping; for metadata.user_id,
	// this includes the inner JSON encoding of the structure and both components. 0 means
	// defaultIdentityLen. It sizes the output, so that identifiers that are longer than the ones they replace, or need
	// escaping, do not make Prepare copy the body again; a longer value is refused (errIdentity).
	IdentityLen int
	// History is the stored input and output items, oldest first, of the response a Responses request continues
	// with previous_response_id, each one JSON object; empty when it continues none.
	History [][]byte
}

// defaultIdentityLen is what a derived value may take when Rewrites.IdentityLen is 0: derived identifiers are short
// opaque values, and a bound that holds whatever the client sent is what lets the output be sized before it is written.
const defaultIdentityLen = 128

// withDefaults fills in what a zero Rewrites value leaves to its defaults.
func (w Rewrites) withDefaults() Rewrites {
	if w.IdentityLen == 0 {
		w.IdentityLen = defaultIdentityLen
	}
	return w
}

// errIdentity reports a gateway whose Identity returned more than Rewrites.IdentityLen bytes.
var errIdentity = errors.New("protocol: a derived value is longer than Rewrites.IdentityLen bytes")

// Upstream is a request ready for an endpoint: its body, and what the body tells the relay about the usage the
// endpoint will report.
type Upstream struct {
	Body  []byte
	Usage UsageContext
}

// NotSupported reports an endpoint that cannot express the request, such as one of another format for a request
// the conversion would change. The gateway skips the endpoint without counting an attempt; it is not the client's
// error, which ErrInvalid is. Reason may reach the client, when no endpoint is left; Detail, if there is one, is why for
// the operator only (the log), such as which spec key is missing, and never leaves the node.
type NotSupported struct{ Reason, Detail string }

// unspecified is an endpoint whose spec does not say whether the model takes what: the client is told only that the
// model does not support it yet, and the key to fill in goes to the operator.
func unspecified(what, detail string) *NotSupported {
	return &NotSupported{Reason: "the model does not support " + what + " yet", Detail: "the spec does not say " + detail}
}

func (e *NotSupported) Error() string {
	return "protocol: the endpoint cannot express the request: " + e.Reason
}

// errHistory reports a gateway that passed a history to a request that continues no response, or none to one that
// does: the request's context would be lost or invented.
var errHistory = errors.New("protocol: previous_response_id and the history passed disagree")

// Prepare readies body, which Inspect read as r, for the endpoint t. For an endpoint of the same format it changes
// only the members README.md「同格式改写」 lists: every other byte, signatures and encrypted items included, is copied
// as it is. A compaction request (Target.Compact) is rewritten by a smaller set of members, and nothing is added to it.
// Chat and Messages/Responses requests convert in either direction (README.md「跨格式转换」).
func Prepare(r *Request, body []byte, t Target, w Rewrites) (Upstream, error) {
	u := Upstream{Usage: usageContext(r)}
	u.Usage.CacheTTL1h = u.Usage.CacheTTL1h && t.Format == Messages // only Anthropic keeps a 1-hour TTL
	var err error
	if r.Format == t.Format {
		u.Body, err = rewrite(r, body, t, w)
		return u, err
	}
	if t.Format == Chat && !t.Compact && (r.Format == Messages || r.Format == Responses) {
		u.Body, err = requestToChat(r.Format, body, t, w)
		return u, err
	}
	if r.Format != Chat {
		return u, &NotSupported{Reason: fmt.Sprintf("format %d is not converted to format %d", r.Format, t.Format)}
	}
	u.Body, err = convert(body, t, w)
	return u, err
}

// usageContext carries the request's metering facts and legacy reply shape through an upstream attempt.
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

// What a conversion holds at once is bounded by what it is given. It copies the body's strings a few times over, and
// reads them into structures, one for every object or null in an array of them and, in an object whose members it does
// not know, one for every member; a body of nothing but "{}," or "null," makes 248 bytes of message of every 3 or 5.
// The body does not say how many there are, but counting its braces, nulls and colons bounds them (any other element
// of such an array is refused where it stands): those inside strings only add. The constants are
// the structures' sizes, doubled for the growth of the slices they sit in, with room to spare (measured 2026-10-16, a
// body of "{}," allocates 1.3 KiB for each object, and 100,000 unknown members 485 bytes each); TestPrepareCost holds
// them to what bodies of the worst shapes allocate.
const (
	convertPerByte   = 6
	convertPerObject = 2048
	convertPerMember = 1024
)

// PrepareCost is the most memory Prepare holds at once for a request, besides the body itself, so that the node's
// budget is asked for it before Prepare allocates it. A rewrite writes into one buffer whose capacity is known before
// it starts, and unquotes an identifier or a response ID into a scratch, copies it to a string and grows the scratch by
// doubling, which the body's size bounds three times over (2.5 measured, for an identifier of escapes); a conversion's
// working set is bounded by the body's size, braces and colons (above). What stays is the capacity of Upstream.Body,
// which is never more than the cost.
func PrepareCost(r *Request, body []byte, t Target, w Rewrites) int64 {
	n := int64(len(body))
	if r.Format == t.Format {
		cost := int64(rewriteSize(rulesFor(r.Format, t), len(body), t, w)) + 3*n + replayCost(r.Format, body)
		if r.Format == Responses {
			cost += replayHistoryCost(w.History)
		}
		if r.Format == Chat && r.legacy {
			cost += nativeRequestCost(body)
		}
		return cost
	}
	if r.Format != Chat && t.Format != Chat {
		return 0
	}
	objects := bytes.Count(body, []byte("{")) + bytes.Count(body, []byte("null")) // a null element is a zero structure too
	if t.Format == Chat {
		cost := nativeRequestCost(body)
		for _, item := range w.History {
			cost += nativeRequestCost(item)
		}
		return cost + int64(quoted(len(t.Model))+3*quoted(w.withDefaults().IdentityLen))
	}
	cost := convertPerByte*n + convertPerObject*int64(objects) + convertPerMember*int64(bytes.Count(body, []byte(":")))
	return cost
}

// quoted is the most bytes a string of n bytes takes as a JSON string: the quotes, and six bytes for each byte that
// needs escaping, the longest escape there is.
func quoted(n int) int { return 6*n + len(`""`) }

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
	src                                   bytes.Buffer
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

func (p *rewriter) top(rules formatRules) error {
	if err := p.token(); err != nil {
		return err
	}
	if err := p.members(rules.members, (*rewriter).skip); err != nil {
		return err
	}
	// Inspect accepts no body without a model, which is rewritten, never dropped, so a member precedes the added ones.
	if rules.add != nil {
		if err := rules.add(p); err != nil {
			return err
		}
	}
	if err := p.token(); err != nil {
		return err
	}
	p.copyTo(len(p.body))
	return nil
}

// members rewrites the members of the object just opened by rules, and the rest by other.
func (p *rewriter) members(rules map[string]rule, other rule) error {
	first, kept := p.offset(), false
	p.copyTo(first)
	for p.dec.PeekKind() != '}' {
		if from := p.offset(); !kept && from != first {
			// Every member before this one was dropped, and so is the comma that led this one.
			p.at = from + bytes.IndexByte(p.body[from:], ',') + 1
		}
		if err := p.member(); err != nil {
			return err
		}
		do := rules[string(p.name)]
		if do == nil {
			do = other
		}
		n := len(p.out)
		if err := do(p); err != nil {
			return err
		}
		p.copyTo(p.offset())
		kept = kept || len(p.out) > n
	}
	return nil
}

func (p *rewriter) drop() error {
	err := p.skip()
	p.at = p.offset()
	return err
}

func (p *rewriter) model() error {
	v, err := p.raw()
	if v == nil || err != nil {
		return err
	}
	return p.replaceString(len(v), p.t.Model)
}

// identity replaces the identifier just read with the gateway's opaque value of it.
func (p *rewriter) identity() error { return p.replaceDerived(p.w.Identity) }

// replaceDerived reads the string just read and writes derive's value of it in its place, refusing one longer than
// Rewrites.IdentityLen.
func (p *rewriter) replaceDerived(derive func(string) string) error {
	v, err := p.raw()
	if v == nil || err != nil {
		return err
	}
	s, err := p.unquote(v)
	if err != nil {
		return err
	}
	derived := derive(string(s))
	if p.w.IdentityLen > 0 && len(derived) > p.w.IdentityLen {
		return errIdentity
	}
	return p.replaceString(len(v), derived)
}

func (p *rewriter) replaceString(n int, s string) (err error) {
	p.replace(n, "")
	p.out, err = jsontext.AppendQuote(p.out, s)
	return err
}

// object rewrites an object's members by rules and copies the rest; null is copied too.
func object(rules map[string]rule) rule {
	return func(p *rewriter) error {
		if ok, err := p.open('{', "an object"); !ok {
			return err
		}
		if err := p.members(rules, (*rewriter).skip); err != nil {
			return err
		}
		return p.token()
	}
}

func (p *rewriter) previous() error {
	v, err := p.str()
	p.continues = len(v) > 0
	p.at = p.offset()
	return err
}

// store turns the upstream's storage off: Axiom keeps the state that continues a response (gateway.md
// 「Responses 的会话状态」).
func (p *rewriter) store() error {
	p.stored = true
	v, err := p.value()
	p.replace(len(v), "false")
	return err
}

// outputCap gives the output cap member, max_tokens or max_output_tokens, the cap the request was quoted at when it is
// null or zero: Inspect treats both as unset.
func (p *rewriter) outputCap() error {
	v, err := p.value()
	if err == nil && p.t.MaxOutput > 0 && (v.Kind() == 'n' || numberIs(v, 0)) {
		p.replace(len(v), strconv.FormatInt(p.t.MaxOutput, 10))
	}
	p.capped = true
	return err
}

func (p *rewriter) include() error {
	p.included = true
	if p.t.EncryptedReasoning != catalog.ClaimYes {
		return p.skip()
	}
	if ok, err := p.open('[', "an array"); !ok {
		if err == nil {
			p.replace(len("null"), `["`+encrypted+`"]`)
		}
		return err
	}
	start, has := p.offset(), false
	for p.dec.PeekKind() != ']' {
		is, err := p.is(encrypted)
		if err != nil {
			return err
		}
		has = has || is
	}
	if !has {
		empty := p.offset() == start
		p.copyTo(start)
		p.out = append(p.out, `"`+encrypted+`"`...)
		if !empty {
			p.out = append(p.out, ',')
		}
	}
	return p.token()
}

// input puts the history before the request's own input, which a string stands for as one user message (OpenAI
// 《Create a model response》, input).
func (p *rewriter) input() error {
	p.hasInput = true
	if len(p.w.History) == 0 {
		return p.skip()
	}
	v, err := p.value()
	if err != nil {
		return err
	}
	switch {
	case v.Kind() == '[':
		p.copyTo(p.offset() - len(v) + 1)
		p.history()
		if len(bytes.TrimSpace(v[1:len(v)-1])) > 0 {
			p.out = append(p.out, ',')
		}
	case v.Kind() == '"' || v.Kind() == 'n':
		p.replace(len(v), "[")
		p.history()
		if v.Kind() == '"' {
			p.out = append(append(append(p.out, wrapInput...), v...), '}')
		}
		p.out = append(p.out, ']')
	default:
		return errors.New("input is not a string or an array")
	}
	return nil
}

func (p *rewriter) history() {
	for i, item := range p.w.History {
		if i > 0 {
			p.out = append(p.out, ',')
		}
		p.out = append(p.out, item...)
	}
}

// addStream asks for the upstream's stream when the endpoint streams one and the request did not name it: the gateway
// reads a stream for every call and aggregates it for a client that wants none (docs/domains/gateway.md「转发规则」).
func (p *rewriter) addStream() error {
	if p.t.Streams && !p.streamed {
		p.out = append(p.out, addStream...)
	}
	return nil
}

// addMessages appends what a Messages request lacks: the output cap it was quoted at, which Messages requires, when the
// model's cap is known, and the stream (README.md「同格式改写」, docs/domains/gateway.md「转发规则」).
func (p *rewriter) addMessages() error {
	if !p.capped && p.t.MaxOutput > 0 {
		p.out = strconv.AppendInt(append(p.out, addCap...), p.t.MaxOutput, 10)
	}
	return p.addStream()
}

// addResponses appends what a Responses request lacks.
func (p *rewriter) addResponses() error {
	if p.continues != (len(p.w.History) > 0) {
		return errHistory
	}
	if !p.stored {
		p.out = append(p.out, addStore...)
	}
	if !p.capped && p.t.MaxOutput > 0 {
		p.out = strconv.AppendInt(append(p.out, addOutputCap...), p.t.MaxOutput, 10)
	}
	if p.t.EncryptedReasoning == catalog.ClaimYes && !p.included {
		p.out = append(p.out, addInclude...)
	}
	if len(p.w.History) > 0 && !p.hasInput {
		p.out = append(p.out, addInput...)
		p.history()
		p.out = append(p.out, ']')
	}
	return p.addStream()
}

// stream asks for the upstream's stream whatever the client asked, when the endpoint streams one: the gateway relays
// every answer as a stream, and a client that wants no stream gets the events aggregated
// (docs/domains/gateway.md「转发规则」).
func (p *rewriter) stream() error {
	p.streamed = true
	v, err := p.value()
	if err == nil && p.t.Streams && v.Kind() != 't' {
		p.replace(len(v), "true")
	}
	return err
}

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
