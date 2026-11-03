package protocol_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"testing/iotest"

	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/sse"
)

func TestChatToolStopReasons(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ finish, arguments, want string }{
		{"stop", `{\"x\":1}`, "tool_use"},
		{"tool_calls", `{\"x\":1}`, "tool_use"},
		{"length", `{\"x\":1}`, "max_tokens"},
		{"length", `{\"x\":`, "max_tokens"},
	} {
		t.Run(tc.finish+tc.arguments, func(t *testing.T) {
			stream := chatPiece(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"`+tc.arguments+`"}}]}`, `"`+tc.finish+`"`) + ev("[DONE]")
			a := relayTo(t, protocol.Messages, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(1<<20), settled)
			b := relayTo(t, protocol.Messages, protocol.Chat, iotest.OneByteReader(strings.NewReader(stream)), protocol.UsageContext{}, protocol.NewMemory(1<<20), settled)
			if a.err != nil || b.err != nil || !bytes.Equal(a.out, b.out) || !bytes.Contains(a.out, []byte(`"stop_reason":"`+tc.want+`"`)) || !bytes.Contains(a.out, []byte("message_stop")) {
				t.Fatalf("wrong stop or discarded partial tool: %v/%v\n%s", a.err, b.err, a.out)
			}
		})
	}
}

func TestChatThinkingStartsWithEmptySignature(t *testing.T) {
	t.Parallel()
	stream := chatPiece(`{"reasoning_content":"why","content":"answer"}`, `"stop"`) + ev("[DONE]")
	got := relayTo(t, protocol.Messages, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(1<<20), settled)
	if got.err != nil {
		t.Fatal(got.err)
	}
	r := sse.NewReader(bytes.NewReader(got.out), 1<<20)
	found := false
	for data, err := r.Next(); err == nil; data, err = r.Next() {
		if member(t, data, "type") != "content_block_start" {
			continue
		}
		block := member(t, data, "content_block").(map[string]any)
		if block["type"] == "thinking" {
			signature, ok := block["signature"]
			if !ok || signature != "" {
				t.Fatalf("missing empty signature: %s", data)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no thinking block")
	}
}

func BenchmarkChatToNativeDeltas(b *testing.B) {
	for _, format := range formats[:2] {
		for _, n := range []int{16, 256} {
			stream := strings.Repeat(chatPiece(`{"content":"a\\b\n\"c"}`, "null"), n) + chatPiece(`{}`, `"stop"`) + ev("[DONE]")
			b.Run(fmt.Sprintf("%s/deltas=%d", format.dir, n), func(b *testing.B) { benchChatToNative(b, format.f, []byte(stream)) })
		}
	}
}
