package gateway

import (
	"context"
	"io"
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// TestPendingEndingKeepsBYOK: the settlement may fail, but who served the call is known from the attempt. The pending
// stream, aggregate and direct endings all report is_byok from it (docs/domains/gateway.md「用量与费用」).
func TestPendingEndingKeepsBYOK(t *testing.T) {
	t.Parallel()
	mark := func(c *Call) { c.Plan.Entries[0].Byok = true }
	for _, tc := range []struct {
		name   string
		client protocol.Format
		stream *bool
	}{
		{name: "stream", client: protocol.Chat},
		{name: "aggregate", client: protocol.Chat, stream: new(bool)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run(t, func(t *testing.T, ctx context.Context) {
				o := options{settleErr: io.ErrUnexpectedEOF, client: tc.client, stream: tc.stream, mutate: mark}
				h := newHarness(t, ctx, o)
				h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
				h.run()
				body := h.w.body()
				if !strings.Contains(body, `"billing_pending":true`) || !strings.Contains(body, `"is_byok":true`) {
					t.Errorf("the pending ending:\n%s", body)
				}
			})
		})
	}
}

// TestPendingAggregateKeepsFiles: a file annotation is not part of the settlement, so a pending ending must keep the
// parsed files the aggregate would otherwise attach, exactly as a settled one does.
func TestPendingAggregateKeepsFiles(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		no := false
		h := newHarness(t, ctx, options{
			client: protocol.Chat, stream: &no, settleErr: io.ErrUnexpectedEOF,
			mutate: func(c *Call) {
				c.Plan.Entries[0].Byok = true
				c.files = []protocol.FileAnnotation{{Hash: "abc123", Name: "scan.pdf", Text: "parsed page"}}
			},
		})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		body := h.w.body()
		if !strings.Contains(body, `"billing_pending":true`) || !strings.Contains(body, `"is_byok":true`) {
			t.Errorf("the pending aggregate lost its flags:\n%s", body)
		}
		if !strings.Contains(body, `"annotations"`) || !strings.Contains(body, "scan.pdf") || !strings.Contains(body, "parsed page") {
			t.Errorf("the pending aggregate lost the parsed files:\n%s", body)
		}
	})
}
