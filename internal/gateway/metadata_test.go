package gateway

import (
	"context"
	"fmt"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// TestSub2apiHasNoFabricatedSession leaves unrelated conversations to the relay's content fallback when no
// session was declared, for both Messages rewriting and Chat conversion.
func TestSub2apiHasNoFabricatedSession(t *testing.T) {
	t.Parallel()
	for _, format := range []protocol.Format{protocol.Messages, protocol.Chat} {
		for _, value := range []string{`""`, `"  "`, `"\t\n"`, `"\u3000"`, `null`} {
			t.Run(fmt.Sprintf("%d/%s", format, value), func(t *testing.T) {
				for _, content := range []string{"plan a trip", "debug a compiler"} {
					checkNoFabricatedSession(t, format, value, content)
				}
			})
		}
	}
}

func checkNoFabricatedSession(t *testing.T, format protocol.Format, value, content string) {
	t.Helper()
	run(t, func(t *testing.T, ctx context.Context) {
		upstream := protocol.Messages
		h := newHarness(t, ctx, options{client: format, upstream: &upstream, mutate: func(call *Call) {
			member := `"metadata":{"user_id":` + value + `}`
			if format == protocol.Chat {
				member = `"user":` + value
			}
			body := fmt.Sprintf(`{"model":"client-model",%s,"messages":[{"role":"user","content":%q}]}`, member, content)
			turnRequest(t, call, format, clientTurn{body: body})
			for i := range call.Plan.Entries {
				call.Plan.Entries[i].Target.Format = upstream
			}
		}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		calls := h.up.endpoint(1).calls
		if len(calls) != 1 {
			t.Fatalf("got %d upstream calls, want 1", len(calls))
		}
		var body struct {
			Metadata struct {
				UserID string `json:"user_id"`
			} `json:"metadata"`
		}
		if err := jsonx.Unmarshal(calls[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		if id, ok := parseUserID(t, body.Metadata.UserID); ok {
			t.Fatalf("undeclared session was fabricated: %+v", id)
		}
	})
}
