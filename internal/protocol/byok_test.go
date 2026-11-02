package protocol_test

import (
	"bytes"
	"testing"

	"axiom/internal/protocol"
)

// TestIsByokUsage: a call served on the account's own key says so in the usage of every format that has the member,
// and Messages, whose usage has no such member, does not grow one. A pending direct reply still reports it.
func TestIsByokUsage(t *testing.T) {
	t.Parallel()
	for _, f := range []protocol.Format{protocol.Chat, protocol.Completions, protocol.Responses, protocol.Embeddings, protocol.Rerank} {
		_, body := protocol.RenderError(f, protocol.Error{Type: protocol.InvalidRequest}, &protocol.Final{IsBYOK: true})
		if !bytes.Contains(body, []byte(`"is_byok":true`)) {
			t.Errorf("format %v reports %s, want is_byok true", f, body)
		}
	}
	_, body := protocol.RenderError(protocol.Messages, protocol.Error{Type: protocol.InvalidRequest}, &protocol.Final{IsBYOK: true})
	if bytes.Contains(body, []byte("is_byok")) {
		t.Errorf("Messages usage grew is_byok: %s", body)
	}

	reply := []byte(`{"object":"list","data":[],"model":"m","usage":{}}`)
	out, err := protocol.RewriteDirectReply(protocol.Embeddings, reply, "id", "m", protocol.Usage{}, &protocol.Final{IsBYOK: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"is_byok":true`)) {
		t.Errorf("a direct reply on the account's key reports %s, want is_byok true", out)
	}
	out, err = protocol.RewriteDirectReply(protocol.Embeddings, reply, "id", "m", protocol.Usage{}, &protocol.Final{Pending: true, IsBYOK: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"billing_pending":true`)) || !bytes.Contains(out, []byte(`"is_byok":true`)) {
		t.Errorf("a pending direct reply on the account's key reports %s, want billing_pending and is_byok true", out)
	}
}
