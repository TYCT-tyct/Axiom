package protocol_test

import (
	"math"
	"testing"

	"axiom/internal/protocol"
)

// TestPrepareMessagesCap: a Messages request without max_tokens, or with null for it, is sent with Target.MaxOutput, the
// cap it was quoted at — Messages requires one of its own, and the cap sent is the cap reserved (I1). A request that sets
// one keeps it, and a model whose cap is not known leaves it out (README.md「同格式改写」, docs/domains/gateway.md
// 「转发规则」). The member is added before the stream, so a request that names neither is rewritten the same way twice.
func TestPrepareMessagesCap(t *testing.T) {
	for _, c := range []struct {
		name, body string
		max        int64
		streams    bool
		want       string
	}{
		{"none", `{"model":"m","messages":[]}`, 7000, false, `{"model":"u","messages":[],"max_tokens":7000}`},
		{"the largest", `{"model":"m","messages":[]}`, math.MaxInt64, false, `{"model":"u","messages":[],"max_tokens":9223372036854775807}`},
		{"null", `{"model":"m","max_tokens":null,"messages":[]}`, 7000, false, `{"model":"u","max_tokens":7000,"messages":[]}`},
		{"set", `{"model":"m","max_tokens":50,"messages":[]}`, 7000, false, `{"model":"u","max_tokens":50,"messages":[]}`},
		{"nothing to send", `{"model":"m","messages":[]}`, 0, false, `{"model":"u","messages":[]}`},
		{"unknown null", `{"model":"m","max_tokens":null,"messages":[]}`, 0, true, `{"model":"u","max_tokens":null,"messages":[],"stream":true}`},
		{"escaped member", `{"model":"m","max_\u0074okens":null,"messages":[]}`, 7000, false, `{"model":"u","max_\u0074okens":7000,"messages":[]}`},
		{"with the stream", `{"model":"m","messages":[]}`, 7000, true, `{"model":"u","messages":[],"max_tokens":7000,"stream":true}`},
	} {
		r := inspected(t, protocol.Messages, []byte(c.body))
		target := protocol.Target{Format: protocol.Messages, Model: "u", MaxOutput: c.max, Streams: c.streams}
		// The model and the identifiers each get the room a one-byte value takes, and the body holds none of them but
		// the model: what is left over is the room for the added members, so one added without room grows the buffer.
		w := protocol.Rewrites{Identity: func(string) string { return "x" }, IdentityLen: 1}
		u, err := protocol.Prepare(&r, []byte(c.body), target, w)
		if err != nil || string(u.Body) != c.want {
			t.Errorf("%s: got %s (%v), want %s", c.name, u.Body, err, c.want)
		}
		if cost := protocol.PrepareCost(&r, []byte(c.body), target, w); int64(cap(u.Body))+3*int64(len(c.body)) != cost {
			t.Errorf("%s: the buffer was %d bytes, the cost %d: the rewrite outgrew its room", c.name, cap(u.Body), cost)
		}
	}
}
