package protocol_test

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// TestRewriteCompactReply: a compact call's reply reports Axiom's usage and cost in its top-level usage, and every
// other member stays where and as it was; a settlement that was not written costs nothing and says so
// (docs/domains/gateway.md「转发规则」).
//
//nolint:gocognit // the table and its assertions are the test
func TestRewriteCompactReply(t *testing.T) {
	settled, err := catalog.NewUsage(map[catalog.Item]int64{
		catalog.ItemInput: 5, catalog.ItemCacheRead: 2, catalog.ItemOutput: 3, catalog.ItemReasoning: 1,
	}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := protocol.UsageOf([]byte(`{"usage":{"input_tokens":7,"output_tokens":1}}`))
	for _, tc := range []struct {
		name, body, prefix, suffix string
		fin                        *protocol.Final
		input, output              int64
		pending                    bool
	}{
		{
			name: "usage replaced in place", body: `{"id":"c1","usage":{"input_tokens":1},"output":[]}`,
			prefix: `{"id":"c1","usage":`, suffix: `,"output":[]}`, input: 7, output: 4,
			fin: &protocol.Final{Usage: protocol.Usage{Segments: []protocol.Segment{{Usage: settled}}}, Currency: usd},
		},
		{
			name: "usage added at the end", body: `{"id":"c1"}`,
			prefix: `{"id":"c1","usage":`, suffix: `}`, input: 7, output: 4,
			fin: &protocol.Final{Usage: protocol.Usage{Segments: []protocol.Segment{{Usage: settled}}}, Currency: usd},
		},
		{
			name: "a settlement that was not written", body: `{"id":"c1","usage":{"input_tokens":1}}`,
			prefix: `{"id":"c1","usage":`, suffix: `,"axiom":{"billing_pending":true}}`, input: 7, output: 1, pending: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := protocol.RewriteCompactReply([]byte(tc.body), seen, tc.fin)
			if err != nil {
				t.Fatal(err)
			}
			body := string(out)
			if !strings.HasPrefix(body, tc.prefix) || !strings.HasSuffix(body, tc.suffix) {
				t.Fatalf("the reply is %s, want %s…%s", body, tc.prefix, tc.suffix)
			}
			u := protocol.UsageOf(out)
			if u.Invalid != nil {
				t.Fatal(u.Invalid)
			}
			if got := u.Segments[0].Usage.InputTotal(); got != tc.input {
				t.Errorf("input %d, want %d", got, tc.input)
			}
			if got := u.Segments[0].Usage.OutputTotal(); got != tc.output {
				t.Errorf("output %d, want %d", got, tc.output)
			}
			if got := strings.Contains(body, `"cost":`); got == tc.pending {
				t.Errorf("cost member present %t, want %t", got, !tc.pending)
			}
		})
	}
}

// TestValidateCompactReply: only one JSON object is a reply whose usage can be replaced, and nothing may follow it.
func TestValidateCompactReply(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`{"object":"response.compaction"}`, true},
		{`{"usage":{"input_tokens":1}}`, true},
		{`[]`, false},
		{`{"object":"response.compaction"} {}`, false},
		{`{"object":`, false},
	} {
		err := protocol.ValidateCompactReply([]byte(tc.body))
		if tc.ok && err != nil {
			t.Errorf("%s: %v", tc.body, err)
		}
		if !tc.ok && !errors.Is(err, protocol.ErrUpstream) {
			t.Errorf("%s: %v, want an upstream error", tc.body, err)
		}
	}
}
