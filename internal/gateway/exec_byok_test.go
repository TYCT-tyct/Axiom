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
