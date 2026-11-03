package protocol_test

import (
	"fmt"
	"math"
	"testing"

	"axiom/internal/protocol"
)

// Zero and null have the same meaning as an omitted cap to admission. The buffer
// must also fit the longest replacement without growing beyond PrepareCost.
func TestPrepareResponsesCap(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		limit   string
		max     int64
		compact bool
		want    string
	}{
		{"0", 7000, false, "7000"},
		{"-0", math.MaxInt64, false, "9223372036854775807"},
		{"0", 1, false, "1"},
		{"null", 7000, false, "7000"},
		{"", 7000, false, "7000"},
		{"1", 7000, false, "1"},
		{"9223372036854775807", 1, false, "9223372036854775807"},
		{"0", 0, false, "0"},
		{"-0", 0, false, "-0"},
		{"null", 0, false, "null"},
		{"", 0, false, ""},
		{"0", 7000, true, "0"},
		{"-0", 7000, true, "-0"},
		{"null", 7000, true, "null"},
		{"", 7000, true, ""},
	} {
		t.Run(fmt.Sprintf("cap=%s/max=%d/compact=%t", c.limit, c.max, c.compact), func(t *testing.T) {
			member := func(value string) string {
				if value == "" {
					return ""
				}
				return `,"max_output_tokens":` + value
			}
			body := []byte(`{"model":"m","input":"Hi"` + member(c.limit) + `}`)
			r := inspected(t, protocol.Responses, body)
			target := protocol.Target{Format: protocol.Responses, Model: "u", MaxOutput: c.max, Streams: true, Compact: c.compact}
			w := protocol.Rewrites{IdentityLen: 1}
			u, err := protocol.Prepare(&r, body, target, w)
			want := responsesCapBody(c.limit, c.want, c.compact)
			if err != nil || string(u.Body) != want {
				t.Fatalf("got %s (%v), want %s", u.Body, err, want)
			}
			cost := protocol.PrepareCost(&r, body, target, w)
			if int64(cap(u.Body))+3*int64(len(body)) > cost {
				t.Fatalf("buffer %d exceeds budget %d", cap(u.Body), cost)
			}
		})
	}
}

func TestPrepareResponsesEscapedZeroCap(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"m","input":"Hi","max_output_\u0074okens":-0}`)
	r := inspected(t, protocol.Responses, body)
	u, err := protocol.Prepare(&r, body, protocol.Target{Format: protocol.Responses, Model: "u", MaxOutput: 7000}, protocol.Rewrites{})
	want := `{"model":"u","input":"Hi","max_output_\u0074okens":7000,"store":false}`
	if err != nil || string(u.Body) != want {
		t.Fatalf("got %s (%v), want %s", u.Body, err, want)
	}
}

func responsesCapBody(limit, wantCap string, compact bool) string {
	want := `{"model":"u","input":"Hi"`
	if limit != "" {
		want += `,"max_output_tokens":` + wantCap
	}
	if !compact {
		want += `,"store":false`
		if limit == "" && wantCap != "" {
			want += `,"max_output_tokens":` + wantCap
		}
		want += `,"stream":true`
	}
	return want + `}`
}

// TestPrepareMessagesCap: a Messages request without max_tokens, or with null or zero for it, is sent with Target.MaxOutput, the
// cap it was quoted at — Messages requires one of its own, and the cap sent is the cap reserved (I1). A request that sets
// one keeps it, and a model whose cap is not known leaves it out (README.md「同格式改写」, docs/domains/gateway.md
// 「转发规则」). The member is added before the stream, so a request that names neither is rewritten the same way twice.
func TestPrepareMessagesCap(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, body string
		max        int64
		streams    bool
		want       string
	}{
		{"none", `{"model":"m","messages":[]}`, 7000, false, `{"model":"u","messages":[],"max_tokens":7000}`},
		{"minimum default", `{"model":"m","messages":[]}`, 1, false, `{"model":"u","messages":[],"max_tokens":1}`},
		{"unknown negative zero", `{"model":"m","messages":[],"max_tokens":-0}`, 0, false, `{"model":"u","messages":[],"max_tokens":-0}`},
		{"the largest", `{"model":"m","messages":[]}`, math.MaxInt64, false, `{"model":"u","messages":[],"max_tokens":9223372036854775807}`},
		{"null", `{"model":"m","max_tokens":null,"messages":[]}`, 7000, false, `{"model":"u","max_tokens":7000,"messages":[]}`},
		{"zero", `{"model":"m","max_tokens":0,"messages":[]}`, 7000, false, `{"model":"u","max_tokens":7000,"messages":[]}`},
		{"negative zero", `{"model":"m","max_tokens":-0,"messages":[]}`, 7000, true, `{"model":"u","max_tokens":7000,"messages":[],"stream":true}`},
		{"unknown zero", `{"model":"m","max_tokens":0,"messages":[]}`, 0, false, `{"model":"u","max_tokens":0,"messages":[]}`},
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
		if cost := protocol.PrepareCost(&r, []byte(c.body), target, w); int64(cap(u.Body))+3*int64(len(c.body)) > cost {
			t.Errorf("%s: the buffer was %d bytes, the cost %d: the rewrite exceeded its budget", c.name, cap(u.Body), cost)
		}
	}
}
