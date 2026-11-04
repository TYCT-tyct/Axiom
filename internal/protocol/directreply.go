package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in direct.go: the reply is read token by token
	"errors"
	"fmt"
	"io"
	"strconv"

	"axiom/internal/catalog"
)

// DirectUsage reads the usage of an embeddings or rerank reply, in the items the catalog prices. A reply without usage
// is an attempt that reported none, which billing estimates; a usage that cannot be read is Invalid, as UsageOf's is.
func DirectUsage(f Format, body []byte) (Usage, error) {
	v, err := usageValue(body)
	if err != nil {
		return Usage{}, err
	}
	if v == nil {
		return Usage{}, nil
	}
	//nolint:modernize // the embedded reader's fields are clearer at this boundary
	d := directScanner{
		reader: reader{dec: jsontext.NewDecoder(bytes.NewReader(v)), name: []byte("usage")},
		r:      Request{Format: f},
	}
	u, err := d.usageObject()
	if err != nil {
		return Usage{Invalid: err}, nil //nolint:nilerr // an invalid usage is reported in Usage.Invalid, not as a failed reply
	}
	return u, nil
}

// usageValue returns the reply's usage member, or nil when it has none.
func usageValue(body []byte) (jsontext.Value, error) {
	members, err := topReply(body, "usage")
	if err != nil {
		return nil, err
	}
	m, ok := members["usage"]
	if !ok {
		return nil, nil
	}
	return body[m.off : m.off+m.n], nil
}

func (d *directScanner) usageObject() (Usage, error) {
	if d.dec.PeekKind() == 'n' {
		return Usage{}, nil
	}
	if d.dec.PeekKind() != '{' {
		return Usage{}, fmt.Errorf("usage is not an object")
	}
	if err := d.token(); err != nil {
		return Usage{}, err
	}
	var c directCounts
	for d.dec.PeekKind() != '}' {
		if err := d.member(); err != nil {
			return Usage{}, err
		}
		if err := d.usageMember(&c); err != nil {
			return Usage{}, err
		}
	}
	if err := d.token(); err != nil {
		return Usage{}, err
	}
	counts := map[catalog.Item]int64{}
	if c.input > 0 {
		counts[catalog.ItemInput] = c.input
	}
	if c.searches > 0 {
		counts[catalog.ItemWebSearch] = c.searches
	}
	u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
	if err != nil {
		return Usage{}, err
	}
	return Usage{Segments: []Segment{{Usage: u, Reported: c.seen}}}, nil
}

// directCounts are the items a direct reply's usage reports, each the largest of the members that count it.
type directCounts struct {
	input, searches int64
	seen            catalog.ItemSet
}

// usageMember reads the usage member just named into c.
func (d *directScanner) usageMember(c *directCounts) error {
	var into *int64
	var item catalog.Item
	switch string(d.name) {
	case "prompt_tokens", "total_tokens":
		into, item = &c.input, catalog.ItemInput
	case "search_units":
		into, item = &c.searches, catalog.ItemWebSearch
	default:
		return d.skip()
	}
	n, err := d.count()
	if err != nil {
		return err
	}
	*into = max(*into, n)
	c.seen |= 1 << item
	return nil
}

// ValidateDirectReply checks that a direct reply has the member its format answers with, before the gateway settles it:
// a reply the upstream broke is retried on another endpoint while nothing has been committed.
func ValidateDirectReply(f Format, body []byte) error {
	_, err := replyMembers(f, body)
	return err
}

// RewriteDirectReply writes the reply a direct call's client gets, from the settlement fin: the upstream's data or
// results, Axiom's call ID and model, and the usage and cost the settlement charged. A pending settlement has no cost
// and says so, as the streams' endings do.
func RewriteDirectReply(f Format, body []byte, id, model string, seen Usage, fin *Final) ([]byte, error) {
	u, costString, top := reply(seen), "", ""
	if fin == nil || fin.Pending {
		top = `,"axiom":{"billing_pending":true}`
		if fin != nil { // the account's key is worth reporting even before the settlement is written
			costString = cost(fin, f)
		}
	} else {
		u, costString = reply(fin.Usage), cost(fin, f)
	}
	return rewriteReply(f, body, id, model, u, costString, top)
}

