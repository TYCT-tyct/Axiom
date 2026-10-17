package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: Prepare walks the body token by token
	"errors"
	"fmt"
	"strconv"

	"axiom/internal/catalog"
)

// Target is the endpoint an attempt is sent to.
type Target struct {
	// Format is the endpoint's call format.
	Format Format
	// Model is the endpoint's name for the model.
	Model string
	// EncryptedReasoning is set when the model returns its reasoning encrypted on request, as OpenAI's reasoning
	// models do. A model without it refuses a request that asks (README.md「同格式改写」).
	EncryptedReasoning bool
	// The rest is what a conversion needs to know of a Messages model (README.md「跨格式转换」). The gateway fills it
	// from the model's spec: it is data, never guessed from the model's name.
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
	Thinking Thinking
	// MidSystem is set when the model takes a system message after the conversation has started, and
	// UntrustedSystemAfterTools when it reads one right after tool results as text it may not trust (Anthropic
	// 《Mid-conversation system messages》, read 2026-10-13).
	MidSystem, UntrustedSystemAfterTools bool
	// DefaultSamplingOnly is set when the model refuses a temperature or top_p other than the default and a forced
	// tool choice, as Claude 5.5 models do.
	DefaultSamplingOnly bool
	// CacheBreakpoints is set for a Responses model that takes explicit cache breakpoints, as OpenAI's from GPT-5.6
	// on do (OpenAI《Prompt caching》, read 2026-10-16).
	CacheBreakpoints bool
}

// Thinking is how a Messages model is asked to reason.
type Thinking uint8

// The ways to ask.
const (
	// ThinkingAdaptive asks with thinking.type adaptive and output_config.effort.
	ThinkingAdaptive Thinking = iota
	// ThinkingManual asks with a thinking budget, budget_tokens, which the newer models refuse.
	ThinkingManual
)

// Rewrites are the gateway's values for one request.
type Rewrites struct {
	// Identity maps a client's session, prompt cache or end-user identifier to the opaque value sent upstream in its
	// place (docs/domains/gateway.md「转发规则」).
	Identity func(string) string
	// IdentityLen is the most bytes Identity may return, before JSON escaping; 0 means defaultIdentityLen. It sizes the
	// output, so that identifiers that are longer than the ones they replace, or need escaping, do not make Prepare
	// copy the body again; a longer value is refused (errIdentity).
	IdentityLen int
	// History is the stored input and output items, oldest first, of the response a Responses request continues
	// with previous_response_id, each one JSON object; empty when it continues none.
	History [][]byte
}

// defaultIdentityLen is what Identity may return when Rewrites.IdentityLen is 0: derived identifiers are short opaque
// values, and a bound that holds whatever the client sent is what lets the output be sized before it is written.
const defaultIdentityLen = 128

// withDefaults fills in what a zero Rewrites value leaves to its defaults.
func (w Rewrites) withDefaults() Rewrites {
	if w.IdentityLen == 0 {
		w.IdentityLen = defaultIdentityLen
	}
	return w
}

// errIdentity reports a gateway whose Identity returned more than Rewrites.IdentityLen bytes.
var errIdentity = errors.New("protocol: Identity returned more than Rewrites.IdentityLen bytes")

// Upstream is a request ready for an endpoint: its body, and what the body tells the relay about the usage the
// endpoint will report.
type Upstream struct {
	Body  []byte
	Usage UsageContext
}

// NotSupported reports an endpoint that cannot express the request, such as one of another format for a request
// the conversion would change. The gateway skips the endpoint without counting an attempt; it is not the client's
// error, which ErrInvalid is.
type NotSupported struct{ Reason string }

func (e *NotSupported) Error() string {
	return "protocol: the endpoint cannot express the request: " + e.Reason
}

// errHistory reports a gateway that passed a history to a request that continues no response, or none to one that
// does: the request's context would be lost or invented.
var errHistory = errors.New("protocol: previous_response_id and the history passed disagree")

// Prepare readies body, which Inspect read as r, for the endpoint t. For an endpoint of the same format it changes
// only the members README.md「同格式改写」 lists: every other byte, signatures and encrypted items included, is copied
// as it is. A compaction request (Target.Compact) is rewritten by a smaller set of members, and nothing is added to it.
// A Chat request for a Messages or Responses endpoint is converted (README.md「跨格式转换」).
func Prepare(r *Request, body []byte, t Target, w Rewrites) (Upstream, error) {
	u := Upstream{Usage: usageContext(r)}
	u.Usage.CacheTTL1h = u.Usage.CacheTTL1h && t.Format == Messages // only Anthropic keeps a 1-hour TTL
	var err error
	if r.Format == t.Format {
		u.Body, err = rewrite(r.Format, body, t, w)
		return u, err
	}
	if r.Format != Chat {
		return u, &NotSupported{Reason: fmt.Sprintf("format %d is not converted to format %d", r.Format, t.Format)}
	}
	u.Body, err = convert(body, t, w)
	return u, err
}

