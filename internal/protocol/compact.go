package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in responses.go: the reply is read token by token, in place
	"errors"
	"fmt"
	"io"

	"axiom/internal/platform/jsonx"
)

// The compact endpoint answers one Responses body, not a stream: its usage is read and replaced in the whole reply.

// UsageOf reads the usage of a complete Responses reply, for the calls the gateway passes through whole: the compact
// endpoint does not stream, so its body is not relayed (docs/domains/gateway.md「转发规则」, OpenAI《Compaction》,
// read 2026-10-14). A reply that names no usage reports nothing of it, which the call estimates rather than waives; one
// protocol cannot read or map is invalid, which billing estimates too (docs/domains/billing.md「免单规则」).
func UsageOf(body []byte) Usage {
	m := meter{final: true}
	members, err := membersOf(body)
	if err != nil {
		m.invalid = fmt.Errorf("usage: %w", err)
		return m.result()
	}
	var report *openAIUsage
	var tier *string
	for _, mem := range members {
		switch mem.name {
		case "usage":
			report = &openAIUsage{}
			if err := jsonx.Unmarshal(mem.value, report); err != nil {
				m.invalid = fmt.Errorf("usage: %w", err)
				return m.result()
			}
		case "service_tier":
			var s string
			if jsonx.Unmarshal(mem.value, &s) == nil {
				tier = &s
			}
		}
	}
	if report != nil {
		if r, err := report.items(tier); err != nil {
			m.invalid = err
		} else {
			m.report(r, nil)
		}
	}
	return m.result()
}

// ValidateCompactReply checks that a compact reply is one JSON object, whose usage the gateway replaces before the
// client sees it: a reply the upstream broke is given up on for another entry while nothing has been committed.
func ValidateCompactReply(body []byte) error {
	return compactReplyMembers(body, nil)
}

// compactKind is the one thing RewriteCompactReply needs to know about a member's name.
type compactKind uint8

const (
	compactOther compactKind = iota
	compactUsage
	compactAxiom
)

// compactMember is one top-level member of a compact reply: where its key and value are. Nothing is copied out of the
// body, so a large member costs no second buffer before the rewrite.
type compactMember struct {
	kind                      compactKind
	keyStart, valueStart, end int
}

// compactName reads a member's name only far enough to recognize the two Axiom owns. A name without an escape is
// compared as it stands; only an escaped name needs unquoting, so ordinary members allocate nothing.
func compactName(n jsontext.Value) (compactKind, error) {
	switch {
	case bytes.Equal(n, []byte(`"usage"`)):
		return compactUsage, nil
	case bytes.Equal(n, []byte(`"axiom"`)):
		return compactAxiom, nil
	case bytes.IndexByte(n, '\\') < 0:
		return compactOther, nil
	}
	key, err := jsontext.AppendUnquote(nil, n)
	if err != nil {
		return compactOther, err
	}
	switch string(key) {
	case "usage":
		return compactUsage, nil
	case "axiom":
		return compactAxiom, nil
	}
	return compactOther, nil
}

// compactReplyMembers reads a compact reply's top-level members, validating that it is one object and nothing follows
// it, and hands each to fn. It keeps no list: a reply with many members must not cost another buffer before the rewrite.
func compactReplyMembers(body []byte, fn func(compactMember)) error {
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	k, err := dec.ReadToken()
	if err != nil || k.Kind() != '{' {
		return fmt.Errorf("%w: a compact reply is not a JSON object", ErrUpstream)
	}
	for dec.PeekKind() != '}' {
		n, err := dec.ReadValue()
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUpstream, err)
		}
		kind, err := compactName(n)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUpstream, err)
		}
		keyStart := int(dec.InputOffset()) - len(n)
		v, err := dec.ReadValue()
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUpstream, err)
		}
		end := int(dec.InputOffset())
		if fn != nil {
			fn(compactMember{kind: kind, keyStart: keyStart, valueStart: end - len(v), end: end})
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: data after a compact reply", ErrUpstream)
	}
	return nil
}

// RewriteCompactReply writes the reply a compact call's client gets, from the settlement fin: the upstream's members as
// they came, except that its usage is Axiom's usage and cost, so a compact call reports what it used and was charged
// like every other call. A pending settlement has no cost and says so, as the streams' endings do
// (docs/domains/gateway.md「转发规则」). A member the upstream should not have sent under our name, axiom, is dropped so
// the settlement state appears once and is ours.
func RewriteCompactReply(body []byte, seen Usage, fin *Final) ([]byte, error) {
	u, costString, axiom := reply(seen), "", ""
	if fin == nil || fin.Pending {
		axiom = `"axiom":{"billing_pending":true}`
	} else {
		u, costString = reply(fin.Usage), cost(fin, Responses)
	}
	usage := appendOpenAIUsage(make([]byte, 0, 512), responsesNames, u, costString)
	// One allocation: the members kept, the usage key, our axiom, and the commas between them all fit.
	out := make([]byte, 0, len(body)+len(usage)+len(axiom)+16)
	sep, hasUsage := byte('{'), false // sep opens the object before the first member and separates the rest
	err := compactReplyMembers(body, func(m compactMember) {
		switch m.kind {
		case compactAxiom:
		case compactUsage:
			if !hasUsage {
				out = append(append(append(out, sep), body[m.keyStart:m.valueStart]...), usage...)
				sep, hasUsage = ',', true
			}
		case compactOther:
			out = append(append(out, sep), body[m.keyStart:m.end]...)
			sep = ','
		}
	})
	if err != nil {
		return nil, err
	}
	if !hasUsage {
		out = append(append(append(out, sep), `"usage":`...), usage...)
	}
	// The usage is written either way, so a comma always comes before our axiom.
	if axiom != "" {
		out = append(append(out, ','), axiom...)
	}
	return append(out, '}'), nil
}
