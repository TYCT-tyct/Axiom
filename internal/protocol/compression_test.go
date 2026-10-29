package protocol

import (
	"bytes"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
)

func TestMiddleOutPreservesBothEndsAndToolPairs(t *testing.T) {
	body := jsonValue(map[string]any{"model": "a", "plugins": []any{map[string]any{"id": "context-compression"}}, "messages": []any{
		map[string]any{"role": "system", "content": "keep instructions"},
		map[string]any{"role": "user", "content": "first question"},
		map[string]any{"role": "assistant", "content": "first answer"},
		map[string]any{"role": "user", "content": strings.Repeat("middle", 300)},
		map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call", "type": "function", "function": map[string]any{"name": "f", "arguments": "{}"}}}},
		map[string]any{"role": "tool", "tool_call_id": "call", "content": "result"},
		map[string]any{"role": "user", "content": "latest question"},
	}})
	compressed, err := CompressContext(body, 700, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) > 600 || !bytes.Contains(compressed, []byte("first question")) || !bytes.Contains(compressed, []byte("latest question")) || !bytes.Contains(compressed, []byte("keep instructions")) {
		t.Fatalf("bad compression: %s", compressed)
	}
	if bytes.Contains(compressed, []byte("tool_call")) {
		t.Fatalf("middle tool turn survived: %s", compressed)
	}
	if _, err := Inspect(Chat, compressed); err != nil {
		t.Fatal(err)
	}
	again, err := CompressContext(compressed, 700, 100)
	if err != nil || !bytes.Equal(again, compressed) {
		t.Fatalf("not stable: %s, %v", again, err)
	}
}

func TestMiddleOutTruncatesTextWithoutBreakingUnicodeOrProtectedContent(t *testing.T) {
	for _, tc := range []struct {
		role      string
		content   any
		wantError bool
	}{
		{"user", "start" + strings.Repeat("你好😀", 100) + "end", false},
		{"system", strings.Repeat("instruction", 100), true},
		{"user", []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": strings.Repeat("x", 1000)}}}, true},
	} {
		body := jsonValue(map[string]any{"model": "a", "messages": []any{map[string]any{"role": tc.role, "content": tc.content}}})
		out, err := CompressContext(body, 400, 100)
		if tc.wantError {
			if err == nil {
				t.Fatalf("protected content lost: %s", out)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(out) > 300 || jsonx.Validate(out) != nil || !bytes.Contains(out, []byte("start")) || !bytes.Contains(out, []byte("end")) {
			t.Fatalf("invalid: %s", out)
		}
	}
}

func TestCompressionPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy  Compression
		context int64
		want    bool
	}{
		{CompressionDefault, 8192, true}, {CompressionDefault, 8193, false}, {CompressionEnabled, 100000, true}, {CompressionDisabled, 1024, false},
	} {
		r := Request{Format: Chat, Compression: tc.policy}
		if r.Compresses(tc.context) != tc.want {
			t.Fatalf("%+v", tc)
		}
	}
}

func TestMiddleOutMessageLimitAndInsufficientWindow(t *testing.T) {
	messages := make([]jsonx.Raw, CompressionMessageLimit+1)
	for i := range messages {
		messages[i] = jsonx.Raw(`{"role":"user","content":"hello"}`)
	}
	body := jsonValue(map[string]any{"model": "a", "messages": messages})
	out, err := CompressContext(body, int64(len(body)+100), 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Inspect(Chat, out)
	if err != nil || r.MessageCount != CompressionMessageLimit {
		t.Fatalf("count %d: %v", r.MessageCount, err)
	}
	if _, err := CompressContext(body, 100, 100); err == nil {
		t.Fatal("accepted output filling the context")
	}
}

func TestMiddleTextRetainsUnicodeEnds(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"😀" + strings.Repeat("中", 10) + "👋", "😀👋"},
		{"ab€", "a€"},
		{"€ab", "€b"},
		{"a中👋", "a👋"},
	} {
		if got := middleText(tc.text, 100); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestCompressionKeepsUntouchedRawValues(t *testing.T) {
	const opaque = `"opaque": {"big":1e9999,"spelling":1.0000}`
	const signed = `{"role":"assistant","content":[{"type":"thinking","thinking":"unchanged","signature":"a\u002fb"}]}`
	body := []byte(`{"model":"a",` + opaque + `,"messages":[{"role":"user","content":"start` + strings.Repeat("middle", 1000) + `end"},` + signed + `]}`)
	out, err := CompressContext(body, 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(opaque)) || !bytes.Contains(out, []byte(signed)) {
		t.Fatalf("untouched data changed: %s", out)
	}
}

func TestCompressionPreservesToolsAcrossInterleavedMessages(t *testing.T) {
	const call = `{"role":"assistant","tool_calls":[{"id":"call","type":"function","function":{"name":"f","arguments":"{}"}}]}`
	const result = `{"role":"tool","tool_call_id":"call","content":"result"}`
	body := []byte(`{"model":"a","messages":[{"role":"user","content":"first"},` + call + `,{"role":"system","content":"keep policy"},` + result + `,{"role":"user","content":"` + strings.Repeat("middle", 1000) + `"},{"role":"user","content":"last"}]}`)
	out, err := CompressContext(body, 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(call)) || !bytes.Contains(out, []byte(result)) || !bytes.Contains(out, []byte("keep policy")) {
		t.Fatalf("tool pair or system lost: %s", out)
	}
}