// usageContext is what the request tells the relay about the usage: a 1-hour cache breakpoint, and the service tier
// when the request names one alone.
func usageContext(r *Request) UsageContext {
	u := UsageContext{CacheTTL1h: r.InputItems.Has(catalog.ItemCacheWrite1h)}
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

// rewrite is Prepare for an endpoint of the request's format. Chat's rewrites depend on the endpoint's dialect,
// which come later (#27).
func rewrite(f Format, body []byte, t Target, w Rewrites) ([]byte, error) {
	rules := rulesFor(f, t)
	if rules.members == nil {
		return nil, &NotSupported{Reason: fmt.Sprintf("format %d is not rewritten yet", f)}
	}
	w = w.withDefaults()
	p := &rewriter{
		dec: jsontext.NewDecoder(bytes.NewBuffer(body)), body: body, out: make([]byte, 0, rewriteSize(rules, len(body), t, w)), t: t, w: w,
	}
	if err := p.top(rules); err != nil {
		if errors.Is(err, errHistory) || errors.Is(err, errIdentity) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return p.out, nil
}

// rewriteSize is the capacity of the buffer a rewrite of an n-byte body writes into: the whole of what it can write.
func rewriteSize(rules formatRules, n int, t Target, w Rewrites) int {
	w = w.withDefaults()
	size := n + quoted(len(t.Model)) + rules.room + rules.identities*quoted(w.IdentityLen)
	for _, item := range w.History {
		size += len(item) + 1
	}
	return size
}

// What a conversion holds at once is bounded by what it is given. It copies the body's strings a few times over, and
// reads them into structures, one for every object or null in an array of them and, in an object whose members it does
// not know, one for every member; a body of nothing but "{}," or "null," makes 224 bytes of message of every 3 or 5.
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
		return int64(rewriteSize(rulesFor(r.Format, t), len(body), t, w)) + 3*n
	}
	if r.Format != Chat {
		return 0 // Prepare refuses at once: nothing is converted from another format
	}
	objects := bytes.Count(body, []byte("{")) + bytes.Count(body, []byte("null")) // a null element is a zero structure too
	return convertPerByte*n + convertPerObject*int64(objects) + convertPerMember*int64(bytes.Count(body, []byte(":")))
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
			}),
			add: (*rewriter).addStream, identities: 1, room: len(addStream),
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
		Chat: {},
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
	}
	metadataRules = map[string]rule{"user_id": (*rewriter).identity}
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

var routingParameters = []string{"provider", "models", "route", "transforms", "plugins", "usage", "session_id", "axiom"}

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
	// addStream asks for the upstream's stream when the endpoint streams one, since the gateway reads every call as a
	// stream and a client that wants no stream gets the events aggregated (docs/domains/gateway.md「转发规则」).
	addStream = `,"stream":true`
)

// rewriter copies a body to out, rewriting members on the way. body[:at] is settled: copied to out or dropped.
type rewriter struct {
	reader
	body, out                             []byte
	at                                    int
	t                                     Target
	w                                     Rewrites
	continues, stored, included, hasInput bool            // the Responses members seen
	streamed                              bool            // the stream member seen
	capped                                bool            // max_output_tokens was given a value
	usageAt                               int             // where the last usage object the relay rewrote closes in out
	usageMembers                          map[string]rule // the usage members the relay keeps
}

func (p *rewriter) offset() int { return int(p.dec.InputOffset()) }

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

func (p *rewriter) identity() error {
	v, err := p.raw()
	if v == nil || err != nil {
		return err
	}
	s, err := p.unquote(v)
	if err != nil {
		return err
	}
	derived := p.w.Identity(string(s))
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
	v, err := p.dec.ReadValue()
	p.replace(len(v), "false")
	return err
}

// outputCap gives max_output_tokens the cap the request was quoted at when it is null, which stands for none.
func (p *rewriter) outputCap() error {
	v, err := p.dec.ReadValue()
	if err == nil && v.Kind() == 'n' && p.t.MaxOutput > 0 {
		p.replace(len(v), strconv.FormatInt(p.t.MaxOutput, 10))
	}
	p.capped = true
	return err
}

func (p *rewriter) include() error {
	p.included = true
	if !p.t.EncryptedReasoning {
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
	v, err := p.dec.ReadValue()
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
	if p.t.EncryptedReasoning && !p.included {
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
	v, err := p.dec.ReadValue()
	if err == nil && p.t.Streams && string(v) != "true" {
		p.replace(len(v), "true")
	}
	return err
}
