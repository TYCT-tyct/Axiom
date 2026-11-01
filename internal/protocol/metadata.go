package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // metadata.user_id contains JSON inside a JSON string
	"slices"
	"strings"
)

// userID replaces Anthropic's metadata.user_id with the structure a relay of this deployment reads the session out of.
func (p *rewriter) userID() error {
	v, err := p.raw()
	if v == nil || err != nil {
		return err
	}
	s, err := p.unquote(v)
	if err != nil {
		return err
	}
	if err := p.b.draw(rounded(int64(len(s)))); err != nil { // string(s)
		return err
	}
	p.replace(len(v), "")
	p.out, err = appendMetadataUserID(p.out, p.w, string(s))
	return err
}

// appendMetadataUserID writes Anthropic's metadata.user_id for the client identifier v: the device, and the session derived from v
// beside it, in the structure a relay of this deployment parses to pin a conversation to one account
// (docs/domains/gateway.md「转发规则」). It is a JSON string whose content is JSON, which is the shape Claude Code sends,
// so a reader that takes the member for a string and a reader that parses it both find what they expect.
func appendMetadataUserID(out []byte, w Rewrites, v string) ([]byte, error) {
	if strings.TrimSpace(v) == "" {
		return append(out, `""`...), nil // No declared session: let the relay fall back to conversation content.
	}
	session := w.Identity(v)
	start := len(out)
	out = appendQuoted(append(out, `{"device_id":`...), w.Device)
	out = appendQuoted(append(out, `,"account_uuid":"","session_id":`...), session)
	out = append(out, '}')
	end := len(out)
	if end-start > w.withDefaults().IdentityLen {
		return nil, errIdentity
	}
	// The inner JSON is valid UTF-8 without control bytes, so outer quoting needs at most twice its length plus quotes.
	// Keep source and destination disjoint, then compact in place; both fit inside rewriteSize's sixfold allowance.
	out = slices.Grow(out, 2*(end-start)+2)
	outer, err := jsontext.AppendQuote(out[end:], out[start:end:end])
	if err != nil {
		return nil, err
	}
	n := copy(out[start:cap(out)], outer)
	return out[:start+n], nil
}
