package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // same-format rewrites preserve raw request members
	"errors"
	"fmt"
)

// rewrite is Prepare for an endpoint of the request's format.
func rewrite(f Format, body []byte, t Target, w Rewrites) ([]byte, error) {
	rules := rulesFor(f, t)
	if rules.members == nil {
		return nil, &NotSupported{Reason: fmt.Sprintf("format %d is not rewritten yet", f)}
	}
	var err error
	body, err = replayBody(f, body, false)
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
	p := &rewriter{
		dec: jsontext.NewDecoder(bytes.NewBuffer(body)), body: body, out: make([]byte, 0, rewriteSize(rules, len(body), t, w)), t: t, w: w,
	}
	if err := p.top(rules); err != nil {
		if errors.Is(err, errHistory) || errors.Is(err, errIdentity) {
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
