package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: Prepare walks the body token by token
	"errors"
	"fmt"
)

// Target is the endpoint an attempt is sent to.
type Target struct {
	// Model is the endpoint's name for the model.
	Model string
	// EncryptedReasoning is set when the model returns its reasoning encrypted on request, as OpenAI's reasoning
	// models do. A model without it refuses a request that asks (README.md「同格式改写」).
	EncryptedReasoning bool
}

// Rewrites are the gateway's values for one request.
type Rewrites struct {
	// Identity maps a client's session, prompt cache or end-user identifier to the opaque value sent upstream in its
	// place (docs/domains/gateway.md「转发规则」).
	Identity func(string) string
	// History is the stored input and output items, oldest first, of the response a Responses request continues
	// with previous_response_id, each one JSON object; empty when it continues none.
	History [][]byte
}

// errHistory reports a gateway that passed a history to a request that continues no response, or none to one that
// does: the request's context would be lost or invented.
var errHistory = errors.New("protocol: previous_response_id and the history passed disagree")

// Prepare rewrites a body Inspect accepted for an endpoint of the same format, changing only the members README.md
// 「同格式改写」 lists: every other byte, signatures and encrypted items included, is copied as it is. Chat's rewrites
// depend on the endpoint's dialect, which comes with the conversion to and from Chat (#27).
func Prepare(f Format, body []byte, t Target, w Rewrites) ([]byte, error) {
	rules := rewriteRules[f]
	if rules.members == nil {
		return nil, fmt.Errorf("protocol: Prepare does not rewrite format %d yet", f)
	}
	size := len(body) + len(t.Model) + 64 // and room for the members added
	for _, item := range w.History {
		size += len(item) + 1
	}
	p := &rewriter{
		dec: jsontext.NewDecoder(bytes.NewBuffer(body)), body: body, out: make([]byte, 0, size), t: t, w: w,
	}
	if err := p.top(rules); err != nil {
		if errors.Is(err, errHistory) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return p.out, nil
}

type rule func(*rewriter) error

// formatRules are one format's rewrites: members to rewrite, and the members to add when the body lacks them.
type formatRules struct {
	members map[string]rule
	add     rule
}

var (
	rewriteRules = [...]formatRules{
		Messages: {members: withRouting(map[string]rule{"model": (*rewriter).model, "metadata": (*rewriter).metadata})},
		Responses: {
			members: withRouting(map[string]rule{
				"model": (*rewriter).model, "previous_response_id": (*rewriter).previous, "input": (*rewriter).input,
				"store": (*rewriter).store, "include": (*rewriter).include, "prompt_cache_key": (*rewriter).identity,
				"user": (*rewriter).identity, "safety_identifier": (*rewriter).identity,
			}),
			add: (*rewriter).addResponses,
		},
		Chat: {},
	}
	metadataRules = map[string]rule{"user_id": (*rewriter).identity}
)

// withRouting adds the members that are OpenRouter's routing parameters or our extensions, which the gateway has
// used and no upstream defines: an official endpoint refuses a member it does not know (gateway.md「路由参数」).
func withRouting(m map[string]rule) map[string]rule {
	for _, name := range []string{"provider", "models", "route", "transforms", "plugins", "usage", "session_id", "axiom"} {
		m[name] = (*rewriter).drop
	}
	return m
}

// encrypted asks Responses for reasoning items that can be sent again, the only way when the upstream stores
// nothing (OpenAI《Create a model response》, include, read 2026-10-14).
const encrypted = "reasoning.encrypted_content"

// rewriter copies a body to out, rewriting members on the way. body[:at] is settled: copied to out or dropped.
type rewriter struct {
	reader
	body, out                             []byte
	at                                    int
	t                                     Target
	w                                     Rewrites
	continues, stored, included, hasInput bool // the Responses members seen
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
	if err := p.members(rules.members); err != nil {
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

// members rewrites the members of the object just opened by rules, and copies the rest.
func (p *rewriter) members(rules map[string]rule) error {
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
			do = (*rewriter).skip
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
	if err != nil {
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
	return p.replaceString(len(v), p.w.Identity(string(s)))
}

func (p *rewriter) replaceString(n int, s string) (err error) {
	p.replace(n, "")
	p.out, err = jsontext.AppendQuote(p.out, s)
	return err
}

func (p *rewriter) metadata() error {
	if ok, err := p.open('{', "an object"); !ok {
		return err
	}
	if err := p.members(metadataRules); err != nil {
		return err
	}
	return p.token()
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
			p.out = append(append(append(p.out, `,{"type":"message","role":"user","content":`...), v...), '}')
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

func (p *rewriter) addResponses() error {
	if p.continues != (len(p.w.History) > 0) {
		return errHistory
	}
	if !p.stored {
		p.out = append(p.out, `,"store":false`...)
	}
	if p.t.EncryptedReasoning && !p.included {
		p.out = append(p.out, `,"include":["`+encrypted+`"]`...)
	}
	if len(p.w.History) > 0 && !p.hasInput {
		p.out = append(p.out, `,"input":[`...)
		p.history()
		p.out = append(p.out, ']')
	}
	return nil
}
