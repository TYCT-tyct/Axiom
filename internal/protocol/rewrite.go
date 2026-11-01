package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // same-format rewrites preserve raw request members
	"errors"
	"fmt"
)

// rewrite is Prepare for an endpoint of the request's format.
func rewrite(r *Request, body []byte, t Target, w Rewrites) ([]byte, error) {
	return rewriteWithin(r, body, t, w, nil)
}

func rewriteWithin(r *Request, body []byte, t Target, w Rewrites, b *budget) ([]byte, error) {
	f := r.Format
	rules := rulesFor(f, t)
	if rules.members == nil {
		return nil, &NotSupported{Reason: fmt.Sprintf("format %d is not rewritten yet", f)}
	}
	body, indexed, err := rewriteBody(r, body)
	if err != nil {
		return nil, err
	}
	if f == Responses {
		w.History, err = replayHistory(w.History)
		if err != nil {
			return nil, err
		}
	}
	w = w.withDefaults()
	size := rewriteSize(rules, len(body), t, w)
	if err := b.draw(rounded(int64(size))); err != nil {
		return nil, err
	}
	p := &rewriter{
		dec: jsontext.NewDecoder(bytes.NewBuffer(body)), body: body, out: make([]byte, 0, size), t: t, w: w,
	}
	p.reader.b = b
	if indexed {
		err = p.indexedTop(&r.positions, rules)
	} else {
		err = p.top(rules)
	}
	if err != nil {
		if errors.Is(err, errHistory) || errors.Is(err, errIdentity) || errors.Is(err, ErrMemory) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
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

// rewriteBody invalidates Inspect's offsets whenever a compatibility rewrite changes the body.
func rewriteBody(r *Request, body []byte) ([]byte, bool, error) {
	if r.Format == Chat && r.legacy {
		body, err := rewriteLegacy(body)
		return body, false, err
	}
	if r.positions.ready && r.positions.size == len(body) && !replayCandidate(r.Format, body) {
		return body, true, nil
	}
	body, err := replayBody(r.Format, body, false)
	return body, false, err
}

// quoted is the most bytes a string of n bytes takes as a JSON string: the quotes, and six bytes for each byte that
// needs escaping, the longest escape there is.
func quoted(n int) int { return 6*n + len(`""`) }