// rewriteReply writes the reply a direct call's client gets: the upstream's, with its identity and cost replaced by
// Axiom's, in the shape OpenRouter documents for the format (OpenRouter《Embeddings》 and 《Rerank》, read 2026-10-30).
//
//nolint:exhaustive // only the two direct formats reach the encoder
func rewriteReply(f Format, body []byte, id, model string, u catalog.Usage, cost, top string) ([]byte, error) {
	members, err := replyMembers(f, body)
	if err != nil {
		return nil, err
	}
	size := len(body) + len(id) + len(model) + len(cost) + 512
	out := make([]byte, 0, size)
	switch f {
	case Embeddings:
		out = append(out, `{"object":`...)
		out = append(out, memberBytes(body, members["object"])...)
		out = append(out, `,"data":`...)
		out = append(out, memberBytes(body, members["data"])...)
		out = append(out, `,"model":`...)
		out = appendQuoted(out, model)
		out = append(out, `,"usage":`...)
		out = appendEmbeddingUsage(out, u, cost)
	case Rerank:
		out = append(out, `{"id":`...)
		out = appendQuoted(out, id)
		out = append(out, `,"model":`...)
		out = appendQuoted(out, model)
		out = append(out, `,"results":`...)
		out = append(out, memberBytes(body, members["results"])...)
		out = append(out, `,"usage":`...)
		out = appendRerankUsage(out, u, cost)
	default:
		return nil, &NotSupported{Reason: fmt.Sprintf("format %d is not a direct reply", f)}
	}
	if top != "" {
		out = append(out, top...)
	}
	return append(out, '}'), nil
}

// topReply reads the named members of a reply object, accepting leading whitespace and refusing anything after the
// object: the usage a call is charged by and the data a client reads must come from one complete JSON value.
func topReply(body []byte, want ...string) (map[string]replyRef, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	k, err := dec.ReadToken()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if k.Kind() != '{' {
		return nil, fmt.Errorf("%w: a direct reply is not a JSON object", ErrUpstream)
	}
	members := make(map[string]replyRef, len(want))
	for dec.PeekKind() != '}' {
		n, err := dec.ReadValue()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
		}
		key, err := jsontext.AppendUnquote(nil, n)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
		}
		v, err := dec.ReadValue()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
		}
		if name, ok := wanted(want, key); ok {
			end := int(dec.InputOffset())
			members[name] = replyRef{off: end - len(v), n: len(v), kind: byte(v.Kind())}
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: data after a direct reply", ErrUpstream)
	}
	return members, nil
}

// wanted is the name of want that key spells, and false when it spells none.
func wanted(want []string, key []byte) (string, bool) {
	for _, name := range want {
		if string(key) == name {
			return name, true
		}
	}
	return "", false
}

// replyMembers reads the members a direct reply keeps: its own object and data (embeddings) or results (rerank). A
// reply without them is the upstream breaking its format.
//
//nolint:exhaustive // only the two direct formats reach the reply reader
func replyMembers(f Format, body []byte) (map[string]replyRef, error) {
	members, err := topReply(body, "object", "data", "results")
	if err != nil {
		return nil, err
	}
	switch f {
	case Embeddings:
		if m, ok := members["data"]; !ok || m.kind != '[' {
			return nil, fmt.Errorf("%w: an embeddings reply has no data array", ErrUpstream)
		}
		if m, ok := members["object"]; !ok || m.kind != '"' {
			members["object"] = replyRef{literal: `"list"`}
		}
	case Rerank:
		if m, ok := members["results"]; !ok || m.kind != '[' {
			return nil, fmt.Errorf("%w: a rerank reply has no results array", ErrUpstream)
		}
	}
	return members, nil
}

// replyRef points at a member's value in the reply body; literal stands for one Axiom supplies when the upstream did
// not. No value is copied out of the body: a 16 MiB data or results array must not become a second 16 MiB slice.
type replyRef struct {
	off, n  int
	kind    byte
	literal string
}

func memberBytes(body []byte, m replyRef) []byte {
	if m.off == 0 && m.n == 0 {
		return []byte(m.literal)
	}
	return body[m.off : m.off+m.n]
}

// appendIntMember appends "name":n, without a leading comma.
func appendIntMember(dst []byte, name string, n int64) []byte {
	dst = appendQuoted(dst, name)
	dst = append(dst, ':')
	return strconv.AppendInt(dst, n, 10)
}
