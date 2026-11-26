package protocol_test

import (
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// Gemini's thought signature, which Google's OpenAI-compatible endpoint puts on a Chat tool call, reaches a Messages
// or Responses client in the call's ID, and a request that hands the call back to a Chat endpoint carries it where
// Google reads it again; a Chat client gets it as Google sent it, whole or streamed.
func TestThoughtSignaturesRoundTrip(t *testing.T) {
	t.Parallel()
	const signature = "c2lnbmF0dXJlKy8="          // standard base64, with characters base64url spells otherwise
	const carried = "call_1_axts_c2lnbmF0dXJlKy8" // what the conversion makes of it
	stream := chatPiece(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"},"extra_content":{"google":{"thought_signature":"`+signature+`"}}}]}`, `"tool_calls"`) +
		ev(`{"id":"c","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`) + ev("[DONE]")
	for _, c := range []struct {
		name    string
		client  protocol.Format
		replied string // where the reply puts the call's ID
		history string // the next request, handing the call and its result back
	}{
		{
			"messages", protocol.Messages, `"id":"` + carried + `"`,
			`"messages":[{"role":"user","content":"Go"},{"role":"assistant","content":[{"type":"tool_use","id":"` + carried + `","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + carried + `","content":"ok"}]}]`,
		},
		{
			"responses", protocol.Responses, `"call_id":"` + carried + `"`,
			`"input":[{"role":"user","content":"Go"},{"type":"function_call","call_id":"` + carried + `","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"` + carried + `","output":"ok"}]`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := relayTo(t, c.client, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
			if got.err != nil || !strings.Contains(string(got.out), c.replied) {
				t.Fatalf("reply %v, want the call's ID to carry the signature:\n%s", got.err, got.out)
			}
			checkSignatureHandedBack(t, c.client, c.history, signature)
		})
	}
	t.Run("chat", func(t *testing.T) {
		t.Parallel()
		whole := aggregate(t, protocol.Chat, protocol.Chat, strings.NewReader(stream), protocol.NewMemory(64<<20), unsettled)
		if whole.err != nil || !strings.Contains(string(whole.body), `"extra_content":{"google":{"thought_signature":"`+signature+`"}}`) {
			t.Fatalf("aggregate %v:\n%s", whole.err, whole.body)
		}
		got := relayTo(t, protocol.Chat, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
		if got.err != nil || !strings.Contains(string(got.out), signature) {
			t.Fatalf("stream %v:\n%s", got.err, got.out)
		}
	})
}

// checkSignatureHandedBack: the next request, converted to Chat, carries the signature on the call's own ID.
func checkSignatureHandedBack(t *testing.T, client protocol.Format, history, signature string) {
	t.Helper()
	up, err := prepareNativeChat(client, chatBody(history), "up", nil)
	if err != nil {
		t.Fatal(err)
	}
	body := string(up.Body)
	if !strings.Contains(body, `"extra_content":{"google":{"thought_signature":"`+signature+`"}}`) ||
		strings.Contains(body, "_axts_") || strings.Count(body, `"call_1"`) != 2 {
		t.Fatalf("the Chat request does not hand the signature back on the call's own ID:\n%s", body)
	}
}
